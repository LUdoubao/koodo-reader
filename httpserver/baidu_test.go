package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Test helpers ─────────────────────────────────────────────────────────────
func testBaiduConfig() baiduConfig {
	return baiduConfig{
		appKey:       "test-app-key",
		secretKey:    "test-secret",
		redirectURI:  "https://reader.example.com/baidu-api/callback",
		appName:      "koodo-reader",
		maxFileBytes: 10 << 20,
		cookieSecure: true,
	}
}

func withBaiduConfig(t *testing.T, cfg baiduConfig) {
	t.Helper()
	previous := baiduCfg
	baiduCfg = cfg
	t.Cleanup(func() { baiduCfg = previous })
}

func resetBaiduRefreshCache(t *testing.T) {
	t.Helper()
	clear := func() {
		baiduRefreshMu.Lock()
		baiduRefreshEntries = map[string]*baiduRefreshEntry{}
		baiduRefreshMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// withBaiduUpstream points both Baidu endpoints at one test server and restores
// the real endpoints afterwards.
func withBaiduUpstream(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	previousToken, previousPan := baiduTokenEndpoint, baiduPanEndpoint
	baiduTokenEndpoint = server.URL + "/oauth/token"
	baiduPanEndpoint = server.URL + "/rest/2.0/xpan"
	t.Cleanup(func() {
		baiduTokenEndpoint, baiduPanEndpoint = previousToken, previousPan
		server.Close()
	})
	return server
}

func withValidAccessToken(req *http.Request) {
	req.AddCookie(&http.Cookie{Name: baiduAccessCookie, Value: "access-valid"})
	req.AddCookie(&http.Cookie{
		Name:  baiduExpiryCookie,
		Value: strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10),
	})
}

func recorderCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func baiduJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

func baiduRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "reader.example.com"
	return req
}

// ── Status and authorization ─────────────────────────────────────────────────

func TestBaiduStatusDisabledWithoutConfig(t *testing.T) {
	withBaiduConfig(t, baiduConfig{})

	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, baiduRequest(http.MethodGet, "/baidu-api/status"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := baiduJSON(t, rec)
	if body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", body["enabled"])
	}
}

func TestBaiduAuthDisabledReturns503(t *testing.T) {
	withBaiduConfig(t, baiduConfig{})

	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, baiduRequest(http.MethodGet, "/baidu-api/auth"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestBaiduAuthRedirectsWithStateCookie(t *testing.T) {
	cfg := testBaiduConfig()
	withBaiduConfig(t, cfg)

	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, baiduRequest(http.MethodGet, "/baidu-api/auth"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("invalid Location: %v", err)
	}
	if location.Host != "openapi.baidu.com" || location.Path != "/oauth/2.0/authorize" {
		t.Fatalf("unexpected authorize URL: %s", location)
	}
	query := location.Query()
	if query.Get("response_type") != "code" || query.Get("client_id") != cfg.appKey {
		t.Fatalf("unexpected authorize params: %v", query)
	}
	if query.Get("scope") != "basic,netdisk" || query.Get("redirect_uri") != cfg.redirectURI {
		t.Fatalf("unexpected authorize params: %v", query)
	}
	state := query.Get("state")
	if len(state) != 64 {
		t.Fatalf("state = %q, want 64 hex characters", state)
	}

	cookie := recorderCookie(rec, baiduStateCookie)
	if cookie == nil || cookie.Value != state {
		t.Fatalf("state cookie missing or mismatched: %#v", cookie)
	}
	if !cookie.HttpOnly || cookie.Path != "/baidu-api/callback" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie attributes are unsafe: %#v", cookie)
	}
	if cookie.MaxAge != baiduStateMaxAge || !cookie.Secure {
		t.Fatalf("state cookie lifetime/secure mismatch: %#v", cookie)
	}
}

func TestBaiduAuthUsesConfiguredCallbackPath(t *testing.T) {
	cfg := testBaiduConfig()
	cfg.redirectURI = "https://reader.example.com/flow/api/callback/baidu"
	withBaiduConfig(t, cfg)
	if !cfg.enabled() {
		t.Fatalf("custom callback path rejected: %v", cfg.missingFields())
	}

	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, baiduRequest(http.MethodGet, "/baidu-api/auth"))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("invalid Location: %v", err)
	}
	if got := location.Query().Get("redirect_uri"); got != cfg.redirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, cfg.redirectURI)
	}
	cookie := recorderCookie(rec, baiduStateCookie)
	if cookie == nil || cookie.Path != "/flow/api/callback/baidu" {
		t.Fatalf("state cookie must follow the callback path: %#v", cookie)
	}
}

func TestBaiduConfigRejectsCallbackWithoutPath(t *testing.T) {
	cfg := testBaiduConfig()
	cfg.redirectURI = "https://reader.example.com"
	if cfg.enabled() {
		t.Fatal("redirect URI without a callback path must stay disabled")
	}
}

func TestBaiduCallbackRejectsMissingOrWrongState(t *testing.T) {
	cfg := testBaiduConfig()
	withBaiduConfig(t, cfg)

	cases := []struct {
		name        string
		cookieState string
		queryState  string
	}{
		{"missing cookie", "", "state-1"},
		{"missing query state", "state-1", ""},
		{"mismatch", "state-1", "state-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baiduRequest(http.MethodGet, "/baidu-api/callback?code=code-1&state="+tc.queryState)
			if tc.cookieState != "" {
				req.AddCookie(&http.Cookie{Name: baiduStateCookie, Value: tc.cookieState})
			}
			rec := httptest.NewRecorder()
			baiduAPIHandler(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "access_token") {
				t.Fatalf("error page leaked token material: %s", rec.Body.String())
			}
			if cookie := recorderCookie(rec, baiduStateCookie); cookie == nil || cookie.MaxAge >= 0 {
				t.Fatalf("state cookie was not cleared: %#v", cookie)
			}
		})
	}
}

func TestBaiduCallbackExchangesCodeAndSetsCookies(t *testing.T) {
	cfg := testBaiduConfig()
	withBaiduConfig(t, cfg)
	resetBaiduRefreshCache(t)

	var tokenCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&tokenCalls, 1)
		query := r.URL.Query()
		if query.Get("grant_type") != "authorization_code" || query.Get("code") != "code-1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if query.Get("client_id") != cfg.appKey || query.Get("client_secret") != cfg.secretKey {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/callback?code=code-1&state=state-1")
	req.AddCookie(&http.Cookie{Name: baiduStateCookie, Value: "state-1"})
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"koodo-baidu-oauth"`) {
		t.Fatalf("success page is missing the postMessage contract: %s", rec.Body.String())
	}
	for name, want := range map[string]string{
		baiduAccessCookie:  "access-1",
		baiduRefreshCookie: "refresh-1",
	} {
		cookie := recorderCookie(rec, name)
		if cookie == nil || cookie.Value != want {
			t.Fatalf("%s cookie = %#v, want %q", name, cookie, want)
		}
		if !cookie.HttpOnly || cookie.Path != baiduCookiePath {
			t.Fatalf("%s cookie attributes are unsafe: %#v", name, cookie)
		}
	}
	if cookie := recorderCookie(rec, baiduExpiryCookie); cookie == nil || cookie.Value == "" {
		t.Fatalf("expiry cookie missing")
	}
	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls)
	}
}

// ── Refresh behaviour ────────────────────────────────────────────────────────

func TestBaiduRefreshIsSingleFlight(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var tokenCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			atomic.AddInt32(&tokenCalls, 1)
			// Hold the first refresh open so the other requests really overlap.
			time.Sleep(50 * time.Millisecond)
			if r.URL.Query().Get("grant_type") != "refresh_token" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-new",
				"refresh_token": "refresh-new",
				"expires_in":    3600,
			})
		case "/rest/2.0/xpan/file":
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))

	const clients = 8
	var waitGroup sync.WaitGroup
	codes := make([]int, clients)
	cookies := make([]string, clients)
	for i := 0; i < clients; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
			req.AddCookie(&http.Cookie{Name: baiduRefreshCookie, Value: "refresh-old"})
			rec := httptest.NewRecorder()
			baiduAPIHandler(rec, req)
			codes[index] = rec.Code
			if cookie := recorderCookie(rec, baiduRefreshCookie); cookie != nil {
				cookies[index] = cookie.Value
			}
		}(i)
	}
	waitGroup.Wait()

	for index, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("client %d status = %d, want 200", index, code)
		}
		// Every response must carry the rotated token, otherwise a browser that
		// reuses its old cookie would break on the next refresh.
		if cookies[index] != "refresh-new" {
			t.Fatalf("client %d refresh cookie = %q, want refresh-new", index, cookies[index])
		}
	}
	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Fatalf("token refresh calls = %d, want 1", tokenCalls)
	}
}

// TestBaiduForceRefreshIsSingleFlightOnErrno111 covers two requests that both
// observe 111 with a still-valid-looking access token: the forced refresh must
// share the per-token single-flight, otherwise the second one spends an
// already rotated refresh token and the user is logged out.
func TestBaiduForceRefreshIsSingleFlightOnErrno111(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var tokenCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			atomic.AddInt32(&tokenCalls, 1)
			// Hold the refresh open so both requests really overlap.
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-new",
				"refresh_token": "refresh-new",
				"expires_in":    3600,
			})
		case "/rest/2.0/xpan/file":
			if r.URL.Query().Get("access_token") == "access-valid" {
				// The token still looks valid but Baidu has revoked it.
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": 111})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))

	const clients = 2
	var waitGroup sync.WaitGroup
	codes := make([]int, clients)
	cookies := make([]string, clients)
	for i := 0; i < clients; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
			withValidAccessToken(req)
			req.AddCookie(&http.Cookie{Name: baiduRefreshCookie, Value: "refresh-old"})
			rec := httptest.NewRecorder()
			baiduAPIHandler(rec, req)
			codes[index] = rec.Code
			if cookie := recorderCookie(rec, baiduRefreshCookie); cookie != nil {
				cookies[index] = cookie.Value
			}
		}(i)
	}
	waitGroup.Wait()

	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Fatalf("token refresh calls = %d, want 1", calls)
	}
	for index, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("client %d status = %d, want 200", index, code)
		}
		if cookies[index] != "refresh-new" {
			t.Fatalf("client %d refresh cookie = %q, want refresh-new", index, cookies[index])
		}
	}
}

// TestBaiduRefreshEntriesArePruned keeps the refresh cache bounded: rotated
// refresh tokens are never queried again, so only completed-and-expired
// entries may be dropped, never an in-flight one that still has waiters.
func TestBaiduRefreshEntriesArePruned(t *testing.T) {
	resetBaiduRefreshCache(t)

	completed := make(chan struct{})
	close(completed)
	inFlight := make(chan struct{})
	now := time.Now()

	baiduRefreshMu.Lock()
	defer baiduRefreshMu.Unlock()
	baiduRefreshEntries = map[string]*baiduRefreshEntry{
		"expired":  {done: completed, expires: now.Add(-time.Second)},
		"reusable": {done: completed, expires: now.Add(time.Minute)},
		"inflight": {done: inFlight},
	}
	baiduPruneRefreshEntries(now)

	if _, ok := baiduRefreshEntries["expired"]; ok {
		t.Fatalf("expired entry was not pruned")
	}
	if _, ok := baiduRefreshEntries["reusable"]; !ok {
		t.Fatalf("entry still inside its reuse window was pruned")
	}
	if _, ok := baiduRefreshEntries["inflight"]; !ok {
		t.Fatalf("in-flight entry was pruned")
	}
}

func TestBaiduRefreshFailureIsUnauthorized(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "refresh token is invalid",
		})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
	req.AddCookie(&http.Cookie{Name: baiduRefreshCookie, Value: "refresh-old"})
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := baiduJSON(t, rec); body["error"] != "reauth_required" {
		t.Fatalf("error = %v, want reauth_required", body["error"])
	}
	// A failed refresh must not clear the cookie: parallel requests would
	// otherwise overwrite each other and log the user out.
	if cookie := recorderCookie(rec, baiduRefreshCookie); cookie != nil && cookie.MaxAge < 0 {
		t.Fatalf("refresh cookie was cleared on failure: %#v", cookie)
	}
}

// ── Listing ──────────────────────────────────────────────────────────────────

func TestBaiduListCreatesRootDirectoryOnMinus9(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var listCalls, createCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/2.0/xpan/file" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			atomic.AddInt32(&createCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0})
			return
		}
		if atomic.AddInt32(&listCalls, 1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": -9, "show_msg": "directory does not exist"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errno": 0,
			"list": []any{
				map[string]any{
					"fs_id":           111,
					"path":            "/apps/koodo-reader/Books",
					"server_filename": "Books",
					"isdir":           1,
				},
				map[string]any{
					"fs_id":           1234567890123456789,
					"path":            "/apps/koodo-reader/Book.epub",
					"server_filename": "Book.epub",
					"size":            1024,
					"isdir":           0,
					"server_mtime":    1700000000,
				},
			},
		})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&createCalls) != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls)
	}
	if atomic.LoadInt32(&listCalls) != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}

	body := baiduJSON(t, rec)
	files, ok := body["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("files = %#v, want 2 entries", body["files"])
	}
	everything := rec.Body.String()
	// fs_id is a uint64: it must reach the browser as a string, never as a
	// rounded number.
	if !strings.Contains(everything, `"fsid":"1234567890123456789"`) {
		t.Fatalf("fs_id was not preserved as text: %s", everything)
	}
	if !strings.Contains(everything, `"type":"folder"`) || !strings.Contains(everything, `"type":"file"`) {
		t.Fatalf("entry types missing: %s", everything)
	}
	if !strings.Contains(everything, `"path":"/Books"`) {
		t.Fatalf("folder path missing: %s", everything)
	}
}

func TestBaiduCreateDirectoryTreatsMinus8AsSuccess(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var listCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// errno -8 means the directory already exists; it is not an error.
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": -8, "show_msg": "file already exists"})
			return
		}
		if atomic.AddInt32(&listCalls, 1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": -9})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{}})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&listCalls) != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}
}

func TestBaiduListSubdirectoryMinus9IsNotFound(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var createCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt32(&createCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": -9, "show_msg": "directory does not exist"})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/list?path=/Missing")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if atomic.LoadInt32(&createCalls) != 0 {
		t.Fatalf("a subdirectory must never trigger create, got %d calls", createCalls)
	}
}

func TestBaiduListErrno31066IsForbidden(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 31066, "show_msg": "app folder is not allowed"})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/list?path=")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestBaiduListRejectsTraversalPath(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var upstreamCalls int32
	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{}})
	}))

	for _, path := range []string{"/../secret", "/Books/../../secret", "Books", "/Books\\\\Sub", "/Books//Sub"} {
		req := baiduRequest(http.MethodGet, "/baidu-api/list?path="+url.QueryEscape(path))
		withValidAccessToken(req)
		rec := httptest.NewRecorder()
		baiduAPIHandler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path %q status = %d, want 400", path, rec.Code)
		}
	}
	if atomic.LoadInt32(&upstreamCalls) != 0 {
		t.Fatalf("invalid paths reached the upstream %d times", upstreamCalls)
	}
}

func TestBaiduRelativePathTable(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"", false},
		{"/Books", false},
		{"/Books/2024", false},
		{"Books", true},
		{"/../etc", true},
		{"/Books/../etc", true},
		{"/Books/./2024", true},
		{"/Books//2024", true},
		{`/Books\2024`, true},
		{"/Books\n2024", true},
		{"/" + strings.Repeat("a", 1100), true},
	}
	for _, tc := range cases {
		_, err := baiduRelativePath(tc.path)
		if (err != nil) != tc.wantErr {
			t.Fatalf("baiduRelativePath(%q) error = %v, wantErr %v", tc.path, err, tc.wantErr)
		}
	}
}

func TestBaiduBookFileWhitelist(t *testing.T) {
	for _, name := range []string{"book.epub", "BOOK.PDF", "notes.md", "scan.cbz", "paper.docx"} {
		if !baiduIsBookFile(name) {
			t.Fatalf("%q should be accepted", name)
		}
	}
	for _, name := range []string{"book.exe", "book.zip", "book.iso", "book", "book.txt.exe"} {
		if baiduIsBookFile(name) {
			t.Fatalf("%q should be rejected", name)
		}
	}
}

// ── Downloads ────────────────────────────────────────────────────────────────

func TestBaiduDownloadRejectsNonNumericFsid(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	req := baiduRequest(http.MethodGet, "/baidu-api/download?fsid=12ab")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBaiduDownloadRejectsFilesOutsideAppDirectory(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errno": 0,
			"list": []any{map[string]any{
				"fs_id":           42,
				"path":            "/apps/another-app/book.epub",
				"server_filename": "book.epub",
				"size":            100,
				"isdir":           0,
				"dlink":           "https://pan.baidu.com/rest/2.0/xpan/file?dlink=1",
			}},
		})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/download?fsid=42")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestBaiduDownloadRejectsOversizeFile(t *testing.T) {
	cfg := testBaiduConfig()
	cfg.maxFileBytes = 1 << 20
	withBaiduConfig(t, cfg)
	resetBaiduRefreshCache(t)

	withBaiduUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errno": 0,
			"list": []any{map[string]any{
				"fs_id":           42,
				"path":            "/apps/koodo-reader/big.epub",
				"server_filename": "big.epub",
				"size":            cfg.maxFileBytes + 1,
				"isdir":           0,
				"dlink":           "https://pan.baidu.com/rest/2.0/xpan/file?dlink=1",
			}},
		})
	}))

	req := baiduRequest(http.MethodGet, "/baidu-api/download?fsid=42")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBaiduDownloadStreamsAndSetsHeaders(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var downloadURL string
	var downloadUA, downloadToken string
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/2.0/xpan/multimedia", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errno": 0,
			"list": []any{map[string]any{
				"fs_id":    42,
				"path":     "/apps/koodo-reader/Books/a b.epub",
				"filename": "a b.epub",
				"size":     5,
				"isdir":    0,
				"dlink":    downloadURL,
			}},
		})
	})
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) {
		downloadUA = r.Header.Get("User-Agent")
		downloadToken = r.URL.Query().Get("access_token")
		w.Header().Set("Content-Length", "5")
		_, _ = w.Write([]byte("hello"))
	})
	server := withBaiduUpstream(t, mux)
	downloadURL = server.URL + "/dl"

	withAllowInsecureBaiduDownloads(t)

	req := baiduRequest(http.MethodGet, "/baidu-api/download?fsid=42")
	withValidAccessToken(req)
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello", rec.Body.String())
	}
	if downloadUA != baiduUserAgent {
		t.Fatalf("upstream User-Agent = %q, want %q", downloadUA, baiduUserAgent)
	}
	if downloadToken != "access-valid" {
		t.Fatalf("upstream access_token = %q, want access-valid", downloadToken)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "filename*=UTF-8''a%20b.epub") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
}

func TestBaiduDownloadRetriesOnceOnAuthErrno(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())
	resetBaiduRefreshCache(t)

	var metadataCalls, tokenCalls int32
	var downloadURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-refreshed",
			"refresh_token": "refresh-rotated",
			"expires_in":    3600,
		})
	})
	mux.HandleFunc("/rest/2.0/xpan/multimedia", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&metadataCalls, 1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 111, "show_msg": "token expired"})
			return
		}
		if r.URL.Query().Get("access_token") != "access-refreshed" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errno": 0,
			"list": []any{map[string]any{
				"fs_id":           42,
				"path":            "/apps/koodo-reader/retry.epub",
				"server_filename": "retry.epub",
				"size":            2,
				"isdir":           0,
				"dlink":           downloadURL,
			}},
		})
	})
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	server := withBaiduUpstream(t, mux)
	downloadURL = server.URL + "/dl"
	withAllowInsecureBaiduDownloads(t)

	req := baiduRequest(http.MethodGet, "/baidu-api/download?fsid=42")
	withValidAccessToken(req)
	req.AddCookie(&http.Cookie{Name: baiduRefreshCookie, Value: "refresh-old"})
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&metadataCalls) != 2 {
		t.Fatalf("metadata calls = %d, want 2 (retry once)", metadataCalls)
	}
	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Fatalf("refresh calls = %d, want 1", tokenCalls)
	}
	if cookie := recorderCookie(rec, baiduRefreshCookie); cookie == nil || cookie.Value != "refresh-rotated" {
		t.Fatalf("rotated refresh cookie missing: %#v", cookie)
	}
}

func TestBaiduValidateDownloadURL(t *testing.T) {
	previousInsecure := baiduAllowInsecureDownload
	previousHost := baiduHostAllowed
	baiduAllowInsecureDownload = false
	baiduHostAllowed = baiduDownloadHostAllowed
	t.Cleanup(func() {
		baiduAllowInsecureDownload = previousInsecure
		baiduHostAllowed = previousHost
	})

	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://pan.baidu.com/rest/2.0/xpan/file?dlink=1", false},
		{"https://pcs.baidupcs.com/file/abc", false},
		{"https://d.pcs.baidu.com/file/abc", false},
		{"https://evil.example.com/file", true},
		{"https://pan.baidu.com.evil.example/file", true},
		{"https://baidu.com.evil/file", true},
		{"http://pan.baidu.com/file", true},
		{"ftp://pan.baidu.com/file", true},
		{"https://user:pass@evil.example/file", true},
	}
	for _, tc := range cases {
		parsed, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("invalid test URL %q: %v", tc.raw, err)
		}
		if err := baiduValidateDownloadURL(parsed); (err != nil) != tc.wantErr {
			t.Fatalf("baiduValidateDownloadURL(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
	}
}

func TestBaiduRedirectKeepsUserAgentAndValidatesHost(t *testing.T) {
	withAllowInsecureBaiduDownloads(t)

	var userAgent string
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("payload"))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/final", http.StatusFound)
	}))
	defer redirector.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL+"/start", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("User-Agent", baiduUserAgent)
	resp, err := baiduDownloadClient.Do(req)
	if err != nil {
		t.Fatalf("redirected request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if userAgent != baiduUserAgent {
		t.Fatalf("redirected User-Agent = %q, want %q", userAgent, baiduUserAgent)
	}

	// A foreign redirect target must be rejected before the follow-up request.
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/file", http.StatusFound)
	}))
	defer foreign.Close()
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, foreign.URL+"/start", nil)
	if _, err := baiduDownloadClient.Do(req); err == nil {
		t.Fatalf("cross-host redirect was followed")
	}
}

func withAllowInsecureBaiduDownloads(t *testing.T) {
	t.Helper()
	previousInsecure := baiduAllowInsecureDownload
	previousHost := baiduHostAllowed
	baiduAllowInsecureDownload = true
	baiduHostAllowed = func(host string) bool { return host == "127.0.0.1" || host == "::1" || baiduDownloadHostAllowed(host) }
	t.Cleanup(func() {
		baiduAllowInsecureDownload = previousInsecure
		baiduHostAllowed = previousHost
	})
}

// ── Router and logout ────────────────────────────────────────────────────────

func TestBaiduRelayBypassesBasicAuth(t *testing.T) {
	withAllowedOrigins(t, buildAllowedOrigins(""))
	withBaiduConfig(t, baiduConfig{})

	statusReq := httptest.NewRequest(http.MethodGet, "/baidu-api/status", nil)
	statusRec := httptest.NewRecorder()
	handler(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint without Basic Auth = %d, want 200", statusRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/list", nil)
	listRec := httptest.NewRecorder()
	handler(listRec, listReq)
	if listRec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy /list without Basic Auth = %d, want 401", listRec.Code)
	}

	// A look-alike prefix must not inherit the relay's auth bypass.
	lookalikeReq := httptest.NewRequest(http.MethodGet, "/baidu-api-evil/status", nil)
	lookalikeRec := httptest.NewRecorder()
	handler(lookalikeRec, lookalikeReq)
	if lookalikeRec.Code != http.StatusUnauthorized {
		t.Fatalf("/baidu-api-evil without Basic Auth = %d, want 401", lookalikeRec.Code)
	}
}

func TestBaiduLogout(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())

	crossOrigin := baiduRequest(http.MethodPost, "/baidu-api/logout")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossRec := httptest.NewRecorder()
	baiduAPIHandler(crossRec, crossOrigin)
	if crossRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout = %d, want 403", crossRec.Code)
	}

	sameOrigin := baiduRequest(http.MethodPost, "/baidu-api/logout")
	sameOrigin.Header.Set("Origin", "http://reader.example.com")
	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, sameOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	for _, name := range []string{baiduAccessCookie, baiduExpiryCookie, baiduRefreshCookie, baiduStateCookie} {
		cookie := recorderCookie(rec, name)
		if cookie == nil || cookie.MaxAge >= 0 {
			t.Fatalf("%s was not cleared: %#v", name, cookie)
		}
	}
}

func TestBaiduUnknownRouteIs404(t *testing.T) {
	withBaiduConfig(t, testBaiduConfig())

	rec := httptest.NewRecorder()
	baiduAPIHandler(rec, baiduRequest(http.MethodGet, "/baidu-api/unknown"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestBaiduDownloadRedirectLoopIsLimited(t *testing.T) {
	withAllowInsecureBaiduDownloads(t)

	var hits int32
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer loop.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, loop.URL+"/start", nil)
	if _, err := baiduDownloadClient.Do(req); err == nil {
		t.Fatalf("redirect loop was not stopped")
	}
	if hits > baiduMaxRedirects {
		t.Fatalf("redirect hop count = %d, want at most %d", hits, baiduMaxRedirects)
	}
}

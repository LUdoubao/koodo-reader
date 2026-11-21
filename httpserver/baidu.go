package main

// Baidu Netdisk relay for the Web import dialog.
//
// The Web build ships no Node runtime and a browser cannot set the
// "User-Agent: pan.baidu.com" header that Baidu requires, so every Netdisk call
// is relayed through this Go service under the same-origin /baidu-api prefix.
// Tokens live only in HttpOnly cookies and in this process; the browser never
// receives them. Only the application-owned directory /apps/{BAIDU_APP_NAME}
// is ever listed, read or downloaded.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	baiduAPIPrefix     = "/baidu-api"
	baiduCallbackPath  = baiduAPIPrefix + "/callback"
	baiduCookiePath    = baiduAPIPrefix
	baiduAccessCookie  = "baidu_access_token"
	baiduExpiryCookie  = "baidu_access_expiry"
	baiduRefreshCookie = "baidu_refresh_token"
	baiduStateCookie   = "baidu_oauth_state"
	baiduUserAgent     = "pan.baidu.com"
	baiduPageLimit     = 1000
	baiduMaxPages      = 10

	baiduRefreshLeadTime   = 60 * time.Second
	baiduRefreshCacheTTL   = 30 * time.Second
	baiduRefreshMaxAge     = 10 * 365 * 24 * 60 * 60
	baiduStateMaxAge       = 600
	baiduDefaultMaxFileMB  = 200
	baiduDownloadChunkSize = 64 * 1024
	baiduMaxRedirects      = 5
	baiduWriteTimeout      = 30 * time.Minute
)

var (
	// baiduTokenEndpoint and baiduPanEndpoint are variables only so tests can
	// point them at an httptest server.
	baiduTokenEndpoint = "https://openapi.baidu.com/oauth/2.0/token"
	baiduPanEndpoint   = "https://pan.baidu.com/rest/2.0/xpan"

	baiduUpstreamClient = &http.Client{Timeout: 60 * time.Second}
	baiduDownloadClient = &http.Client{CheckRedirect: baiduCheckDownloadRedirect}

	// baiduHostAllowed is swapped in tests; production always uses the
	// Baidu-owned host allowlist.
	baiduHostAllowed           = baiduDownloadHostAllowed
	baiduAllowInsecureDownload = false
)

var (
	errBaiduDisabled      = errors.New("Baidu Netdisk is not configured on this server")
	errBaiduReauth        = errors.New("Baidu Netdisk reauthorization required")
	errBaiduInvalidPath   = errors.New("invalid Baidu path")
	errBaiduInvalidID     = errors.New("invalid Baidu file id")
	errBaiduTokenExchange = errors.New("Baidu token exchange failed")
	errBaiduUpstream      = errors.New("Baidu upstream request failed")
)

// ── Configuration ────────────────────────────────────────────────────────────

// baiduConfig is the immutable server-side Baidu configuration. All fields that
// Baidu requires to consider the relay usable are validated by enabled().
type baiduConfig struct {
	appKey       string
	secretKey    string
	redirectURI  string
	appName      string
	appID        string
	maxFileBytes int64
	cookieSecure bool
}

var (
	baiduCfg     = loadBaiduConfig()
	baiduEnabled = baiduCfg.enabled()
)

// loadBaiduConfig reads the BAIDU_* environment once at start-up. It logs only
// the names of missing settings, never their values or the secret itself.
func loadBaiduConfig() baiduConfig {
	cfg := baiduConfig{
		appKey:       strings.TrimSpace(os.Getenv("BAIDU_APP_KEY")),
		appName:      getEnv("BAIDU_APP_NAME", "koodo-reader"),
		appID:        strings.TrimSpace(os.Getenv("BAIDU_APP_ID")),
		redirectURI:  strings.TrimSpace(os.Getenv("BAIDU_REDIRECT_URI")),
		maxFileBytes: int64(baiduDefaultMaxFileMB) << 20,
	}

	// Secret key: environment first, then the Docker secret file (default
	// "baidu_secret") so docker-compose can keep the secret off the command line.
	if v := strings.TrimSpace(os.Getenv("BAIDU_SECRET_KEY")); v != "" {
		cfg.secretKey = v
	} else {
		cfg.secretKey = getDockerSecret(getEnv("BAIDU_SECRET_KEY_FILE", "baidu_secret"))
	}

	if raw := strings.TrimSpace(os.Getenv("BAIDU_MAX_FILE_MB")); raw != "" {
		if mb, err := strconv.Atoi(raw); err == nil && mb > 0 {
			cfg.maxFileBytes = int64(mb) << 20
		} else {
			log.Printf("Warning: invalid BAIDU_MAX_FILE_MB value; using %d MB", baiduDefaultMaxFileMB)
		}
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv("BAIDU_COOKIE_SECURE"))) {
	case "true", "1", "yes":
		cfg.cookieSecure = true
	case "false", "0", "no":
		cfg.cookieSecure = false
	default:
		// Caddy terminates TLS in front of this service, so r.TLS is nil here:
		// the Secure attribute has to be derived from the public callback URI.
		cfg.cookieSecure = strings.HasPrefix(strings.ToLower(cfg.redirectURI), "https://")
	}

	if missing := cfg.missingFields(); len(missing) > 0 && (cfg.appKey != "" || cfg.secretKey != "" || cfg.redirectURI != "") {
		log.Printf("Baidu Netdisk disabled: check %s", strings.Join(missing, ", "))
	}

	return cfg
}

// missingFields returns the configuration problems that keep the relay
// disabled. An empty slice means the relay is ready.
func (c baiduConfig) missingFields() []string {
	var missing []string
	if c.appKey == "" {
		missing = append(missing, "BAIDU_APP_KEY")
	}
	if c.secretKey == "" {
		missing = append(missing, "BAIDU_SECRET_KEY")
	}
	if c.redirectURI == "" {
		missing = append(missing, "BAIDU_REDIRECT_URI")
	} else if u, err := url.Parse(c.redirectURI); err != nil || u.Scheme == "" || u.Host == "" || u.Path != baiduCallbackPath {
		missing = append(missing, "BAIDU_REDIRECT_URI(path must be "+baiduCallbackPath+")")
	}
	return missing
}

// enabled reports whether every required setting is present and valid. A
// half-configured deployment stays fail-closed: no entry point, no auth flow.
func (c baiduConfig) enabled() bool {
	return len(c.missingFields()) == 0
}

// appDirectory is the only remote directory this relay may touch.
func (c baiduConfig) appDirectory() string {
	name := strings.Trim(strings.TrimSpace(c.appName), "/")
	if name == "" || strings.Contains(name, "..") {
		name = "koodo-reader"
	}
	return "/apps/" + name
}

// ── Errors ───────────────────────────────────────────────────────────────────

// baiduErrno carries the Baidu errno so callers can map it to an HTTP status
// without losing the upstream message.
type baiduErrno struct {
	errno   int
	message string
}

func (e *baiduErrno) Error() string { return e.message }

func baiduErrnoError(errno int, showMsg string) error {
	msg := strings.TrimSpace(showMsg)
	if msg == "" {
		msg = fmt.Sprintf("Baidu returned errno %d", errno)
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &baiduErrno{errno: errno, message: msg}
}

// baiduIsAuthError reports the errnos Baidu uses for an expired or revoked
// access token. Those are refreshed and retried exactly once.
func baiduIsAuthError(err error) bool {
	var e *baiduErrno
	if errors.As(err, &e) {
		return e.errno == 111 || e.errno == -6
	}
	return false
}

// baiduStatusForError maps a relay error to an HTTP status. The public error
// code (see baiduWriteError) is derived from the status, so the two stay in sync.
func baiduStatusForError(err error) (int, string) {
	var e *baiduErrno
	switch {
	case errors.Is(err, errBaiduDisabled):
		return http.StatusServiceUnavailable, "upstream"
	case errors.Is(err, errBaiduReauth):
		return http.StatusUnauthorized, "reauth_required"
	case errors.Is(err, errBaiduInvalidPath), errors.Is(err, errBaiduInvalidID):
		return http.StatusBadRequest, "invalid_id"
	case errors.As(err, &e):
		switch e.errno {
		case 111, -6:
			return http.StatusUnauthorized, "reauth_required"
		case -9:
			return http.StatusNotFound, "not_found"
		case 31066:
			return http.StatusForbidden, "forbidden"
		default:
			return http.StatusBadGateway, "upstream"
		}
	default:
		return http.StatusBadGateway, "upstream"
	}
}

func baiduWriteError(w http.ResponseWriter, status int, code string, errno int, message string) {
	payload := map[string]any{"error": code}
	if errno != 0 {
		payload["errno"] = errno
	}
	if message != "" {
		payload["message"] = message
	}
	writeJSON(w, status, payload)
}

func baiduWriteRelayError(w http.ResponseWriter, err error) {
	status, code := baiduStatusForError(err)
	var e *baiduErrno
	errno := 0
	message := ""
	switch {
	case errors.As(err, &e):
		errno = e.errno
		// Only upstream show_msg is echoed, truncated by baiduErrnoError.
		message = e.message
	case errors.Is(err, errBaiduReauth):
		message = "Baidu Netdisk authorization expired, please authorize again"
	case errors.Is(err, errBaiduDisabled):
		message = "Baidu Netdisk is not configured on this server"
	default:
		message = "Baidu Netdisk request failed"
	}
	baiduWriteError(w, status, code, errno, message)
}

// ── Cookies ──────────────────────────────────────────────────────────────────

func baiduSetCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     baiduCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   baiduCfg.cookieSecure,
	})
}

func baiduDeleteCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     baiduCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   baiduCfg.cookieSecure,
	})
}

func baiduCookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// baiduToken is the subset of the Baidu token response this relay needs. The
// raw response is never logged or echoed.
type baiduToken struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func baiduWriteTokenCookies(w http.ResponseWriter, tok baiduToken) {
	maxAge := int(tok.ExpiresIn)
	if maxAge < 0 {
		maxAge = 0
	}
	expiresAt := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
	baiduSetCookie(w, baiduAccessCookie, tok.AccessToken, maxAge)
	baiduSetCookie(w, baiduExpiryCookie, strconv.FormatInt(expiresAt, 10), maxAge)
	// Baidu rotates the refresh token on every refresh; the new value must be
	// written immediately or the next refresh invalidates the session.
	baiduSetCookie(w, baiduRefreshCookie, tok.RefreshToken, baiduRefreshMaxAge)
}

// ── Token refresh ────────────────────────────────────────────────────────────

type baiduRefreshEntry struct {
	done    chan struct{}
	token   baiduToken
	err     error
	expires time.Time
}

var (
	baiduRefreshMu      sync.Mutex
	baiduRefreshEntries = map[string]*baiduRefreshEntry{}
)

// baiduPruneRefreshEntries drops completed entries past their reuse window.
// Baidu rotates the refresh token on every refresh, so the key of an old
// token is never queried again and its entry (including the dead token) would
// otherwise stay in memory for the life of the process.
// Callers must hold baiduRefreshMu.
func baiduPruneRefreshEntries(now time.Time) {
	for key, entry := range baiduRefreshEntries {
		select {
		case <-entry.done:
			if !now.Before(entry.expires) {
				delete(baiduRefreshEntries, key)
			}
		default:
			// In-flight refreshes are never pruned; their waiters need them.
		}
	}
}

// baiduRefreshWithSingleFlight serialises refreshes per old refresh token.
// Baidu invalidates a refresh token as soon as it is used, so two concurrent
// refreshes would otherwise race and one of them would break the session.
// A successful result is reused for baiduRefreshCacheTTL.
func baiduRefreshWithSingleFlight(ctx context.Context, refreshToken string) (baiduToken, error) {
	key := baiduTokenCacheKey(refreshToken)

	baiduRefreshMu.Lock()
	baiduPruneRefreshEntries(time.Now())
	if entry, ok := baiduRefreshEntries[key]; ok {
		select {
		case <-entry.done:
			// A completed refresh stays valid for a short window so that a
			// burst of parallel requests reuses one rotation.
			if time.Now().Before(entry.expires) {
				baiduRefreshMu.Unlock()
				return entry.token, entry.err
			}
			delete(baiduRefreshEntries, key)
		default:
			// The refresh is still in flight: wait for that exact result instead
			// of spending the one-time refresh token a second time.
			baiduRefreshMu.Unlock()
			select {
			case <-entry.done:
			case <-ctx.Done():
				return baiduToken{}, ctx.Err()
			}
			return entry.token, entry.err
		}
	}
	entry := &baiduRefreshEntry{done: make(chan struct{})}
	baiduRefreshEntries[key] = entry
	baiduRefreshMu.Unlock()

	token, err := baiduRefreshToken(ctx, refreshToken)

	baiduRefreshMu.Lock()
	entry.token, entry.err = token, err
	if err == nil {
		entry.expires = time.Now().Add(baiduRefreshCacheTTL)
	} else {
		delete(baiduRefreshEntries, key)
	}
	close(entry.done)
	baiduRefreshMu.Unlock()

	return token, err
}

func baiduTokenCacheKey(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

// baiduRefreshToken exchanges a rotating refresh token for a new token pair.
func baiduRefreshToken(ctx context.Context, refreshToken string) (baiduToken, error) {
	query := url.Values{}
	query.Set("grant_type", "refresh_token")
	query.Set("refresh_token", refreshToken)
	query.Set("client_id", baiduCfg.appKey)
	query.Set("client_secret", baiduCfg.secretKey)
	return baiduFetchToken(ctx, query)
}

// baiduExchangeCode turns the one-time OAuth code into the first token pair.
func baiduExchangeCode(ctx context.Context, code string) (baiduToken, error) {
	query := url.Values{}
	query.Set("grant_type", "authorization_code")
	query.Set("code", code)
	query.Set("client_id", baiduCfg.appKey)
	query.Set("client_secret", baiduCfg.secretKey)
	query.Set("redirect_uri", baiduCfg.redirectURI)
	return baiduFetchToken(ctx, query)
}

func baiduFetchToken(ctx context.Context, query url.Values) (baiduToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baiduTokenEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		return baiduToken{}, errBaiduUpstream
	}
	resp, err := baiduUpstreamClient.Do(req)
	if err != nil {
		return baiduToken{}, errBaiduUpstream
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return baiduToken{}, errBaiduUpstream
	}
	var token baiduToken
	if err := json.Unmarshal(body, &token); err != nil {
		return baiduToken{}, errBaiduUpstream
	}
	if resp.StatusCode != http.StatusOK || token.Error != "" || token.AccessToken == "" || token.RefreshToken == "" {
		return baiduToken{}, errBaiduTokenExchange
	}
	return token, nil
}

// baiduAccessTokenFromRequest returns a token that is valid for at least the
// next minute, refreshing and re-issuing cookies when needed.
func baiduAccessTokenFromRequest(w http.ResponseWriter, r *http.Request) (string, error) {
	access := baiduCookieValue(r, baiduAccessCookie)
	expiresAt, _ := strconv.ParseInt(baiduCookieValue(r, baiduExpiryCookie), 10, 64)
	if access != "" && expiresAt > time.Now().Add(baiduRefreshLeadTime).UnixMilli() {
		return access, nil
	}

	refresh := baiduCookieValue(r, baiduRefreshCookie)
	if refresh == "" {
		return "", errBaiduReauth
	}
	// Cookies are rewritten even when the refresh came from the cache, because
	// Baidu already rotated the token in the request that filled the cache.
	token, err := baiduRefreshWithSingleFlight(r.Context(), refresh)
	if err != nil {
		return "", errBaiduReauth
	}
	baiduWriteTokenCookies(w, token)
	return token.AccessToken, nil
}

// baiduForceRefresh refreshes regardless of the cached access token, used when
// Baidu reports 111/-6 mid-flight.
func baiduForceRefresh(w http.ResponseWriter, r *http.Request) (baiduToken, error) {
	refresh := baiduCookieValue(r, baiduRefreshCookie)
	if refresh == "" {
		return baiduToken{}, errBaiduReauth
	}
	// Two requests can observe 111/-6 at the same time. The forced path must use
	// the same single-flight as the normal path, otherwise both would spend the
	// same one-time refresh token and the loser would be logged out.
	token, err := baiduRefreshWithSingleFlight(r.Context(), refresh)
	if err != nil {
		return baiduToken{}, errBaiduReauth
	}
	baiduWriteTokenCookies(w, token)
	return token, nil
}

// baiduWithToken runs fn with a valid token and retries once after a forced
// refresh when Baidu invalidates the token mid-flight.
func baiduWithToken[T any](w http.ResponseWriter, r *http.Request, fn func(context.Context, string) (T, error)) (T, string, error) {
	var zero T
	if !baiduCfg.enabled() {
		return zero, "", errBaiduDisabled
	}
	token, err := baiduAccessTokenFromRequest(w, r)
	if err != nil {
		return zero, "", err
	}

	out, err := fn(r.Context(), token)
	if err == nil || !baiduIsAuthError(err) {
		return out, token, err
	}

	refreshed, refreshErr := baiduForceRefresh(w, r)
	if refreshErr != nil {
		return zero, "", errBaiduReauth
	}
	out, err = fn(r.Context(), refreshed.AccessToken)
	return out, refreshed.AccessToken, err
}

// ── Baidu API ────────────────────────────────────────────────────────────────

// baiduFileEntry is one file/list or multimedia/filemetas entry.
// fs_id is uint64, so it is decoded as json.Number and rendered as a string:
// a float64 round-trip would silently address the wrong file.
type baiduFileEntry struct {
	FsID           json.Number `json:"fs_id"`
	Path           string      `json:"path"`
	ServerFilename string      `json:"server_filename"`
	Filename       string      `json:"filename"`
	Size           json.Number `json:"size"`
	IsDir          int         `json:"isdir"`
	ServerMtime    int64       `json:"server_mtime"`
	DLink          string      `json:"dlink"`
}

type baiduEnvelope struct {
	Errno   int              `json:"errno"`
	ShowMsg string           `json:"show_msg"`
	List    []baiduFileEntry `json:"list"`
}

func baiduGetJSON(ctx context.Context, rawURL string, out *baiduEnvelope) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errBaiduUpstream
	}
	// Baidu rejects xpan requests that do not carry this exact User-Agent.
	req.Header.Set("User-Agent", baiduUserAgent)
	resp, err := baiduUpstreamClient.Do(req)
	if err != nil {
		return errBaiduUpstream
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return errBaiduUpstream
	}
	if err := json.Unmarshal(body, out); err != nil {
		if resp.StatusCode >= http.StatusBadRequest {
			return baiduErrnoError(-1, fmt.Sprintf("upstream HTTP %d", resp.StatusCode))
		}
		return errBaiduUpstream
	}
	return nil
}

func baiduFetchPage(ctx context.Context, token, dir string, start int) ([]baiduFileEntry, error) {
	query := url.Values{}
	query.Set("method", "list")
	query.Set("access_token", token)
	query.Set("dir", dir)
	query.Set("start", strconv.Itoa(start))
	query.Set("limit", strconv.Itoa(baiduPageLimit))
	query.Set("order", "name")
	if baiduCfg.appID != "" {
		query.Set("app_id", baiduCfg.appID)
	}

	var envelope baiduEnvelope
	if err := baiduGetJSON(ctx, baiduPanEndpoint+"/file?"+query.Encode(), &envelope); err != nil {
		return nil, err
	}
	if envelope.Errno != 0 {
		return nil, baiduErrnoError(envelope.Errno, envelope.ShowMsg)
	}
	return envelope.List, nil
}

// baiduListDirectory lists one directory with at most baiduMaxPages pages.
// Only the app root creates a missing directory, and only once: the relay must
// not write anywhere else in the user's Netdisk.
func baiduListDirectory(ctx context.Context, token, dir, rootDir string) ([]baiduFileEntry, error) {
	var all []baiduFileEntry
	start := 0
	retriedMissing := false

	for page := 0; page < baiduMaxPages; page++ {
		entries, err := baiduFetchPage(ctx, token, dir, start)
		if err != nil {
			var e *baiduErrno
			if dir == rootDir && !retriedMissing && errors.As(err, &e) && e.errno == -9 {
				retriedMissing = true
				if createErr := baiduCreateDirectory(ctx, token, rootDir); createErr != nil {
					return nil, createErr
				}
				continue
			}
			return nil, err
		}
		all = append(all, entries...)
		if len(entries) < baiduPageLimit {
			break
		}
		start += len(entries)
	}
	return all, nil
}

// baiduCreateDirectory creates the app directory. errno -8 means it already
// exists (an earlier or concurrent attempt), which is treated as success.
func baiduCreateDirectory(ctx context.Context, token, dir string) error {
	query := url.Values{}
	query.Set("method", "create")
	query.Set("access_token", token)

	form := url.Values{}
	form.Set("path", dir)
	form.Set("isdir", "1")
	form.Set("rtype", "0")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baiduPanEndpoint+"/file?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return errBaiduUpstream
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", baiduUserAgent)

	resp, err := baiduUpstreamClient.Do(req)
	if err != nil {
		return errBaiduUpstream
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errBaiduUpstream
	}
	var envelope baiduEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errBaiduUpstream
	}
	if envelope.Errno != 0 && envelope.Errno != -8 {
		return baiduErrnoError(envelope.Errno, envelope.ShowMsg)
	}
	return nil
}

func baiduFileMetadata(ctx context.Context, token, fsid string) (baiduFileEntry, error) {
	query := url.Values{}
	query.Set("method", "filemetas")
	query.Set("access_token", token)
	// fs_id must stay a decimal string; the brackets are the API's list syntax.
	query.Set("fsids", "["+fsid+"]")
	query.Set("dlink", "1")

	var envelope baiduEnvelope
	if err := baiduGetJSON(ctx, baiduPanEndpoint+"/multimedia?"+query.Encode(), &envelope); err != nil {
		return baiduFileEntry{}, err
	}
	if envelope.Errno != 0 {
		return baiduFileEntry{}, baiduErrnoError(envelope.Errno, envelope.ShowMsg)
	}
	if len(envelope.List) == 0 {
		return baiduFileEntry{}, baiduErrnoError(-9, "file metadata returned no entry")
	}
	return envelope.List[0], nil
}

// ── Validation ───────────────────────────────────────────────────────────────

// baiduRelativePath accepts only an empty path or a clean absolute path such as
// "/Books/2024". Dot segments, backslashes, control characters and over-long
// input are rejected, so the client cannot escape /apps/{APP_NAME}.
func baiduRelativePath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 1024 || !utf8.ValidString(raw) {
		return "", errBaiduInvalidPath
	}
	if !strings.HasPrefix(raw, "/") {
		return "", errBaiduInvalidPath
	}
	for _, r := range raw {
		if r == '\\' || r < 0x20 || r == 0x7f {
			return "", errBaiduInvalidPath
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(raw, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errBaiduInvalidPath
		}
	}
	return raw, nil
}

func baiduIsDigits(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// baiduBookExtensions mirrors supportedFormats in src/utils/common.ts; this
// server-side list is the authority for downloads.
var baiduBookExtensions = []string{
	".epub", ".pdf", ".txt", ".mobi", ".azw3", ".azw", ".kdz",
	".htm", ".html", ".xml", ".xhtml", ".mhtml",
	".docx", ".doc", ".rtf", ".md", ".fb2", ".chm",
	".cbz", ".cbt", ".cbr", ".cb7",
	".pptx", ".ppt", ".xps", ".oxps", ".djvu", ".djv", ".ofd", ".caj",
}

func baiduIsBookFile(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range baiduBookExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func baiduFileName(f baiduFileEntry) string {
	if f.ServerFilename != "" {
		return f.ServerFilename
	}
	if f.Filename != "" {
		return f.Filename
	}
	if i := strings.LastIndex(f.Path, "/"); i >= 0 {
		return f.Path[i+1:]
	}
	return f.Path
}

func baiduNumberInt64(n json.Number) int64 {
	if v, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return v
	}
	if f, err := n.Float64(); err == nil {
		return int64(f)
	}
	return 0
}

func baiduFormatMtime(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

// baiduDownloadHostAllowed accepts only Baidu-owned hosts, so a tampered dlink
// cannot turn the relay into an SSRF proxy or leak the token to a third party.
func baiduDownloadHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "baidu.com" || host == "baidupcs.com" {
		return true
	}
	return strings.HasSuffix(host, ".baidu.com") || strings.HasSuffix(host, ".baidupcs.com")
}

// baiduValidateDownloadURL enforces https plus the host allowlist.
func baiduValidateDownloadURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported download scheme")
	}
	if u.Scheme != "https" && !baiduAllowInsecureDownload {
		return fmt.Errorf("download URL must use https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !baiduHostAllowed(host) {
		return fmt.Errorf("download host is not allowed")
	}
	return nil
}

// baiduCheckDownloadRedirect validates every hop and re-applies the Baidu
// User-Agent, because Go copies headers across redirects but not reliably.
func baiduCheckDownloadRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= baiduMaxRedirects {
		return fmt.Errorf("too many redirects")
	}
	if err := baiduValidateDownloadURL(req.URL); err != nil {
		return err
	}
	req.Header.Set("User-Agent", baiduUserAgent)
	return nil
}

// baiduSanitizeFilename keeps the last path element and strips characters that
// would break Content-Disposition.
func baiduSanitizeFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '\\' || r == '/' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// baiduSameOrigin rejects cross-site browser requests. The relay is reachable
// without Basic Auth, so this is the CSRF boundary for /baidu-api.
func baiduSameOrigin(r *http.Request) bool {
	origin := normalizeOrigin(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if serverOrigin := normalizeOrigin(getServerOrigin(r)); serverOrigin != "" && origin == serverOrigin {
		return true
	}
	// In development the Vite proxy keeps Host but may not forward the scheme;
	// accept both schemes for the very same host.
	host := strings.ToLower(r.Host)
	if host == "" {
		return false
	}
	return origin == "http://"+host || origin == "https://"+host
}

// baiduAPIHandler dispatches /baidu-api/* before CORS and Basic Auth in handler().
func baiduAPIHandler(w http.ResponseWriter, r *http.Request) {
	if !baiduSameOrigin(r) {
		baiduWriteError(w, http.StatusForbidden, "forbidden", 0, "cross-origin request rejected")
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch route := strings.TrimPrefix(r.URL.Path, baiduAPIPrefix); {
	case route == "/status" && r.Method == http.MethodGet:
		baiduHandleStatus(w, r)
	case route == "/auth" && r.Method == http.MethodGet:
		baiduHandleAuth(w, r)
	case route == "/callback" && r.Method == http.MethodGet:
		baiduHandleCallback(w, r)
	case route == "/list" && r.Method == http.MethodGet:
		baiduHandleList(w, r)
	case route == "/download" && r.Method == http.MethodGet:
		baiduHandleDownload(w, r)
	case route == "/logout" && r.Method == http.MethodPost:
		baiduHandleLogout(w, r)
	default:
		writePlain(w, http.StatusNotFound, "Not Found")
	}
}

func baiduHandleStatus(w http.ResponseWriter, r *http.Request) {
	if !baiduCfg.enabled() {
		// Always 200 when the service is reachable: the dialog uses this to
		// decide whether to render the entry at all.
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"authorized": baiduCookieValue(r, baiduRefreshCookie) != "",
		"maxFileMB":  baiduCfg.maxFileBytes >> 20,
	})
}

func baiduHandleAuth(w http.ResponseWriter, r *http.Request) {
	if !baiduCfg.enabled() {
		baiduWriteRelayError(w, errBaiduDisabled)
		return
	}
	state, err := baiduRandomState()
	if err != nil {
		baiduWriteError(w, http.StatusInternalServerError, "upstream", 0, "could not start authorization")
		return
	}
	// The one-time state cookie binds the callback to this browser; it expires
	// after ten minutes and is deleted as soon as the callback is handled.
	baiduSetCookie(w, baiduStateCookie, state, baiduStateMaxAge)

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", baiduCfg.appKey)
	query.Set("redirect_uri", baiduCfg.redirectURI)
	query.Set("scope", "basic,netdisk")
	query.Set("state", state)
	http.Redirect(w, r, "https://openapi.baidu.com/oauth/2.0/authorize?"+query.Encode(), http.StatusFound)
}

func baiduRandomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func baiduHandleCallback(w http.ResponseWriter, r *http.Request) {
	if !baiduCfg.enabled() {
		baiduFailPage(w, "Baidu Netdisk is not configured on this server")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	cookieState := baiduCookieValue(r, baiduStateCookie)
	baiduDeleteCookie(w, baiduStateCookie)

	// Constant-time comparison: a callback carrying a foreign or replayed state
	// must never be able to install tokens for another browser.
	if code == "" || state == "" || cookieState == "" ||
		subtle.ConstantTimeCompare([]byte(cookieState), []byte(state)) != 1 {
		baiduFailPage(w, "Baidu Netdisk authorization failed")
		return
	}

	token, err := baiduExchangeCode(r.Context(), code)
	if err != nil {
		baiduFailPage(w, "Baidu Netdisk authorization failed")
		return
	}
	baiduWriteTokenCookies(w, token)
	baiduSuccessPage(w)
}

func baiduSuccessPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// The status-only message is delivered to the exact origin of this page;
	// the opener still verifies event.origin before trusting it.
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Baidu Netdisk</title></head>
<body>
<p id="done">Baidu Netdisk connected. You can close this window.</p>
<p id="fallback" hidden><a href="/">Return to Koodo Reader</a></p>
<script>
(function () {
  try {
    if (window.opener) {
      window.opener.postMessage({ type: "koodo-baidu-oauth", ok: true }, window.location.origin);
      setTimeout(function () { window.close(); }, 3000);
      return;
    }
  } catch (e) {}
  document.getElementById("fallback").hidden = false;
})();
</script>
</body></html>`)
}

func baiduFailPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusBadRequest)
	// Generic text only: the upstream response body is never reflected here.
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Baidu Netdisk</title></head>
<body><p>`+message+`</p><p><a href="/">Return to Koodo Reader</a></p></body></html>`)
}

func baiduHandleList(w http.ResponseWriter, r *http.Request) {
	if !baiduCfg.enabled() {
		baiduWriteRelayError(w, errBaiduDisabled)
		return
	}
	rel, err := baiduRelativePath(r.URL.Query().Get("path"))
	if err != nil {
		baiduWriteRelayError(w, err)
		return
	}
	dir := baiduCfg.appDirectory() + rel

	entries, _, err := baiduWithToken(w, r, func(ctx context.Context, token string) ([]baiduFileEntry, error) {
		return baiduListDirectory(ctx, token, dir, baiduCfg.appDirectory())
	})
	if err != nil {
		baiduWriteRelayError(w, err)
		return
	}

	type listItem struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Type     string `json:"type"`
		Modified string `json:"modified"`
		Path     string `json:"path"`
		Fsid     string `json:"fsid"`
	}
	items := make([]listItem, 0, len(entries))
	for _, entry := range entries {
		name := baiduFileName(entry)
		if name == "" {
			continue
		}
		item := listItem{
			Name:     name,
			Type:     "file",
			Modified: baiduFormatMtime(entry.ServerMtime),
			Path:     rel + "/" + name,
			Fsid:     entry.FsID.String(),
		}
		if entry.IsDir != 0 {
			item.Type = "folder"
		} else {
			number := baiduNumberInt64(entry.Size)
			if number > 0 {
				item.Size = number
			}
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"files": items})
}

func baiduHandleDownload(w http.ResponseWriter, r *http.Request) {
	if !baiduCfg.enabled() {
		baiduWriteRelayError(w, errBaiduDisabled)
		return
	}
	fsid := strings.TrimSpace(r.URL.Query().Get("fsid"))
	if !baiduIsDigits(fsid) {
		baiduWriteRelayError(w, errBaiduInvalidID)
		return
	}

	entry, token, err := baiduWithToken(w, r, func(ctx context.Context, token string) (baiduFileEntry, error) {
		return baiduFileMetadata(ctx, token, fsid)
	})
	if err != nil {
		baiduWriteRelayError(w, err)
		return
	}

	// Re-validate everything on the server: the dialog's checks are convenience
	// only, and fsid comes straight from a user-controlled query string.
	name := baiduFileName(entry)
	if entry.IsDir != 0 {
		baiduWriteRelayError(w, errBaiduInvalidID)
		return
	}
	if !baiduIsBookFile(name) {
		baiduWriteRelayError(w, errBaiduInvalidID)
		return
	}
	if !strings.HasPrefix(entry.Path, baiduCfg.appDirectory()+"/") {
		baiduWriteError(w, http.StatusForbidden, "forbidden", 0, "file is outside the application directory")
		return
	}
	size := baiduNumberInt64(entry.Size)
	if size <= 0 || size > baiduCfg.maxFileBytes {
		baiduWriteError(w, http.StatusRequestEntityTooLarge, "too_large", 0, "file exceeds the configured size limit")
		return
	}
	if entry.DLink == "" {
		baiduWriteRelayError(w, errBaiduUpstream)
		return
	}

	downloadURL, err := url.Parse(entry.DLink)
	if err != nil {
		baiduWriteRelayError(w, errBaiduUpstream)
		return
	}
	if err := baiduValidateDownloadURL(downloadURL); err != nil {
		baiduWriteError(w, http.StatusForbidden, "forbidden", 0, "download link rejected")
		return
	}

	query := downloadURL.Query()
	query.Set("access_token", token)
	downloadURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		baiduWriteRelayError(w, errBaiduUpstream)
		return
	}
	req.Header.Set("User-Agent", baiduUserAgent)

	resp, err := baiduDownloadClient.Do(req)
	if err != nil {
		baiduWriteRelayError(w, errBaiduUpstream)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		baiduWriteRelayError(w, errBaiduUpstream)
		return
	}
	if resp.ContentLength > baiduCfg.maxFileBytes {
		baiduWriteError(w, http.StatusRequestEntityTooLarge, "too_large", 0, "file exceeds the configured size limit")
		return
	}

	safeName := baiduSanitizeFilename(name)
	if safeName == "" {
		safeName = "book"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(safeName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")

	// The global server WriteTimeout is five minutes, which would cut a large
	// book download; extend the deadline for this single streaming response.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(baiduWriteTimeout))

	written := int64(0)
	buf := make([]byte, baiduDownloadChunkSize)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			written += int64(n)
			// Content-Length can lie; stop the transfer as soon as the real byte
			// count exceeds the limit instead of exhausting disk/network.
			if written > baiduCfg.maxFileBytes {
				return
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return
		}
	}
}

func baiduHandleLogout(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{baiduAccessCookie, baiduExpiryCookie, baiduRefreshCookie, baiduStateCookie} {
		baiduDeleteCookie(w, name)
	}
	w.WriteHeader(http.StatusNoContent)
}

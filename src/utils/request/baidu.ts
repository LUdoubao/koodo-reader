/**
 * Client for the same-origin `/baidu-api` relay.
 *
 * Tokens never reach this layer: the Go service keeps them in HttpOnly cookies
 * scoped to `/baidu-api`, so every request here only needs
 * `credentials: "include"`. Download bytes are streamed from the relay because
 * a browser cannot set the `User-Agent: pan.baidu.com` header Baidu requires.
 */

/** Default relay prefix; production serves it from the same origin via Caddy. */
export const BAIDU_DEFAULT_API_BASE = "/baidu-api";

/** Public relay status. `enabled` is false when the server lacks BAIDU_* config. */
export interface BaiduStatus {
  enabled: boolean;
  authorized: boolean;
  maxFileMB: number;
}

/** One directory entry returned by `GET /baidu-api/list`. */
export interface BaiduFileInfo {
  name: string;
  size: number;
  type: "file" | "folder";
  modified: string;
  path: string;
  fsid: string;
}

export type BaiduErrorCode =
  | "reauth_required"
  | "forbidden"
  | "not_found"
  | "too_large"
  | "upstream"
  | "invalid_id";

const BAIDU_ERROR_CODES: BaiduErrorCode[] = [
  "reauth_required",
  "forbidden",
  "not_found",
  "too_large",
  "upstream",
  "invalid_id",
];

/** Error raised by the relay, carrying the public error code and HTTP status. */
export class BaiduApiError extends Error {
  code: BaiduErrorCode;
  status: number;
  errno?: number;

  constructor(
    code: BaiduErrorCode,
    status: number,
    message: string,
    errno?: number
  ) {
    super(message);
    this.name = "BaiduApiError";
    this.code = code;
    this.status = status;
    this.errno = errno;
  }
}

// ── Pure helpers (unit-tested) ───────────────────────────────────────────────

/**
 * Resolves the relay base to a same-origin path.
 *
 * `VITE_BAIDU_API_BASE` may be a path or an absolute URL, but only the current
 * origin is accepted: cookies are SameSite=Lax and the relay rejects
 * cross-origin requests, so a foreign base would be unusable anyway.
 *
 * @param raw configured value
 * @param currentOrigin origin of the page, used to validate absolute URLs
 * @returns a path such as `/baidu-api`
 */
export function normalizeBaiduApiBase(
  raw?: string,
  currentOrigin?: string
): string {
  const fallback = BAIDU_DEFAULT_API_BASE;
  const value = (raw || "").trim();
  if (!value) {
    return fallback;
  }
  if (value.startsWith("/")) {
    return value.replace(/\/+$/, "") || fallback;
  }
  try {
    const url = new URL(value);
    const origin =
      currentOrigin ||
      (typeof window !== "undefined" ? window.location.origin : "");
    if (!origin || url.origin !== origin) {
      return fallback;
    }
    return url.pathname.replace(/\/+$/, "") || fallback;
  } catch {
    return fallback;
  }
}

/**
 * Builds a relay URL and drops empty parameters so `?path=` never becomes a
 * stray `?path=undefined`.
 *
 * @param base relay base path, e.g. `/baidu-api`
 * @param endpoint endpoint name without a leading slash
 * @param params optional query values
 */
export function buildBaiduApiUrl(
  base: string,
  endpoint: string,
  params?: Record<string, string | number | undefined>
): string {
  const query = new URLSearchParams();
  Object.entries(params || {}).forEach(([key, value]) => {
    if (value === undefined || value === null || value === "") {
      return;
    }
    query.set(key, String(value));
  });
  const suffix = query.toString();
  return `${base}/${endpoint}${suffix ? `?${suffix}` : ""}`;
}

/**
 * Maps a relay error response to a typed error. Unknown payloads fall back to
 * the HTTP status so a proxy-generated error page still yields a sane code.
 *
 * @param status HTTP status
 * @param payload parsed JSON body, when available
 */
export function mapBaiduError(status: number, payload: unknown): BaiduApiError {
  const body = (payload || {}) as {
    error?: string;
    message?: string;
    errno?: number;
  };
  let code: BaiduErrorCode;
  if (BAIDU_ERROR_CODES.includes(body.error as BaiduErrorCode)) {
    code = body.error as BaiduErrorCode;
  } else if (status === 401) {
    code = "reauth_required";
  } else if (status === 403) {
    code = "forbidden";
  } else if (status === 404) {
    code = "not_found";
  } else if (status === 413) {
    code = "too_large";
  } else if (status === 400) {
    code = "invalid_id";
  } else {
    code = "upstream";
  }
  const message =
    typeof body.message === "string" && body.message
      ? body.message
      : `Baidu Netdisk request failed (${code})`;
  return new BaiduApiError(
    code,
    status,
    message,
    typeof body.errno === "number" ? body.errno : undefined
  );
}

/**
 * Whether a name has one of the allowed book formats. The caller passes
 * `supportedFormats` from `utils/common` so the list has a single source of
 * truth; the Go relay re-checks the same suffixes as the final authority.
 *
 * @param name file name from the relay
 * @param formats allowed extensions such as `[".epub", ".pdf"]`
 */
export function isSupportedBookFile(
  name: string,
  formats: readonly string[]
): boolean {
  const lower = (name || "").toLowerCase();
  const dot = lower.lastIndexOf(".");
  if (dot < 0) {
    return false;
  }
  return formats.includes(lower.slice(dot));
}

/** Keeps the last path segment and removes characters invalid in a file name. */
export function sanitizeBaiduFileName(name: string): string {
  const base = (name || "").split(/[/\\]/).pop() || "";
  const cleaned = base
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .replace(/["'<>|:*?]/g, "_")
    .trim();
  return cleaned || "book";
}

/**
 * Validates the `postMessage` contract used by the OAuth callback page.
 * Returns `null` for unrelated messages so callers can ignore them.
 *
 * @param data event payload
 * @param expectedOrigin origin of this page; the callback posts to it
 */
export function parseBaiduOAuthMessage(
  data: unknown,
  expectedOrigin: string
): "ok" | "error" | null {
  if (!expectedOrigin || !data || typeof data !== "object") {
    return null;
  }
  const message = data as { type?: unknown; ok?: unknown };
  if (message.type !== "koodo-baidu-oauth") {
    return null;
  }
  if (message.ok === true) {
    return "ok";
  }
  if (message.ok === false) {
    return "error";
  }
  return null;
}

/** Download progress as an integer percentage; 0 when the size is unknown. */
export function baiduProgressPercent(loaded: number, total: number): number {
  if (!Number.isFinite(total) || total <= 0) {
    return 0;
  }
  const percent = Math.floor((loaded / total) * 100);
  if (percent < 0) {
    return 0;
  }
  return percent > 100 ? 100 : percent;
}

// ── Relay calls ──────────────────────────────────────────────────────────────

function resolveBaiduApiBase(): string {
  const env = (
    import.meta as unknown as { env?: Record<string, string | undefined> }
  ).env;
  const configured = env?.VITE_BAIDU_API_BASE;
  return normalizeBaiduApiBase(
    configured,
    typeof window !== "undefined" ? window.location.origin : ""
  );
}

async function readBaiduError(response: Response): Promise<BaiduApiError> {
  let payload: unknown = null;
  try {
    payload = await response.json();
  } catch {
    payload = null;
  }
  return mapBaiduError(response.status, payload);
}

function normalizeBaiduFile(raw: unknown): BaiduFileInfo | null {
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const entry = raw as Partial<BaiduFileInfo>;
  if (typeof entry.name !== "string" || !entry.name) {
    return null;
  }
  return {
    name: entry.name,
    size: typeof entry.size === "number" && entry.size > 0 ? entry.size : 0,
    type: entry.type === "folder" ? "folder" : "file",
    modified: typeof entry.modified === "string" ? entry.modified : "",
    path: typeof entry.path === "string" ? entry.path : "",
    fsid: entry.fsid === undefined || entry.fsid === null ? "" : String(entry.fsid),
  };
}

/**
 * Probes the relay. Any failure (service down, route missing, non-JSON) hides
 * the entry instead of surfacing an error, so the dialog keeps working on
 * deployments without the relay.
 */
export async function getBaiduStatus(): Promise<BaiduStatus> {
  try {
    const response = await fetch(buildBaiduApiUrl(resolveBaiduApiBase(), "status"), {
      credentials: "include",
      cache: "no-store",
    });
    if (!response.ok) {
      return { enabled: false, authorized: false, maxFileMB: 0 };
    }
    const payload = (await response.json()) as Partial<BaiduStatus>;
    return {
      enabled: payload?.enabled === true,
      authorized: payload?.authorized === true,
      maxFileMB: Number(payload?.maxFileMB) || 0,
    };
  } catch {
    return { enabled: false, authorized: false, maxFileMB: 0 };
  }
}

/**
 * Lists one folder inside `/apps/{BAIDU_APP_NAME}`.
 *
 * @param path relative path, `""` for the app root
 * @throws BaiduApiError with `reauth_required` when the session expired
 */
export async function listBaiduFiles(path: string): Promise<BaiduFileInfo[]> {
  const response = await fetch(
    buildBaiduApiUrl(resolveBaiduApiBase(), "list", { path }),
    { credentials: "include", cache: "no-store" }
  );
  if (!response.ok) {
    throw await readBaiduError(response);
  }
  const payload = (await response.json()) as { files?: unknown };
  const files = Array.isArray(payload?.files) ? payload.files : [];
  return files
    .map((item) => normalizeBaiduFile(item))
    .filter((item): item is BaiduFileInfo => item !== null);
}

/**
 * Downloads one book through the relay and returns it as a `File` ready for
 * `importBookFunc`.
 *
 * Progress is throttled to one update per 500ms so a fast download does not
 * flood react-hot-toast with state updates.
 *
 * @throws BaiduApiError with `reauth_required`, `too_large` or `upstream`
 */
export async function downloadBaiduBook(options: {
  fsid: string;
  name: string;
  size?: number;
  onProgress?: (percent: number) => void;
}): Promise<File> {
  const response = await fetch(
    buildBaiduApiUrl(resolveBaiduApiBase(), "download", {
      fsid: options.fsid,
    }),
    { credentials: "include", cache: "no-store" }
  );
  if (!response.ok) {
    throw await readBaiduError(response);
  }
  if (!response.body) {
    throw new BaiduApiError("upstream", response.status, "empty download");
  }

  const total =
    Number(response.headers.get("Content-Length")) || options.size || 0;
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let loaded = 0;
  let lastReport = 0;

  while (true) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    if (!value) {
      continue;
    }
    chunks.push(value);
    loaded += value.byteLength;
    const now = Date.now();
    if (options.onProgress && (now - lastReport >= 500 || loaded === total)) {
      lastReport = now;
      options.onProgress(baiduProgressPercent(loaded, total));
    }
  }

  const blob = new Blob(chunks as unknown as BlobPart[], {
    type: "application/octet-stream",
  });
  return new File([blob], sanitizeBaiduFileName(options.name));
}

/** Clears the relay cookies, forcing a new authorization on the next click. */
export async function disconnectBaidu(): Promise<void> {
  const response = await fetch(
    buildBaiduApiUrl(resolveBaiduApiBase(), "logout"),
    { method: "POST", credentials: "include" }
  );
  if (!response.ok) {
    throw await readBaiduError(response);
  }
}

/**
 * Opens the authorization popup and resolves once the callback page posts the
 * success message for this exact origin.
 *
 * A 401 from a later call means the session expired; the caller should reset
 * its authorized flag and ask the user to authorize again.
 *
 * @throws BaiduApiError when the popup is closed, times out or reports failure
 */
export function startBaiduAuth(timeoutMs = 10 * 60 * 1000): Promise<void> {
  const url = buildBaiduApiUrl(resolveBaiduApiBase(), "auth");
  return new Promise((resolve, reject) => {
    const popup = window.open(
      url,
      "koodo-baidu-oauth",
      "width=520,height=680"
    );
    if (!popup) {
      // Popup blocked: continue in the current page. The callback page then
      // shows a "return to Koodo Reader" link and this promise never settles
      // because the page navigates away.
      window.location.assign(url);
      return;
    }

    let settled = false;
    const cleanup = () => {
      window.removeEventListener("message", handleMessage);
      window.clearInterval(pollTimer);
      window.clearTimeout(timeoutTimer);
    };
    const finish = (error?: BaiduApiError) => {
      if (settled) {
        return;
      }
      settled = true;
      cleanup();
      if (error) {
        reject(error);
      } else {
        resolve();
      }
    };

    const handleMessage = (event: MessageEvent) => {
      // Never trust another origin, even if it echoes our message shape.
      if (event.origin !== window.location.origin) {
        return;
      }
      const result = parseBaiduOAuthMessage(event.data, event.origin);
      if (!result) {
        return;
      }
      if (result === "ok") {
        try {
          popup.close();
        } catch {
          // Closing an already-closed popup is harmless.
        }
        finish();
        return;
      }
      finish(
        new BaiduApiError(
          "reauth_required",
          401,
          "Baidu Netdisk authorization failed"
        )
      );
    };

    const pollTimer = window.setInterval(() => {
      if (popup.closed && !settled) {
        finish(
          new BaiduApiError(
            "reauth_required",
            401,
            "Baidu Netdisk authorization window was closed"
          )
        );
      }
    }, 500);
    const timeoutTimer = window.setTimeout(() => {
      finish(
        new BaiduApiError(
          "reauth_required",
          401,
          "Baidu Netdisk authorization timed out"
        )
      );
    }, timeoutMs);

    window.addEventListener("message", handleMessage);
  });
}

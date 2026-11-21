import { describe, expect, it } from "vitest";
import {
  BaiduApiError,
  baiduProgressPercent,
  buildBaiduApiUrl,
  isSupportedBookFile,
  mapBaiduError,
  normalizeBaiduApiBase,
  parseBaiduOAuthMessage,
  sanitizeBaiduFileName,
} from "./baidu";

const FORMATS = [".epub", ".pdf", ".txt", ".cbz"];

describe("normalizeBaiduApiBase", () => {
  it.each([
    [undefined, "https://reader.example.com", "/baidu-api"],
    ["", "https://reader.example.com", "/baidu-api"],
    ["/custom-api", "https://reader.example.com", "/custom-api"],
    ["/custom-api/", "https://reader.example.com", "/custom-api"],
    [
      "https://reader.example.com/relay/",
      "https://reader.example.com",
      "/relay",
    ],
    ["https://evil.example.com/relay", "https://reader.example.com", "/baidu-api"],
    [
      "https://reader.example.com:8443/relay",
      "https://reader.example.com",
      "/baidu-api",
    ],
    ["not a url", "https://reader.example.com", "/baidu-api"],
  ])("maps %s on %s to %s", (raw, origin, expected) => {
    expect(normalizeBaiduApiBase(raw, origin)).toBe(expected);
  });

  it("falls back to the default when the origin cannot be determined", () => {
    expect(normalizeBaiduApiBase("https://reader.example.com/relay", "")).toBe(
      "/baidu-api"
    );
  });
});

describe("buildBaiduApiUrl", () => {
  it("appends no query string when there are no parameters", () => {
    expect(buildBaiduApiUrl("/baidu-api", "status")).toBe(
      "/baidu-api/status"
    );
  });

  it("drops empty values so path is never sent as undefined", () => {
    expect(buildBaiduApiUrl("/baidu-api", "list", { path: "" })).toBe(
      "/baidu-api/list"
    );
    expect(
      buildBaiduApiUrl("/baidu-api", "list", { path: undefined })
    ).toBe("/baidu-api/list");
  });

  it("encodes the relative folder path", () => {
    expect(buildBaiduApiUrl("/baidu-api", "list", { path: "/Books 2024" })).toBe(
      "/baidu-api/list?path=%2FBooks+2024"
    );
  });

  it("encodes a numeric fsid instead of stringifying an object", () => {
    expect(
      buildBaiduApiUrl("/baidu-api", "download", { fsid: "1234567890" })
    ).toBe("/baidu-api/download?fsid=1234567890");
  });
});

describe("mapBaiduError", () => {
  it("prefers an explicit public error code", () => {
    const error = mapBaiduError(502, {
      error: "reauth_required",
      message: "please authorize again",
      errno: 111,
    });
    expect(error).toBeInstanceOf(BaiduApiError);
    expect(error.code).toBe("reauth_required");
    expect(error.status).toBe(502);
    expect(error.errno).toBe(111);
    expect(error.message).toBe("please authorize again");
  });

  it.each([
    [401, "reauth_required"],
    [403, "forbidden"],
    [404, "not_found"],
    [413, "too_large"],
    [400, "invalid_id"],
    [500, "upstream"],
    [302, "upstream"],
  ])("maps HTTP %s to %s", (status, expected) => {
    expect(mapBaiduError(status, null).code).toBe(expected);
  });

  it("ignores unknown error codes and uses the status", () => {
    const error = mapBaiduError(404, { error: "something_else" });
    expect(error.code).toBe("not_found");
  });

  it("always provides a readable message", () => {
    expect(mapBaiduError(502, {}).message).toContain("upstream");
  });
});

describe("isSupportedBookFile", () => {
  it.each([
    ["Book.EPUB", true],
    ["paper.pdf", true],
    ["notes.txt", true],
    ["scan.cbz", true],
    ["scan.cbz.tmp", false],
    ["installer.exe", false],
    ["README", false],
    ["", false],
  ])("classifies %s as %s", (name, expected) => {
    expect(isSupportedBookFile(name, FORMATS)).toBe(expected);
  });
});

describe("sanitizeBaiduFileName", () => {
  it.each([
    ["../../etc/passwd", "passwd"],
    ["C:\\books\\novel.epub", "novel.epub"],
    ['bad"name.epub', "bad_name.epub"],
    ["a:b*c?.epub", "a_b_c_.epub"],
    ["tab\tname.epub", "tabname.epub"],
    ["", "book"],
    ["   ", "book"],
  ])("cleans %j to %j", (input, expected) => {
    expect(sanitizeBaiduFileName(input)).toBe(expected);
  });
});

describe("parseBaiduOAuthMessage", () => {
  it("accepts the success contract from the expected origin", () => {
    expect(
      parseBaiduOAuthMessage(
        { type: "koodo-baidu-oauth", ok: true },
        "https://reader.example.com"
      )
    ).toBe("ok");
  });

  it("reports a failure message", () => {
    expect(
      parseBaiduOAuthMessage(
        { type: "koodo-baidu-oauth", ok: false },
        "https://reader.example.com"
      )
    ).toBe("error");
  });

  it.each([
    [null, "https://reader.example.com"],
    ["koodo-baidu-oauth", "https://reader.example.com"],
    [{ type: "other", ok: true }, "https://reader.example.com"],
    [{ type: "koodo-baidu-oauth", ok: true }, ""],
    [{ type: "koodo-baidu-oauth", ok: "yes" }, "https://reader.example.com"],
  ])("ignores %j for origin %j", (data, origin) => {
    expect(parseBaiduOAuthMessage(data, origin)).toBeNull();
  });
});

describe("baiduProgressPercent", () => {
  it.each([
    [0, 0, 0],
    [0, 100, 0],
    [50, 100, 50],
    [99, 100, 99],
    [150, 100, 100],
    [5, Number.NaN, 0],
    [-5, 100, 0],
  ])("reports %s of %s as %s%%", (loaded, total, expected) => {
    expect(baiduProgressPercent(loaded, total)).toBe(expected);
  });
});

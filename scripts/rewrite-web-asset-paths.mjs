/**
 * 构建后资源路径改写脚本（Web 版 /reader 子路径部署专用）。
 *
 * 用途：
 *   阅读引擎在浏览器端把静态资源拼成根绝对路径 `/lib/...`（pdfjs、highlight-js、
 *   排版 CSS、tesseract worker）。Vite 构建基址是相对 `./`，页面一旦部署到
 *   `https://gin.hk.cn/reader/`，根绝对路径就会指向站点根目录，命中宿主的 SPA
 *   回落页（HTML），导致 PDF 文本层 / CMaps、代码高亮、EPUB 主题与 OCR 失效。
 *
 * 输入：
 *   `build/static/kookit-*.js`（引擎单独 chunk）。只处理该 chunk，不触碰其他产物。
 *
 * 输出：
 *   原地把 `${isElectron()?".":""}/lib/` 形态的表达式替换为相对字面量 `./lib/`。
 *   替换后 Web 端解析为 `/reader/lib/...`，Electron 端 `file://` 下与原值等价。
 *
 * 断言与幂等：
 *   - 首次执行必须命中 3 处且替换后无残留，否则以非 0 退出码失败，避免静默半修复。
 *   - 已改写过的产物再次执行时命中 0 处且已存在相对路径，直接跳过并以 0 退出。
 *
 * 用法：`yarn build:web`（vite build 之后自动调用）。
 */

import { readFileSync, writeFileSync, readdirSync, existsSync } from "node:fs";
import { join } from "node:path";

/** 引擎中根绝对路径表达式的正则：匹配任意被压缩重命名的判定函数 */
const ABSOLUTE_LIB_PATTERN =
  /\$\{[A-Za-z_$][\w$]*\(\)\?"\.":""\}\/lib\//g;

/** 改写后的相对字面量 */
const RELATIVE_LIB_PATH = "./lib/";

/** 期望命中次数 */
const EXPECTED_MATCHES = 3;

const STATIC_DIR = join("build", "static");

/**
 * 定位引擎 chunk。
 *
 * @returns {string[]} `build/static` 下的 kookit-*.js 绝对/相对路径列表
 */
function findKookitChunks() {
  if (!existsSync(STATIC_DIR)) {
    console.error(`[rewrite-web-asset-paths] 找不到构建目录：${STATIC_DIR}`);
    process.exit(1);
  }
  return readdirSync(STATIC_DIR)
    .filter((name) => /^kookit.*\.js$/.test(name))
    .map((name) => join(STATIC_DIR, name));
}

/**
 * 对单个 chunk 执行限定替换。
 *
 * @param {string} file 引擎 chunk 路径
 * @returns {{ file: string, matches: number, skipped: boolean }}
 */
function rewriteChunk(file) {
  const source = readFileSync(file, "utf8");
  const matches = source.match(ABSOLUTE_LIB_PATTERN)?.length ?? 0;

  if (matches === 0) {
    const alreadyRelative = source.split(RELATIVE_LIB_PATH).length - 1;
    if (alreadyRelative > 0) {
      console.log(
        `[rewrite-web-asset-paths] ${file} 已是相对路径（${alreadyRelative} 处 ./lib/），跳过`
      );
      return { file, matches: 0, skipped: true };
    }
    console.error(
      `[rewrite-web-asset-paths] ${file} 未命中任何根绝对路径表达式，产物结构可能已变化`
    );
    process.exit(1);
  }

  const rewritten = source.replace(ABSOLUTE_LIB_PATTERN, RELATIVE_LIB_PATH);
  writeFileSync(file, rewritten);
  return { file, matches, skipped: false };
}

const chunks = findKookitChunks();
let totalMatches = 0;

for (const chunk of chunks) {
  const result = rewriteChunk(chunk);
  totalMatches += result.matches;
  if (result.matches > 0) {
    // 自断言：替换后不应再有原表达式残留
    const residual =
      readFileSync(chunk, "utf8").match(ABSOLUTE_LIB_PATTERN)?.length ?? 0;
    if (residual !== 0) {
      console.error(
        `[rewrite-web-asset-paths] ${chunk} 替换后仍残留 ${residual} 处，终止`
      );
      process.exit(1);
    }
    console.log(
      `[rewrite-web-asset-paths] ${chunk} 替换 ${result.matches} 处 /lib/ -> ./lib/`
    );
  }
}

if (totalMatches === 0) {
  console.log("[rewrite-web-asset-paths] 无需改写（已处理过），跳过");
} else if (totalMatches !== EXPECTED_MATCHES) {
  console.error(
    `[rewrite-web-asset-paths] 命中 ${totalMatches} 处，期望 ${EXPECTED_MATCHES} 处，终止`
  );
  process.exit(1);
} else {
  console.log(`[rewrite-web-asset-paths] 替换 ${totalMatches} 处，完成`);
}

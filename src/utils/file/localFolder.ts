import { ConfigService } from "../../assets/lib/kookit-extra-browser.min";
import { LOCAL_FOLDER_PATH_KEY } from "../../constants/featureFlag";

declare var window: any;

/**
 * 读取本地文件夹同步目录。
 *
 * 专业版关闭后本地文件夹同步不再经过第三方令牌加解密，目录以明文保存在
 * `proLocalFolderPath` 中，避免依赖登录态接口。
 *
 * @returns 绝对路径；未配置时返回空字符串。
 */
export const getLocalFolderPath = (): string => {
  return ConfigService.getItem(LOCAL_FOLDER_PATH_KEY) || "";
};

/**
 * 保存本地文件夹同步目录。
 *
 * @param path 用户通过系统目录选择器选中的绝对路径。
 * @returns 无返回值；配置写入失败由 ConfigService 自行抛出。
 */
export const setLocalFolderPath = (path: string): void => {
  ConfigService.setItem(LOCAL_FOLDER_PATH_KEY, path);
};

/**
 * 清空本地文件夹同步目录。
 *
 * 用于删除本地文件夹数据源时同步清理本地配置，避免残留路径继续参与同步。
 */
export const clearLocalFolderPath = (): void => {
  ConfigService.removeItem(LOCAL_FOLDER_PATH_KEY);
};

/**
 * 判断本地文件夹目录是否仍然可用。
 *
 * 仅在桌面端能访问文件系统时校验目录存在性；Web 端没有本地文件夹能力，
 * 直接返回 false，调用方据此回落到本地库与本地备份。
 *
 * @returns 目录存在且可用于同步时为 true。
 */
export const isLocalFolderAvailable = (): boolean => {
  const path = getLocalFolderPath();
  const fs = window?.electronAPI?.fs;
  if (!path || !fs || typeof fs.existsSync !== "function") {
    return false;
  }
  try {
    return Boolean(fs.existsSync(path));
  } catch (error) {
    console.error("Failed to check local folder:", error);
    return false;
  }
};

/**
 * 生成同步层使用的本地文件夹配置。
 *
 * 同步工具与主进程 `cloud-*` 通道按 `drivePath` 读取目录，这里保持与
 * 加密令牌配置相同的字段形状。
 *
 * @returns 含 `drivePath` 的配置对象；未配置目录时返回空对象。
 */
export const getLocalFolderConfig = (): { drivePath?: string } => {
  const path = getLocalFolderPath();
  return path ? { drivePath: path } : {};
};

/**
 * 专业版功能总开关。
 *
 * 为 false 时，账户体系、付费入口、第三方云盘同步与官方 AI 云服务在 UI
 * 与插件注册层全部不可达；本地图书馆、本地文件夹同步（免账号）、本地
 * 备份/还原、百度网盘导入、自定义 AI、系统 TTS、本地词典与
 * 系统/Paddle/Tesseract OCR 保持可用。
 *
 * 回滚方式：改回 true 后重启，`restoreProFeaturePolicy` 会从
 * `proFeatureBackupV1` 回填被降级的配置；代码级回滚为丢弃实施分支。
 */
export const PRO_ENABLED = false;

/** 专业版关闭时仍可用的云同步数据源，仅本地文件夹（免账号）。 */
export const FREE_DRIVE_VALUES = ["folder"];

/** 官方 AI 插件 key 的公共前缀，用于统一过滤官方插件。 */
export const OFFICIAL_PLUGIN_PREFIX = "official-ai-";

/** 本地文件夹同步目录的本地配置键，不参与云端令牌加解密。 */
export const LOCAL_FOLDER_PATH_KEY = "proLocalFolderPath";

/** 被降级的配置值备份键，开关恢复为 true 时用于回填。 */
export const PRO_BACKUP_KEY = "proFeatureBackupV1";

import { ConfigService } from "../../assets/lib/kookit-extra-browser.min";
import {
  FREE_DRIVE_VALUES,
  OFFICIAL_PLUGIN_PREFIX,
  PRO_BACKUP_KEY,
  PRO_ENABLED,
} from "../../constants/featureFlag";

/** 官方 AI 服务配置键与其对应的官方插件 key。 */
const OFFICIAL_SERVICE_CONFIG: Record<string, string> = {
  voiceEngine: "official-ai-voice-plugin",
  dictService: "official-ai-dict-plugin",
  transService: "official-ai-trans-plugin",
  aiService: "official-ai-assistant-plugin",
};

/** 官方服务降级后的回退值：语音回落到系统引擎，其余回落到未选择。 */
const OFFICIAL_SERVICE_FALLBACK: Record<string, string> = {
  voiceEngine: "system",
  dictService: "",
  transService: "",
  aiService: "",
};

/** 官方 OCR 引擎配置键，降级后回落到系统 OCR。 */
const OFFICIAL_OCR_CONFIG_KEYS = ["textOcrEngine", "scannedOcrEngine"];

/** 多角色朗读按书籍记录的开关列表，降级时整体停用并备份。 */
const MULTI_ROLE_BOOKS_KEY = "multiRoleVoiceBooks";

interface ProFeatureBackup {
  version: number;
  savedAt: string;
  readerConfig: Record<string, string>;
  items: Record<string, string>;
  lists: Record<string, string[]>;
}

/**
 * 判断插件 key 是否属于官方 AI 插件。
 *
 * @param key 插件唯一标识。
 * @returns 官方 AI 插件返回 true。
 */
export const isOfficialAiKey = (key: string): boolean => {
  return Boolean(key) && key.startsWith(OFFICIAL_PLUGIN_PREFIX);
};

/**
 * 判断数据源是否属于关闭专业版后仍可用的免费数据源。
 *
 * @param service 数据源标识，如 `folder`、`webdav`。
 * @returns 属于免费数据源白名单时返回 true。
 */
export const isFreeDriveService = (
  service: string | null | undefined
): boolean => {
  return Boolean(service) && FREE_DRIVE_VALUES.includes(service as string);
};

/**
 * 过滤官方 AI 插件。
 *
 * 专业版关闭时，插件列表里不允许出现官方词典/翻译/助手/语音，全部保留
 * 用户自建插件与本地词典；开启时原样返回，避免影响原行为。
 *
 * @param plugins 待过滤的插件数组。
 * @returns 过滤后的新数组，输入数组不被修改。
 */
export const filterOfficialPlugins = <T extends { key: string }>(
  plugins: T[]
): T[] => {
  if (PRO_ENABLED) {
    return plugins;
  }
  return plugins.filter((plugin) => !isOfficialAiKey(plugin.key));
};

/**
 * 读取已保存的降级配置备份。
 *
 * @returns 解析后的备份对象；不存在或格式异常时返回 null。
 */
const readBackup = (): ProFeatureBackup | null => {
  const raw = ConfigService.getItem(PRO_BACKUP_KEY);
  if (!raw) {
    return null;
  }
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return null;
    }
    return {
      version: parsed.version || 1,
      savedAt: parsed.savedAt || "",
      readerConfig: parsed.readerConfig || {},
      items: parsed.items || {},
      lists: parsed.lists || {},
    };
  } catch (error) {
    console.error("Failed to parse pro feature backup:", error);
    return null;
  }
};

/**
 * 写入降级配置备份。
 *
 * 只在首次降级时落盘：已有备份说明本次关闭专业版之前的状态已经记录，
 * 后续启动不能覆盖，否则回滚时会丢失原始值。
 *
 * @param backup 待写入的备份对象。
 */
const writeBackup = (backup: ProFeatureBackup): void => {
  if (readBackup()) {
    return;
  }
  ConfigService.setItem(PRO_BACKUP_KEY, JSON.stringify(backup));
};

/**
 * 关闭专业版时降级官方服务配置。
 *
 * 在插件加载前调用：把指向官方语音/词典/翻译/助手/OCR 的当前选择备份后
 * 回落到本地能力，并停用 Koodo Sync、多角色朗读与非免费默认数据源。
 * 云盘令牌、`dataSourceList`、插件记录与书籍数据都不删除，开关恢复可回填。
 */
export const applyProFeaturePolicy = (): void => {
  if (PRO_ENABLED) {
    return;
  }
  const backup: ProFeatureBackup = {
    version: 1,
    savedAt: new Date().toISOString(),
    readerConfig: {},
    items: {},
    lists: {},
  };
  // 先只读采集待降级项，把写操作延后到备份落盘之后；
  // 这样任何一处写入失败或进程中断，都能凭备份回填用户原选择。
  const pendingWrites: Array<() => void> = [];

  for (const [key, officialValue] of Object.entries(OFFICIAL_SERVICE_CONFIG)) {
    const value = ConfigService.getReaderConfig(key);
    if (value && value === officialValue) {
      backup.readerConfig[key] = value;
      pendingWrites.push(() => {
        ConfigService.setReaderConfig(
          key,
          OFFICIAL_SERVICE_FALLBACK[key] || ""
        );
      });
    }
  }

  for (const key of OFFICIAL_OCR_CONFIG_KEYS) {
    const value = ConfigService.getReaderConfig(key);
    if (value === "official-ai-ocr") {
      backup.readerConfig[key] = value;
      pendingWrites.push(() => {
        ConfigService.setReaderConfig(key, "system-ocr");
      });
    }
  }

  const fullTranslationMode = ConfigService.getReaderConfig(
    "fullTranslationMode"
  );
  if (fullTranslationMode && fullTranslationMode !== "no") {
    backup.readerConfig.fullTranslationMode = fullTranslationMode;
    pendingWrites.push(() => {
      ConfigService.setReaderConfig("fullTranslationMode", "no");
    });
  }

  const multiRoleBooks = ConfigService.getAllListConfig(MULTI_ROLE_BOOKS_KEY);
  if (multiRoleBooks && multiRoleBooks.length > 0) {
    const books = [...multiRoleBooks];
    backup.lists[MULTI_ROLE_BOOKS_KEY] = books;
    pendingWrites.push(() => {
      for (const bookKey of books) {
        ConfigService.deleteListConfig(bookKey, MULTI_ROLE_BOOKS_KEY);
      }
    });
  }

  if (ConfigService.getReaderConfig("isEnableKoodoSync") === "yes") {
    backup.readerConfig.isEnableKoodoSync = "yes";
    pendingWrites.push(() => {
      ConfigService.setReaderConfig("isEnableKoodoSync", "no");
    });
  }

  const defaultSyncOption = ConfigService.getItem("defaultSyncOption");
  if (defaultSyncOption && !isFreeDriveService(defaultSyncOption)) {
    backup.items.defaultSyncOption = defaultSyncOption;
    pendingWrites.push(() => {
      ConfigService.removeItem("defaultSyncOption");
    });
  }

  if (pendingWrites.length === 0) {
    return;
  }
  writeBackup(backup);
  for (const applyWrite of pendingWrites) {
    applyWrite();
  }
};

/**
 * 恢复专业版时回填被降级的配置。
 *
 * 仅在 `PRO_ENABLED` 为 true 时由启动流程调用；恢复后立即删除备份，
 * 避免后续启动反复覆盖用户新选择。备份不存在时不做任何事。
 */
export const restoreProFeaturePolicy = (): void => {
  if (!PRO_ENABLED) {
    return;
  }
  const backup = readBackup();
  if (!backup) {
    return;
  }

  for (const [key, value] of Object.entries(backup.readerConfig)) {
    ConfigService.setReaderConfig(key, value);
  }
  for (const [key, value] of Object.entries(backup.items)) {
    ConfigService.setItem(key, value);
  }
  for (const [key, values] of Object.entries(backup.lists)) {
    for (const value of values) {
      ConfigService.setListConfig(value, key);
    }
  }
  ConfigService.removeItem(PRO_BACKUP_KEY);
};

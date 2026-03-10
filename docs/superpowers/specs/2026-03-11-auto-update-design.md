# Auto Update Design

**Date:** 2026-03-11
**Status:** Approved
**Topic:** 将手动更新改为应用内自动下载 + 提示安装

---

## Background

现有更新流程需要用户手动执行三步：检查 → 下载 → 重启。目标是改为：应用启动后自动检测并下载新版本，下载完成后通过 toast 提示用户点击"立即重启"安装。

---

## Architecture Overview

```
App 启动 (5s 延迟)
  → main process: autoUpdater.checkForUpdates()
  → [有新版本] autoDownload=true 自动开始下载
  → IPC 事件流：update-available → download-progress(%) → update-downloaded
  → renderer: useUpdateCheck 订阅事件，更新 state
  → App.tsx: 下载中可在 Settings > System 查看进度
           → 下载完成弹 toast "立即重启"
           → 下载失败弹 toast + "打开 Releases" 降级链接

[Linux deb 等不支持 electron-updater 的平台]
  → isAutoUpdateSupported() === false → startAutoCheck() 不调用
  → 保持原有 GitHub API 通知 + "打开 Releases 页"行为
```

**各层职责：**

| 层 | 文件 | 职责变化 |
|---|---|---|
| 主进程 | `electron/bridges/autoUpdateBridge.cjs` | `autoDownload=true`；全局持久化事件监听；新增 `startAutoCheck()` |
| 主进程 | `electron/main.cjs` | 窗口 `ready-to-show` 后调用 `startAutoCheck(5000)` |
| Preload | `electron/preload.cjs` | 新增 `onUpdateAvailable` 事件订阅暴露 |
| Renderer Hook | `application/state/useUpdateCheck.ts` | 新增 `autoDownloadStatus`/`downloadPercent`/`downloadError` state；订阅 electron-updater IPC 事件；新增 `installUpdate` 返回值 |
| Renderer UI | `App.tsx` | 新增 `autoDownloadStatus === 'ready'` toast；新增 `autoDownloadStatus === 'error'` toast |
| Renderer UI | `components/settings/tabs/SettingsSystemTab.tsx` | 下载进度/就绪状态改由 `useUpdateCheck` 的 state 驱动 |

---

## Detailed Design

### 1. `autoUpdateBridge.cjs`

```js
// 1. 改为自动下载
autoUpdater.autoDownload = true;
autoUpdater.autoInstallOnAppQuit = false;

// 2. 全局持久化监听器（在 init() 时注册一次）
function setupGlobalListeners() {
  const updater = getAutoUpdater();
  if (!updater) return;

  updater.on('update-available', (info) => {
    getSenderWindow()?.webContents.send('netcatty:update:update-available', {
      version: info.version,
      releaseNotes: typeof info.releaseNotes === 'string' ? info.releaseNotes : '',
      releaseDate: info.releaseDate ?? null,
    });
  });
  updater.on('download-progress', (info) => {
    getSenderWindow()?.webContents.send('netcatty:update:download-progress', {
      percent: info.percent ?? 0,
      bytesPerSecond: info.bytesPerSecond ?? 0,
      transferred: info.transferred ?? 0,
      total: info.total ?? 0,
    });
  });
  updater.on('update-downloaded', () => {
    getSenderWindow()?.webContents.send('netcatty:update:downloaded');
  });
  updater.on('error', (err) => {
    getSenderWindow()?.webContents.send('netcatty:update:error', {
      error: err?.message || 'Unknown error',
    });
  });
}

// 3. 新增 startAutoCheck
function startAutoCheck(delayMs = 5000) {
  if (!isAutoUpdateSupported()) return;
  setTimeout(async () => {
    try {
      await getAutoUpdater()?.checkForUpdates();
    } catch (err) {
      console.warn('[AutoUpdate] Auto-check failed:', err?.message || err);
    }
  }, delayMs);
}

module.exports = { init, registerHandlers, isAutoUpdateSupported, startAutoCheck };
```

`init()` 内调用 `setupGlobalListeners()`。

**注意：** 原 `netcatty:update:download` IPC handler 内的一次性监听器需移除（避免与全局监听器重复），`netcatty:update:download` handler 本身可保留用于设置页手动触发兼容。

### 2. `electron/main.cjs`

```js
const { startAutoCheck } = require('./bridges/autoUpdateBridge.cjs');

mainWindow.once('ready-to-show', () => {
  startAutoCheck(5000);
});
```

### 3. `electron/preload.cjs`

新增 `update-available` 事件监听集合及暴露：

```js
const updateAvailableListeners = new Set();

ipcRenderer.on('netcatty:update:update-available', (_event, payload) => {
  updateAvailableListeners.forEach((cb) => {
    try { cb(payload); } catch (err) { console.error('onUpdateAvailable callback failed', err); }
  });
});

// 在 contextBridge.exposeInMainWorld 中新增：
onUpdateAvailable: (cb) => {
  updateAvailableListeners.add(cb);
  return () => updateAvailableListeners.delete(cb);
},
```

### 4. `application/state/useUpdateCheck.ts`

**新增 state 字段：**

```ts
type AutoDownloadStatus = 'idle' | 'downloading' | 'ready' | 'error';

interface UpdateState {
  // 现有字段不变...
  autoDownloadStatus: AutoDownloadStatus;
  downloadPercent: number;
  downloadError: string | null;
}
```

初始值：`autoDownloadStatus: 'idle', downloadPercent: 0, downloadError: null`

**新增 useEffect 订阅 electron-updater IPC 事件：**

```ts
useEffect(() => {
  const bridge = netcattyBridge.get();

  const cleanupAvailable = bridge?.onUpdateAvailable?.((info) => {
    setUpdateState((prev) => ({
      ...prev,
      autoDownloadStatus: 'downloading',
      downloadPercent: 0,
      downloadError: null,
    }));
  });

  const cleanupProgress = bridge?.onUpdateDownloadProgress?.((p) => {
    setUpdateState((prev) => ({
      ...prev,
      autoDownloadStatus: 'downloading',
      downloadPercent: Math.round(p.percent),
    }));
  });

  const cleanupDownloaded = bridge?.onUpdateDownloaded?.(() => {
    setUpdateState((prev) => ({
      ...prev,
      autoDownloadStatus: 'ready',
      downloadPercent: 100,
    }));
  });

  const cleanupError = bridge?.onUpdateError?.((payload) => {
    setUpdateState((prev) => ({
      ...prev,
      autoDownloadStatus: 'error',
      downloadError: payload.error,
    }));
  });

  return () => {
    cleanupAvailable?.();
    cleanupProgress?.();
    cleanupDownloaded?.();
    cleanupError?.();
  };
}, []);
```

**新增 `installUpdate` 并加入返回值：**

```ts
const installUpdate = useCallback(() => {
  netcattyBridge.get()?.installUpdate?.();
}, []);

return { updateState, checkNow, dismissUpdate, openReleasePage, installUpdate };
```

### 5. `App.tsx`

```ts
const { updateState, dismissUpdate, installUpdate } = useUpdateCheck();

// 下载完成 → 提示重启
useEffect(() => {
  if (updateState.autoDownloadStatus === 'ready') {
    toast.info(
      t('update.readyToInstall.message', { version: updateState.latestRelease?.version ?? '' }),
      {
        title: t('update.readyToInstall.title'),
        duration: Infinity, // 持久显示直到用户操作
        actionLabel: t('update.restartNow'),
        onClick: () => installUpdate(),
      }
    );
  }
}, [updateState.autoDownloadStatus, updateState.latestRelease?.version, t, installUpdate]);

// 下载失败 → 降级提示
useEffect(() => {
  if (updateState.autoDownloadStatus === 'error') {
    toast.error(
      t('update.downloadFailed.message'),
      {
        title: t('update.downloadFailed.title'),
        actionLabel: t('update.openReleases'),
        onClick: () => openReleasePage(),
      }
    );
  }
}, [updateState.autoDownloadStatus, t, openReleasePage]);
```

### 6. `SettingsSystemTab.tsx`

- 接收 `autoDownloadStatus` 和 `downloadPercent` 作为 props（或通过 hook 共享）
- 当 `autoDownloadStatus === 'downloading'` → 进度条显示（现有 UI 复用）
- 当 `autoDownloadStatus === 'ready'` → 显示"立即重启"按钮
- 手动"检查更新"按钮保留

---

## Platform Fallback

| 平台 | electron-updater 支持 | 行为 |
|---|---|---|
| Windows (NSIS) | ✅ | 自动检测 + 下载 + 提示重启 |
| macOS (dmg/zip) | ✅ | 同上 |
| Linux AppImage | ✅ | 同上 |
| Linux deb/rpm/snap | ❌ | `startAutoCheck()` 不执行；GitHub API 通知 + "打开 Releases" |

---

## i18n Keys Required

新增以下 i18n key（中英文）：

- `update.readyToInstall.title`
- `update.readyToInstall.message` (带 `{version}` 插值)
- `update.downloadFailed.title`
- `update.downloadFailed.message`

---

## Out of Scope

- `autoInstallOnAppQuit`：保持 `false`，不做静默自动安装
- 更新频率策略：保持现有 1 小时间隔（`useUpdateCheck` 的 `UPDATE_CHECK_INTERVAL_MS`）
- 更新回滚机制

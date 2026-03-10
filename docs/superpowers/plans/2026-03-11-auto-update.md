# Auto Update Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将手动三步更新流程（检查 → 下载 → 重启）改为：应用启动后自动检测并下载新版本，下载完成后通过 toast 提示用户点击"立即重启"安装。

**Architecture:** 在主进程 `autoUpdateBridge.cjs` 中启用 `autoDownload=true` 并注册全局持久化 IPC 事件监听，应用启动后自动触发 `checkForUpdates()`；渲染层 `useUpdateCheck` hook 订阅 electron-updater IPC 事件，下载完成时 `App.tsx` 弹出带"立即重启"按钮的持久 toast；`SettingsSystemTab` 进度条由 `useUpdateCheck` state 驱动；Linux deb 等不支持平台自动降级为原有 GitHub API 通知。

**Tech Stack:** Electron, electron-updater ^6.8.3, React, TypeScript, IPC (contextBridge)

---

## File Map

| 文件 | 改动类型 | 职责 |
|------|---------|------|
| `electron/bridges/autoUpdateBridge.cjs` | Modify | `autoDownload=true`；全局持久化事件监听；新增 `startAutoCheck()`；清理 download handler 中的重复/危险清理代码 |
| `electron/main.cjs` | Modify | `createWindow()` resolve 后调用 `startAutoCheck(5000)` |
| `electron/preload.cjs` | Modify | 新增 `onUpdateAvailable` IPC 事件订阅暴露 |
| `global.d.ts` | Modify | `NetcattyBridge` 接口新增 `onUpdateAvailable` 类型定义 |
| `application/state/useUpdateCheck.ts` | Modify | 新增 `AutoDownloadStatus` 类型和三个 state 字段；订阅 IPC 事件；新增 `installUpdate` 返回值 |
| `App.tsx` | Modify | 修改 `hasUpdate` toast 的触发条件；新增 `ready`/`error` 状态 toast |
| `components/settings/tabs/SettingsSystemTab.tsx` | Modify | 删除对 electron-updater 事件的直接订阅；新增 `autoDownloadStatus`/`downloadPercent` props；用同步 effect 驱动本地进度 state |
| `components/SettingsPage.tsx` | Modify | 调用 `useUpdateCheck()` 并向 `SettingsSystemTab` 传入新 props |
| `application/i18n/locales/en.ts` | Modify | 新增 `update.readyToInstall.*`/`update.downloadFailed.*`/`update.restartNow`/`update.openReleases` key |
| `application/i18n/locales/zh-CN.ts` | Modify | 同上中文翻译 |

---

## Chunk 1: Git Branch Setup

### Task 0: 创建功能分支

- [ ] **Step 1: 创建并切换到新分支**

```bash
cd E:/code/project/Netcatty
git checkout -b feat/auto-update
```

Expected: `Switched to a new branch 'feat/auto-update'`

---

## Chunk 2: Main Process — autoUpdateBridge

### Task 1: 重构 autoUpdateBridge.cjs

**Files:**
- Modify: `electron/bridges/autoUpdateBridge.cjs`

**背景：** 当前 `autoDownload = false`，进度监听器是在用户手动点击下载时临时注册的一次性监听器。需改为全局持久化监听。注意：现有 `netcatty:update:download` handler 的 catch 块中有 `updaterForCleanup.removeAllListeners(...)` 调用，这在新架构下会意外清除全局监听器，必须移除。

- [ ] **Step 1: 修改 `getAutoUpdater()` 中的 `autoDownload` 设置**

在 `getAutoUpdater()` 函数中，找到：
```js
autoUpdater.autoDownload = false;
```
改为：
```js
autoUpdater.autoDownload = true;
```

- [ ] **Step 2: 新增 `setupGlobalListeners()` 函数**

在 `init` 函数定义之前插入：

```js
/**
 * Register persistent global IPC event listeners for auto-download flow.
 * Called once in init(). Forwards electron-updater events to the renderer
 * even when no manual download was initiated.
 */
function setupGlobalListeners() {
  const updater = getAutoUpdater();
  if (!updater) return;

  updater.on("update-available", (info) => {
    const win = getSenderWindow();
    if (win && !win.isDestroyed()) {
      win.webContents.send("netcatty:update:update-available", {
        version: info.version || "",
        releaseNotes: typeof info.releaseNotes === "string" ? info.releaseNotes : "",
        releaseDate: info.releaseDate || null,
      });
    }
  });

  updater.on("download-progress", (info) => {
    const win = getSenderWindow();
    if (win && !win.isDestroyed()) {
      win.webContents.send("netcatty:update:download-progress", {
        percent: info.percent ?? 0,
        bytesPerSecond: info.bytesPerSecond ?? 0,
        transferred: info.transferred ?? 0,
        total: info.total ?? 0,
      });
    }
  });

  updater.on("update-downloaded", () => {
    const win = getSenderWindow();
    if (win && !win.isDestroyed()) {
      win.webContents.send("netcatty:update:downloaded");
    }
  });

  updater.on("error", (err) => {
    const win = getSenderWindow();
    if (win && !win.isDestroyed()) {
      win.webContents.send("netcatty:update:error", {
        error: err?.message || "Unknown update error",
      });
    }
  });

  console.log("[AutoUpdate] Global listeners registered");
}
```

- [ ] **Step 3: 在 `init()` 中调用 `setupGlobalListeners()`**

找到：
```js
function init(deps) {
  _deps = deps;
}
```
改为：
```js
function init(deps) {
  _deps = deps;
  setupGlobalListeners();
}
```

- [ ] **Step 4: 新增 `startAutoCheck()` 函数**

在 `setupGlobalListeners` 函数之后插入：

```js
/**
 * Trigger an automatic update check after a delay.
 * No-op on platforms that don't support auto-update (Linux deb/rpm/snap).
 * Called from main process after the main window is created.
 *
 * @param {number} delayMs - Milliseconds to wait before checking (default: 5000)
 */
function startAutoCheck(delayMs = 5000) {
  if (!isAutoUpdateSupported()) {
    console.log("[AutoUpdate] Platform does not support auto-update, skipping auto-check");
    return;
  }
  setTimeout(async () => {
    try {
      console.log("[AutoUpdate] Starting automatic update check...");
      await getAutoUpdater()?.checkForUpdates();
    } catch (err) {
      console.warn("[AutoUpdate] Auto-check failed:", err?.message || err);
    }
  }, delayMs);
}
```

- [ ] **Step 5: 清理 `netcatty:update:download` handler**

找到 `ipcMain.handle("netcatty:update:download", async () => { ... })` handler。

**需要做两件事：**

1. 移除 handler 内部的一次性事件监听器注册（`progressHandler`/`downloadedHandler`/`errorHandler` 及其 `updater.on(...)`/`updater.removeListener(...)` 调用）——这些现在由全局监听器覆盖

2. 移除 catch 块中的 `updaterForCleanup.removeAllListeners(...)` 调用——这会意外清除全局监听器

将整个 handler 替换为：

```js
ipcMain.handle("netcatty:update:download", async () => {
  const updater = getAutoUpdater();
  if (!updater) {
    return { success: false, error: "Update module not available." };
  }
  try {
    // Global listeners (registered in setupGlobalListeners) handle all
    // progress/downloaded/error events. Just trigger the download.
    await updater.downloadUpdate();
    return { success: true };
  } catch (err) {
    console.error("[AutoUpdate] Download failed:", err?.message || err);
    return { success: false, error: err?.message || "Download failed" };
  }
});
```

- [ ] **Step 6: 更新 `module.exports` 导出 `startAutoCheck`**

找到文件末尾：
```js
module.exports = { init, registerHandlers, isAutoUpdateSupported };
```
改为：
```js
module.exports = { init, registerHandlers, isAutoUpdateSupported, startAutoCheck };
```

- [ ] **Step 7: Commit**

```bash
git add electron/bridges/autoUpdateBridge.cjs
git commit -m "feat(auto-update): enable autoDownload and global IPC event listeners"
```

---

## Chunk 3: Main Process — Startup Trigger

### Task 2: 在 main.cjs 中触发启动检查

**Files:**
- Modify: `electron/main.cjs`

**背景：** `startAutoCheck()` 在 `createWindow()` resolve 后立即调用。由于 `startAutoCheck` 内部有 5000ms 延迟，这与 `ready-to-show` 触发时机实际上无差异——窗口在 5s 内必然已完全就绪。`autoUpdateBridge` 已通过 `init(deps)` 在 `registerBridges` 回调中完成初始化。

- [ ] **Step 1: 在 `createWindow()` resolve 后调用 `startAutoCheck`**

在 `app.whenReady().then(...)` 内，找到：
```js
void createWindow().catch((err) => {
  console.error("[Main] Failed to create main window:", err);
  showStartupError(err);
  try {
    app.quit();
  } catch {}
});
```
改为：
```js
void createWindow().then(() => {
  // Trigger auto-update check 5 s after window creation.
  // startAutoCheck() is a no-op on unsupported platforms (Linux deb/rpm/snap).
  autoUpdateBridge.startAutoCheck(5000);
}).catch((err) => {
  console.error("[Main] Failed to create main window:", err);
  showStartupError(err);
  try {
    app.quit();
  } catch {}
});
```

- [ ] **Step 2: Commit**

```bash
git add electron/main.cjs
git commit -m "feat(auto-update): trigger startAutoCheck after main window ready"
```

---

## Chunk 4: Preload & Types

### Task 3: 扩展 preload.cjs 暴露 onUpdateAvailable

**Files:**
- Modify: `electron/preload.cjs`

**背景：** preload 已有 `updateDownloadProgressListeners`、`updateDownloadedListeners` 等 Set。用相同模式新增 `updateAvailableListeners`。

- [ ] **Step 1: 新增 `updateAvailableListeners` Set**

找到：
```js
const updateDownloadProgressListeners = new Set();
const updateDownloadedListeners = new Set();
```
在其后新增：
```js
const updateAvailableListeners = new Set();
```

- [ ] **Step 2: 注册 IPC → Set 分发**

找到：
```js
ipcRenderer.on("netcatty:update:download-progress", (_event, payload) => {
```
在其上方插入：
```js
ipcRenderer.on("netcatty:update:update-available", (_event, payload) => {
  updateAvailableListeners.forEach((cb) => {
    try {
      cb(payload);
    } catch (err) {
      console.error("onUpdateAvailable callback failed", err);
    }
  });
});
```

- [ ] **Step 3: 在 contextBridge 中暴露 `onUpdateAvailable`**

找到：
```js
onUpdateDownloadProgress: (cb) => {
  updateDownloadProgressListeners.add(cb);
  return () => updateDownloadProgressListeners.delete(cb);
},
```
在其上方插入：
```js
onUpdateAvailable: (cb) => {
  updateAvailableListeners.add(cb);
  return () => updateAvailableListeners.delete(cb);
},
```

- [ ] **Step 4: Commit**

```bash
git add electron/preload.cjs
git commit -m "feat(auto-update): expose onUpdateAvailable in preload bridge"
```

### Task 4: 更新 global.d.ts 类型定义

**Files:**
- Modify: `global.d.ts`

- [ ] **Step 1: 在 `NetcattyBridge` 接口新增 `onUpdateAvailable`**

找到：
```ts
onUpdateDownloaded?(cb: () => void): () => void;
onUpdateError?(cb: (payload: { error: string }) => void): () => void;
```
在 `onUpdateDownloaded` 之前插入：
```ts
onUpdateAvailable?(cb: (info: {
  version: string;
  releaseNotes: string;
  releaseDate: string | null;
}) => void): () => void;
```

- [ ] **Step 2: Commit**

```bash
git add global.d.ts
git commit -m "feat(auto-update): add onUpdateAvailable type to NetcattyBridge"
```

---

## Chunk 5: Renderer Hook

### Task 5: 扩展 useUpdateCheck.ts

**Files:**
- Modify: `application/state/useUpdateCheck.ts`

**背景：** 新增自动下载状态追踪。`latestRelease` 已由 GitHub API 检测填充；`onUpdateAvailable` 回调中同时用 electron-updater 的 `info.version` 作为兜底，防止两个检测时序不同步时 toast 显示空版本号。

- [ ] **Step 1: 新增 `AutoDownloadStatus` 类型，在 `UpdateState` 接口中新增三个字段**

找到 `export interface UpdateState {` 定义，在其中补充：
```ts
export type AutoDownloadStatus = 'idle' | 'downloading' | 'ready' | 'error';

export interface UpdateState {
  isChecking: boolean;
  hasUpdate: boolean;
  currentVersion: string;
  latestRelease: ReleaseInfo | null;
  error: string | null;
  lastCheckedAt: number | null;
  // Auto-download state — driven by electron-updater IPC events
  autoDownloadStatus: AutoDownloadStatus;
  downloadPercent: number;
  downloadError: string | null;
}
```

- [ ] **Step 2: 在 `UseUpdateCheckResult` 接口中新增 `installUpdate`**

找到 `export interface UseUpdateCheckResult {`，新增一行：
```ts
installUpdate: () => void;
```

- [ ] **Step 3: 扩展 `useState` 初始值**

找到 `useState<UpdateState>({` 的初始值对象，新增三个字段：
```ts
autoDownloadStatus: 'idle',
downloadPercent: 0,
downloadError: null,
```

- [ ] **Step 4: 新增订阅 electron-updater IPC 事件的 `useEffect`**

在加载版本号的 `useEffect` 之后，`performCheck` 定义之前，插入：

```ts
// Subscribe to electron-updater auto-download IPC events.
// These fire automatically when autoDownload=true in the main process.
useEffect(() => {
  const bridge = netcattyBridge.get();

  const cleanupAvailable = bridge?.onUpdateAvailable?.((info) => {
    setUpdateState((prev) => ({
      ...prev,
      autoDownloadStatus: 'downloading',
      downloadPercent: 0,
      downloadError: null,
      // Use electron-updater's version as fallback if GitHub API hasn't resolved yet
      latestRelease: prev.latestRelease ?? {
        version: info.version,
        tagName: `v${info.version}`,
        name: `v${info.version}`,
        body: info.releaseNotes || '',
        htmlUrl: '',
        publishedAt: info.releaseDate || new Date().toISOString(),
        assets: [],
      },
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

- [ ] **Step 5: 新增 `installUpdate` callback**

在 `openReleasePage` callback 定义之后新增：
```ts
const installUpdate = useCallback(() => {
  netcattyBridge.get()?.installUpdate?.();
}, []);
```

- [ ] **Step 6: 更新 return 语句**

找到：
```ts
return {
  updateState,
  checkNow,
  dismissUpdate,
  openReleasePage,
};
```
改为：
```ts
return {
  updateState,
  checkNow,
  dismissUpdate,
  openReleasePage,
  installUpdate,
};
```

- [ ] **Step 7: Commit**

```bash
git add application/state/useUpdateCheck.ts
git commit -m "feat(auto-update): add autoDownloadStatus state and IPC subscriptions to useUpdateCheck"
```

---

## Chunk 6: i18n Keys

### Task 6: 新增 i18n 翻译 key

**Files:**
- Modify: `application/i18n/locales/en.ts`
- Modify: `application/i18n/locales/zh-CN.ts`

**注意：** 以下已有 key 不要修改：
- `settings.update.restartNow`（值 `'Restart to Update'`）— 用于 Settings Tab 按钮
- `settings.update.readyToInstall`（值 `'Update downloaded and ready to install.'`）— 用于 Settings Tab 状态文字

新增的是 `update.*`（无 `settings.` 前缀）命名空间的 toast 专用 key，两者不冲突。

- [ ] **Step 1: 在 `en.ts` 中新增 key**

找到 `'update.viewInSettings': ...` 行，在其后追加：
```ts
'update.readyToInstall.title': 'Update Ready',
'update.readyToInstall.message': 'Version {version} downloaded and ready to install.',
'update.restartNow': 'Restart Now',
'update.downloadFailed.title': 'Update Failed',
'update.downloadFailed.message': 'Failed to download update. You can download it manually.',
'update.openReleases': 'Open Releases',
```

- [ ] **Step 2: 在 `zh-CN.ts` 中新增对应中文 key**

找到对应位置（`'update.viewInSettings'` 附近），追加：
```ts
'update.readyToInstall.title': '更新已就绪',
'update.readyToInstall.message': '版本 {version} 已下载完成，准备安装。',
'update.restartNow': '立即重启',
'update.downloadFailed.title': '更新失败',
'update.downloadFailed.message': '下载更新失败，可前往 GitHub 手动下载。',
'update.openReleases': '打开 Releases',
```

- [ ] **Step 3: Commit**

```bash
git add application/i18n/locales/en.ts application/i18n/locales/zh-CN.ts
git commit -m "feat(auto-update): add i18n keys for ready-to-install and download-failed toasts"
```

---

## Chunk 7: App.tsx Toast Notifications

### Task 7: 在 App.tsx 新增 toast 通知

**Files:**
- Modify: `App.tsx`

**背景：** `App.tsx` 已在约第 307 行解构 `useUpdateCheck()`，约第 313 行有一个 `hasUpdate` toast effect。需要：
1. 修改 `hasUpdate` toast 的触发条件（当自动下载已开始时不重复弹出）
2. 新增"下载完成"toast
3. 新增"下载失败"toast

- [ ] **Step 1: 从 `useUpdateCheck()` 解构新增返回值**

找到：
```ts
const { updateState, dismissUpdate } = useUpdateCheck();
```
改为：
```ts
const { updateState, dismissUpdate, openReleasePage, installUpdate } = useUpdateCheck();
```

（如果 `openReleasePage` 已在其他地方通过 `useUpdateCheck` 解构，检查是否重复，合并到同一个解构即可）

- [ ] **Step 2: 修改现有 `hasUpdate` toast effect，当自动下载已启动时跳过**

找到：
```ts
useEffect(() => {
  if (updateState.hasUpdate && updateState.latestRelease) {
```
在条件前新增一个早返回，防止自动下载流程中重复弹出通知：
```ts
useEffect(() => {
  // Skip "update available" toast if auto-download has already started or completed
  if (updateState.autoDownloadStatus !== 'idle') return;
  if (updateState.hasUpdate && updateState.latestRelease) {
```

- [ ] **Step 3: 新增"下载完成，立即重启"toast effect**

在现有 `hasUpdate` toast effect 之后新增：
```ts
// Persistent toast when update is downloaded and ready to install
useEffect(() => {
  if (updateState.autoDownloadStatus !== 'ready') return;
  const version = updateState.latestRelease?.version ?? '';
  toast.info(
    t('update.readyToInstall.message', { version }),
    {
      title: t('update.readyToInstall.title'),
      duration: Infinity,
      actionLabel: t('update.restartNow'),
      onClick: () => installUpdate(),
    }
  );
}, [updateState.autoDownloadStatus, updateState.latestRelease?.version, t, installUpdate]);
```

- [ ] **Step 4: 新增"下载失败"toast effect**

紧接着新增：
```ts
// Error toast when auto-download fails, with manual fallback
useEffect(() => {
  if (updateState.autoDownloadStatus !== 'error') return;
  toast.error(
    t('update.downloadFailed.message'),
    {
      title: t('update.downloadFailed.title'),
      actionLabel: t('update.openReleases'),
      onClick: () => openReleasePage(),
    }
  );
}, [updateState.autoDownloadStatus, t, openReleasePage]);
```

- [ ] **Step 5: 验证 TypeScript 编译通过**

```bash
npx tsc --noEmit
```
Expected: 无报错（或仅有与本次修改无关的既有警告）

- [ ] **Step 6: Commit**

```bash
git add App.tsx
git commit -m "feat(auto-update): add ready-to-install and download-failed toast notifications"
```

---

## Chunk 8: SettingsSystemTab Sync

### Task 8: SettingsSystemTab 使用 props 驱动进度状态

**Files:**
- Modify: `components/settings/tabs/SettingsSystemTab.tsx`
- Modify: `components/SettingsPage.tsx`

**背景：** `SettingsSystemTab` 目前在第 97-113 行直接订阅 electron-updater IPC 事件（`onDownloadProgress`/`onDownloaded`/`onError as onUpdateError`）。这些事件现在由 `useUpdateCheck` 统一管理。需要：
1. 删除 `SettingsSystemTab` 中的直接 IPC 订阅
2. 改为接收来自父组件的 `autoDownloadStatus`/`downloadPercent` props
3. 用 effect 将 props 同步到本地 `updateStatus`/`updatePercent` state
4. 在父组件 `SettingsPage.tsx` 中调用 `useUpdateCheck()` 并传入新 props

**状态冲突说明：** `updateStatus` 本地 state 仍处理手动检查流程（`idle`/`checking`/`available`/`up-to-date`/`error`）。同步 effect 只在 `autoDownloadStatus` 为 `'downloading'`/`'ready'` 时覆盖本地 state，不处理 `'idle'`/`'error'`（避免覆盖手动检查的错误信息）。

---

**SettingsSystemTab.tsx 修改：**

- [ ] **Step 1: 删除对 electron-updater IPC 事件的直接 import**

找到文件顶部 import 块（第 9-17 行）：
```ts
import {
  checkForUpdate,
  downloadUpdate,
  installUpdate,
  onDownloadProgress,
  onDownloaded,
  onError as onUpdateError,
  getReleasesUrl,
} from "../../../infrastructure/services/updateService";
```
删除其中的 `onDownloadProgress`、`onDownloaded`、`onError as onUpdateError` 三行，保留其余 import：
```ts
import {
  checkForUpdate,
  downloadUpdate,
  installUpdate,
  getReleasesUrl,
} from "../../../infrastructure/services/updateService";
```

- [ ] **Step 2: 在 `SettingsSystemTabProps` 中新增两个 props**

在 `SettingsSystemTabProps` 接口末尾新增：
```ts
import type { AutoDownloadStatus } from '../../../application/state/useUpdateCheck';

// ...在 interface 内：
autoDownloadStatus: AutoDownloadStatus;
downloadPercent: number;
```

- [ ] **Step 3: 在组件函数签名中解构新 props**

找到：
```ts
const SettingsSystemTab: React.FC<SettingsSystemTabProps> = ({
  sessionLogsEnabled,
  // ... 其余 props
  hotkeyRegistrationError,
}) => {
```
新增解构：
```ts
const SettingsSystemTab: React.FC<SettingsSystemTabProps> = ({
  sessionLogsEnabled,
  // ... 其余 props
  hotkeyRegistrationError,
  autoDownloadStatus,
  downloadPercent,
}) => {
```

- [ ] **Step 4: 删除第 97-113 行的直接 IPC 订阅 useEffect**

删除以下整块代码：
```ts
// Subscribe to auto-update events
useEffect(() => {
  const cleanupProgress = onDownloadProgress((p) => {
    setUpdatePercent(Math.round(p.percent));
  });
  const cleanupDownloaded = onDownloaded(() => {
    setUpdateStatus('ready');
  });
  const cleanupError = onUpdateError((payload) => {
    setUpdateError(payload.error);
    setUpdateStatus('error');
  });
  return () => {
    cleanupProgress?.();
    cleanupDownloaded?.();
    cleanupError?.();
  };
}, []);
```

- [ ] **Step 5: 新增 props → 本地 state 同步 effect**

在删除的 useEffect 原位置插入：

```ts
// Sync auto-download progress from parent (useUpdateCheck) into local state.
// Only overrides 'downloading' and 'ready' — manual check states are unaffected.
useEffect(() => {
  if (autoDownloadStatus === 'downloading') {
    setUpdateStatus('downloading');
    setUpdatePercent(downloadPercent);
  } else if (autoDownloadStatus === 'ready') {
    setUpdateStatus('ready');
  }
}, [autoDownloadStatus, downloadPercent]);
```

- [ ] **Step 6: TypeScript 编译验证**

```bash
npx tsc --noEmit
```
Expected: 无报错

---

**SettingsPage.tsx 修改：**

- [ ] **Step 7: 在 `SettingsPage.tsx` 中引入 `useUpdateCheck`**

在文件顶部 import 区域新增：
```ts
import { useUpdateCheck } from '../application/state/useUpdateCheck';
```

- [ ] **Step 8: 在 `SettingsPageContent` 组件中调用 `useUpdateCheck()`**

找到 `SettingsPageContent` 组件（接收 `settings` prop 的那个）。在组件函数体内，现有 hooks 调用之后新增：
```ts
const { updateState } = useUpdateCheck();
```

- [ ] **Step 9: 向 `SettingsSystemTab` 传入新 props**

找到（约第 228-240 行）：
```tsx
<SettingsSystemTab
  sessionLogsEnabled={settings.sessionLogsEnabled}
  // ... 其余 props
  hotkeyRegistrationError={settings.hotkeyRegistrationError}
/>
```
新增两个 props：
```tsx
<SettingsSystemTab
  sessionLogsEnabled={settings.sessionLogsEnabled}
  // ... 其余 props
  hotkeyRegistrationError={settings.hotkeyRegistrationError}
  autoDownloadStatus={updateState.autoDownloadStatus}
  downloadPercent={updateState.downloadPercent}
/>
```

- [ ] **Step 10: 最终 TypeScript 编译验证**

```bash
npx tsc --noEmit
```
Expected: 无报错

- [ ] **Step 11: Commit**

```bash
git add components/settings/tabs/SettingsSystemTab.tsx components/SettingsPage.tsx
git commit -m "feat(auto-update): drive SettingsSystemTab progress from useUpdateCheck state"
```

---

## Chunk 9: Manual Smoke Test & PR

### Task 9: 手动冒烟测试

由于本项目无 Electron IPC 单元测试，使用手动验证。

- [ ] **Step 1: 启动开发环境**

```bash
npm run dev
```
Expected: 应用正常启动，无 JS 错误

- [ ] **Step 2: 验证 TypeScript 无类型错误**

```bash
npx tsc --noEmit
```
Expected: 无错误

- [ ] **Step 3: 验证 Settings > System 页面正常渲染**

打开应用 → Settings → System Tab → 确认进度/状态区域无白屏/崩溃

- [ ] **Step 4: 验证新 API 已暴露**

在 DevTools Console 中执行：
```js
typeof window.netcatty.onUpdateAvailable   // 应输出 "function"
typeof window.netcatty.installUpdate       // 应输出 "function"
```

- [ ] **Step 5: 验证 useUpdateCheck 新字段存在（React DevTools）**

通过 React DevTools 找到使用 `useUpdateCheck` 的组件，确认 state 中包含：
- `autoDownloadStatus: "idle"`
- `downloadPercent: 0`
- `downloadError: null`

### Task 10: 提交 PR

- [ ] **Step 1: 推送分支**

```bash
git push -u origin feat/auto-update
```

- [ ] **Step 2: 创建 PR**

```bash
gh pr create \
  --title "feat(auto-update): auto-download updates with restart prompt" \
  --body "$(cat <<'EOF'
## Summary

- Enable `autoDownload=true` in `autoUpdateBridge.cjs` with persistent global IPC event listeners (replacing one-shot per-request listeners)
- Remove accidental `removeAllListeners()` call that would have cleared global listeners on download error
- Trigger `checkForUpdates()` automatically 5 s after main window creation
- Expose `onUpdateAvailable` in preload bridge + `global.d.ts`
- Extend `useUpdateCheck` hook: `autoDownloadStatus`/`downloadPercent`/`downloadError` state, electron-updater IPC subscriptions, `installUpdate()`
- `App.tsx`: suppress "update available" toast when auto-download is in progress; add "Restart Now" persistent toast on download complete; add "Download Failed" toast with "Open Releases" fallback
- `SettingsSystemTab`: remove direct IPC subscriptions, receive progress via props from `SettingsPage` (which calls `useUpdateCheck`)
- Linux deb/rpm/snap: `startAutoCheck()` is a no-op via `isAutoUpdateSupported()`, GitHub API notification preserved

## Test plan

- [ ] `npx tsc --noEmit` passes with no errors
- [ ] Dev mode: app launches, Settings > System tab renders correctly
- [ ] `window.netcatty.onUpdateAvailable` and `window.netcatty.installUpdate` are functions in renderer
- [ ] React DevTools: `useUpdateCheck` state shows `autoDownloadStatus: "idle"` on startup
- [ ] (Manual with old binary) Install older version → wait 5 s → verify auto-download starts and "Restart Now" toast appears on completion
- [ ] (Linux deb) Verify `startAutoCheck()` skips silently, GitHub API banner still works
EOF
)"
```

---

## Notes

- **`autoInstallOnAppQuit` 保持 `false`：** 用户必须主动点击"立即重启"，不做静默自动安装。
- **`hasUpdate` toast 与 `ready` toast 不重叠：** `hasUpdate` toast 在 `autoDownloadStatus !== 'idle'` 时跳过，所以用户最多只会看到一个更新相关 toast。
- **手动下载按钮保留：** `SettingsSystemTab` 中的手动"检查更新 → 下载"流程完整保留，作为自动下载的补充和回退。
- **已知限制：** `SettingsSystemTab` 本地 `updateStatus === 'error'` 不与 `autoDownloadStatus === 'error'` 同步，两套错误状态独立显示（Settings Tab 显示手动检查错误，App.tsx toast 显示自动下载错误）。

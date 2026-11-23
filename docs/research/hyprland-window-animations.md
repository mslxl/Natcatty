# Hyprland 窗口 resize 与 open/close 动画调研

Status: research
Date: 2026-11-23
Scope: 只调研 Hyprland（Wayland 合成器）中 **窗口打开（windowsIn）、关闭（windowsOut）与几何变化/resize（windowsMove）** 的配置模型与源码实现。以最新 release **v0.57.2** 为主，并对照最后一个以 hyprlang 为主的版本 **v0.54.0**，说明 0.55–0.56 的过渡与 0.57 的移除。不展开 workspace/layer 动画的完整实现，只在解释继承关系和风格枚举时提及。未在真实 Hyprland 会话中运行验证（本机为 macOS），所有行为结论来自源码阅读。

Sources verified against:

- `hyprwm/Hyprland` tag **v0.57.2**，commit `11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947`（Release 日期 2026-11-09；该 commit 的 `VERSION` 为 `0.57.2`）。
- `hyprwm/Hyprland` `main` @ `423da96907feb3eef7ca4e06dc5cb69e9eec63a8`（2026-11-22），仅用于记录当前主线位置；本报告的行为分析固定在上面的 v0.57.2 tag。
- `hyprwm/Hyprland` tag **v0.54.0**，commit `0002f148c9a4fe421a9d33c0faa5528cdc411e62`（该 commit 的 `VERSION` 为 `0.54.0`）。
- `hyprwm/Hyprland` tag **v0.55.0**，commit `af923e30d1d24f1f4a4f5cb8308065173c1d9539`；tag **v0.57.0**，commit `0aa6745f705f2c4b0a5004eb019ef76e5bbdb426`，用于确认配置系统迁移边界。
- `hyprwm/hyprutils` @ `7e1f69209cd910615d0573a93fa970408f643539`，即 Hyprland v0.57.2 的 `flake.lock` 锁定版本（[flake.lock#L228-L243](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/flake.lock#L228-L243)）。动画曲线、AnimatedVariable 与 spring 的实现在该仓库。
- `hyprwm/hyprland-wiki` `main` @ `8a914cdfce28b332961778816c967467afd76a18`（2026-11-22），动画页源文件：[content/configuring/core/animations.md](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md)。在线页面：<https://wiki.hypr.land/configuring/core/animations/>。
- 历史 wiki 页面：<https://wiki.hypr.land/0.54.0/Configuring/Animations/>（hyprlang 语法）。
- 以上查询与源码阅读日期：2026-11-23。

## 1. 结论摘要

1. **最新 v0.57.2 已经不再使用 hyprlang 的 `animation = NAME, SPEED, CURVE[, STYLE]` 语法**。配置入口只有 `hyprland.lua`，动画由 `hl.animation()` / `hl.curve()` 声明（[ConfigManager.cpp#L15-L35](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/ConfigManager.cpp#L15-L35), [LuaBindingsConfigRules.cpp#L283-L484](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L283-L484)）。旧语法属于 v0.54.x；v0.55–v0.56 保留 legacy `.conf` 回退，v0.57.0 起移除（见 §2.2）。
2. **`windowsIn` / `windowsOut` 只负责打开/关闭时的几何与透明度入场/退场；`windowsMove` 负责“其余一切几何变化”，包括布局重排、移动、拖拽与 resize。** 动画树中没有独立的 resize 叶子，也没有单独的 resize 曲线/速度（[AnimationTree.cpp#L35-L37](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L35-L37), [wiki animation tree](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L33-L38)）。
3. **打开动画**：位置/尺寸用 `windowsIn`，透明度用 `fadeIn`。窗口在 `mapWindow()` 一开始就 `m_isMapped = true` 并在动画开始前获得焦点；源码中没有“打开动画期间禁止输入”的开关，`animatingIn` 只被渲染器使用（[Window.cpp#L1178-L1181](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1178-L1181), [Window.cpp#L1519-L1525](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1519-L1525), [Renderer.cpp#L276-L278](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L276-L278)）。
4. **关闭动画在 v0.57.2 改成“快照淡出”**：`unmapWindow()` 立刻把窗口标记为未映射、从布局移除，并抓取窗口/工作区 framebuffer 生成 `CWindowFadeout`，由快照自己按 `windowsOut` / `fadeOut` 动画到 alpha 0。真实 `CWindow` 的销毁不再被关闭动画阻塞（[Window.cpp#L1649-L1652](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1649-L1652), [Window.cpp#L1687-L1692](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1687-L1692), [WindowFadeout.cpp#L88-L105](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.cpp#L88-L105)）。v0.54 则把窗口对象留在 `m_windowsFadingOut` 中，直到 alpha 归零后才清理（见 §4.3）。
5. **曲线**：bezier 用固定时长，`speed` 的单位是 decisecond（100 ms），公式为 `elapsed_ms / 100 / speed`；spring 是逐帧物理模拟，`speed` 不参与 spring 的推进，只在配置解析时仍被要求填写。内置曲线只有 `default` 与 `linear`；`easeIn` / `easeOut` 等不是内置曲线，示例配置里的 `easeOutQuint` 等都是用户定义（[AnimatedVariable.cpp#L74-L81](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L74-L81), [AnimatedVariable.cpp#L101-L137](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L101-L137), [AnimationManager.cpp#L13-L21](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimationManager.cpp#L13-L21)）。
6. **`misc:animate_manual_resizes` 与 `misc:animate_mouse_windowdragging` 都默认 `false`**。但按 v0.57.2 源码，鼠标拖拽 resize 在 `DragController` 中始终显式 `warpPositionSize()`；`animate_manual_resizes` 只在 Dwindle 的 `resizeTarget()` 中被读取，并作为一个当前实现里基本未被消费的 `force` 参数传递。`animate_mouse_windowdragging` 在 v0.54.0 与 v0.57.2 中都找不到配置声明之外的读取点，疑似遗留/无效项（见 §5.4，标注为未确认）。

## 2. 配置模型

### 2.1 当前 v0.57.2：Lua 配置

v0.57.2 只接受 Lua 配置。配置路径解析只查找 `hyprland.lua`（debug 下为 `hyprlandd.lua`），没有 `.conf` 分支（[Jeremy.cpp#L19-L44](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/supplementary/jeremy/Jeremy.cpp#L19-L44)），配置管理器也只会创建 `Lua::CConfigManager`（[ConfigManager.cpp#L29-L35](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/ConfigManager.cpp#L29-L35)）。

动画声明语法（源码实际实现）：

```lua
hl.animation({ leaf = "windowsIn", enabled = true, speed = 4, bezier = "default", style = "popin 87%" })
hl.animation({ leaf = "windowsIn", enabled = true, speed = 4, spring = "easy", style = "popin 87%" })
hl.curve("snappy", { type = "bezier", points = { {0.2, 0}, {0.1, 1} } })
hl.curve("soft",   { type = "spring", mass = 1, stiffness = 200, damping = 24 })
```

- `leaf` 必须是动画树中已存在的节点，否则报 `no such animation leaf`（[LuaBindingsConfigRules.cpp#L404-L412](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L404-L412)）。
- `enabled = false` 时直接 `setConfigForNode(leaf, false, 1, "default")` 并返回，后面的 `speed` / 曲线可以不写（[LuaBindingsConfigRules.cpp#L414-L424](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L414-L424)）。
- `speed` 为 float，解析范围 0–100，且必须大于 0（[LuaBindingsConfigRules.cpp#L426-L434](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L426-L434)）。wiki 明确说单位是 decisecond，`speed = 1` 即 100 ms（[wiki#L14-L18](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L14-L18)）。
- **源码要求 `bezier` 或 `spring` 二选一**：先看 `bezier` 字段，不存在再看 `spring`，两者都没有直接报 `bezier or spring is required`；`spring` 名字会被内部转成 `spring:<name>`（[LuaBindingsConfigRules.cpp#L436-L463](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L436-L463)）。
- `style` 只有在非空时才会走合法性校验（[LuaBindingsConfigRules.cpp#L465-L484](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L465-L484)）。
- 全局开关是配置值 `animations:enabled`，默认 `true`（[ConfigValues.cpp#L348](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/values/ConfigValues.cpp#L348)）；示例配置写成 `hl.config({ animations = { enabled = true } })`（[example/hyprland.lua#L130-L133](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L130-L133)）。

**wiki 与源码不一致**：wiki 的语法行写的是 `curve = string`（[wiki#L10-L18](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L10-L18)），后面几个例子也写 `curve = "default"`（[wiki#L143-L161](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L143-L161)）；但 v0.57.2 的 `hl.animation` 只读取 `bezier` / `spring` 字段，并不存在 `curve` 字段（[LuaBindingsConfigRules.cpp#L436-L463](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L436-L463)）。按本报告约定，此处以源码为准：**应写 `bezier = "..."` 或 `spring = "..."`，不能写 `curve = "..."`**。

### 2.2 历史语法（v0.54.x，以及 v0.55–v0.56 的 legacy 回退）

v0.54.0 的 hyprlang 语法：

```ini
animations {
    enabled = yes
    bezier = snappy, 0.2, 0, 0.1, 1
    animation = windowsIn, 1, 4, snappy, popin 85%
    animation = windowsOut, 1, 2, linear, popin 85%
}
```

- `animation = NAME, ONOFF, SPEED, CURVE [,STYLE]`；`ONOFF` 为 0/1，`SPEED` 为 decisecond，`CURVE` 是已定义的 bezier 名（[0.54 wiki](https://wiki.hypr.land/0.54.0/Configuring/Animations/), [ConfigManager.cpp#L2437-L2492](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigManager.cpp#L2437-L2492)）。
- `bezier = NAME, X0, Y0, X1, Y1`，两个控制点可配置，端点固定为 (0,0)、(1,1)（[ConfigManager.cpp#L2400-L2434](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigManager.cpp#L2400-L2434), [0.54 wiki](https://wiki.hypr.land/0.54.0/Configuring/Animations/)）。
- `ONOFF = 0` 时会 `setConfigForNode(NAME, 0, 1, "default")` 并忽略后面的参数（[ConfigManager.cpp#L2455-L2458](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigManager.cpp#L2455-L2458)）。
- v0.54.0 的 `AnimationManager` 只有 bezier 取点逻辑，没有 spring 分支（[AnimationManager.cpp#L139-L140](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/managers/animation/AnimationManager.cpp#L139-L140)）。

版本边界（按源码路径确认）：

| 版本 | 主配置 | legacy `.conf` 回退 | spring |
| --- | --- | --- | --- |
| ≤ v0.54.x | hyprlang | — | 无 |
| v0.55.0–v0.56.2 | Lua `hyprland.lua` | 有：`.lua` 不存在时把扩展名换成 `.conf` 并创建 `Legacy::CConfigManager`（[v0.55.0 ConfigManager.cpp#L33-L40](https://github.com/hyprwm/Hyprland/blob/af923e30d1d24f1f4a4f5cb8308065173c1d9539/src/config/ConfigManager.cpp#L33-L40)） | 有（v0.55.0 已实现 `hl.curve` spring） |
| ≥ v0.57.0 | 仅 Lua（[v0.57.2 ConfigManager.cpp#L29-L35](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/ConfigManager.cpp#L29-L35)） | 无 | 有 |

### 2.3 动画树、默认值与继承

动画配置是一棵树，未显式设置的子节点继承父节点的 `pValues`（[hyprutils AnimationConfig.cpp#L10-L29](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimationConfig.cpp#L10-L29), [AnimationConfig.cpp#L61-L69](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimationConfig.cpp#L61-L69)）。v0.57.2 的树节点：

```
global
  ↳ windows - styles: slide, popin, gnomed
    ↳ windowsIn  - window open
    ↳ windowsOut - window close
    ↳ windowsMove - moving, dragging, resizing
  ↳ fade
    ↳ fadeIn / fadeOut / ...
  ↳ workspaces / layers / border / ...
```

源码建树位置见 [AnimationTree.cpp#L14-L59](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L14-L59)；wiki 的文字版在 [wiki#L28-L68](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L28-L68)。

**代码内置默认值**只有根节点被显式设置：

```cpp
setConfigForNode("global", 1, 8.f, "default");
setConfigForNode("__internal_fadeCTM", 1, 5.f, "linear");
setConfigForNode("borderangle", 0, 1, "default");
```

见 [AnimationTree.cpp#L61-L66](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L61-L66)。因此**没有任何用户配置时**，`windows`、`windowsIn`、`windowsOut`、`windowsMove` 都继承：`enabled = true`、`speed = 8`（800 ms）、`bezier = "default"`、`style = ""`。

`style = ""` 在窗口动画控制器里会落入 popin 回退，而 popin 的默认百分比是 0%，最小尺寸被 clamp 到 5×5（[WindowAnimationController.cpp#L18-L49](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L18-L49), [WindowAnimationController.cpp#L124-L150](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L124-L150)）。也就是说，**“内置默认 style”并不是 slide，而是空样式触发的 popin 0%（视觉上从 5×5 放大）**。

Hyprland 生成的默认配置文件则会显式覆盖这些叶子。v0.57.2 的示例配置（`DefaultConfig.hpp` 直接 `#embed` 了它，[DefaultConfig.hpp#L14-L18](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/DefaultConfig.hpp#L14-L18)）是：

| 叶子 | 生成配置中的值 |
| --- | --- |
| `windows` | `speed = 4.79`, `spring = "easy"`（[example#L147](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L147)） |
| `windowsIn` | `speed = 4.1`, `spring = "easy"`, `style = "popin 87%"`（[example#L148](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L148)） |
| `windowsOut` | `speed = 1.49`, `bezier = "linear"`, `style = "popin 87%"`（[example#L149](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L149)） |
| `windowsMove` | 未覆盖，继承 `windows` 的 `speed = 4.79` + spring `easy` |

v0.54.0 的示例默认值类似，但曲线是 bezier：`windowsIn = 1, 4.1, easeOutQuint, popin 87%`，`windowsOut = 1, 1.49, linear, popin 87%`（[example/hyprland.conf#L136-L142](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/example/hyprland.conf#L136-L142)）。

`animations:enabled = false` 时，`AnimationManager::tick()` 会给所有活动变量传 `warp = true`，直接跳到 goal（[AnimationManager.cpp#L274-L299](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L274-L299)）；窗口规则 `no_anim` 也会让该窗口的变量 warp（[AnimationManager.cpp#L127-L135](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L127-L135)）。

## 3. 打开动画（windowsIn）

### 3.1 从 map 到动画结束

1. `CWindow::mapWindow()` 开始处设置 `m_isMapped = true`、`m_presentation->setAnimatingIn(true)`（[Window.cpp#L1177-L1185](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1177-L1185)）。
2. 焦点在动画开始前就已设置：`fullWindowFocus(...FOCUS_REASON_NEW_WINDOW)`（[Window.cpp#L1519-L1525](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1519-L1525)）。
3. 布局完成一次 `layoutTarget()->recalc()`（[Window.cpp#L1578-L1581](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1578-L1581)）。
4. 设置动画配置：位置/尺寸用 `windowsIn`，淡入 alpha 用 `fadeIn`；然后 `applyAnimateIn()`（[Window.cpp#L1583-L1590](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1583-L1590), [WindowPresentation.cpp#L509-L511](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/WindowPresentation.cpp#L509-L511)）。
5. `applyAnimateIn()` 调用控制器的 `animateIn()` + `apply()`。`animateIn()` 以 goal 为默认 from/to，alpha 0→1，然后根据 style 改写 from；`apply()` 把当前值 warp 到 from，把 goal 设为 to（[WindowAnimationController.cpp#L156-L197](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L156-L197)）。
6. 位置和尺寸的 `setCallbackOnEnd(setVector2DAnimToMove)` 在动画结束时把配置切换为 `windowsMove`，并 `setAnimatingIn(false)`（[Window.cpp#L1589-L1590](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1589-L1590), [Window.cpp#L1144-L1157](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1144-L1157)）。

**窗口何时可见**：当前版本没有窗口级 `visible()` 标志；渲染条件是可映射且未隐藏（`!w->mapped() || isHidden()` 时跳过，[Renderer.cpp#L486-L496](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L486-L496)）。`m_isMapped` 在 map 开始即为 true，所以窗口从第一帧就会参与渲染，只是几何/alpha 处于动画起始值。

**动画期间是否接收输入**：源码中没有按 `animatingIn()` 屏蔽输入的逻辑；`animatingIn()` 的全部引用只有渲染器的两处（[Renderer.cpp#L276-L278](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L276-L278), [ElementRenderer.cpp#L118-L123](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/ElementRenderer.cpp#L118-L123)）。由于窗口已经 mapped 且已获得焦点，**只要窗口规则允许，打开动画期间就可以接收输入**。这里的“是否应该如此”是设计意图问题，源码只能确认“没有屏蔽”。

**动画结束后的落定**：位置/尺寸变量到达 goal，回调把动画配置从 `windowsIn` 换成 `windowsMove`；`animatingIn` 变 false；窗口保持 mapped/focused，不发生额外的 visible 状态切换。X11 override-redirect 窗口会跳过位移 style（[WindowAnimationController.cpp#L165-L167](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L165-L167)）。

### 3.2 windowsIn / windowsOut 可用 style

wiki 的动画树把窗口 style 列为 `slide, popin, gnomed`（[wiki#L33-L38](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L33-L38)）。源码校验器对 `window*` 接受以 `slide` 开头的字符串、`gnome`/`gnomed`、以 `popin` 开头的字符串（[AnimationManager.cpp#L392-L413](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L392-L413)），但实际渲染器的分派更窄：

| style | 视觉效果 | 参数/细节 | 源码 |
| --- | --- | --- | --- |
| `slide` | 尺寸不变，窗口从最近显示器边缘滑入（关闭时滑出） | 可选强制方向 `top` / `bottom` / `left` / `right`；无方向时按窗口中心到四条边的最近距离选边 | [WindowAnimationController.cpp#L67-L122](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L67-L122) |
| `popin [N%]` | 以窗口中心为基准缩放：打开从 `N%` 大小到 100%，关闭反向 | `N` 为整数百分比；起始尺寸 = `goalSize * N%`，下限 clamp 到 `5×5`；不写百分比等于 `0%`，因此从 5×5 开始；超过 100% 被上限 clamp 到 goal | [WindowAnimationController.cpp#L18-L49](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L18-L49), [wiki#L143-L148](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L143-L148) |
| `gnomed` / `gnome` | GNOME 风格：宽度保持，高度从 0 长到完整高度；位置在窗口底部边缘（关闭时压回一条水平线） | 无参数 | [WindowAnimationController.cpp#L52-L65](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L52-L65) |

需要特别区分：

- `slidevert` / `slidefade` / `slidefadevert` 是 **workspaces** 的 style，不是窗口 style（[wiki#L60-L65](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L60-L65), [AnimationManager.cpp#L414-L434](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L414-L434)）。
- `fade` 是 layers/workspaces 的 style；窗口透明度的入场/退场由独立的 `fadeIn` / `fadeOut` 配置驱动（[Window.cpp#L1584-L1587](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1584-L1587), [WindowFadeout.cpp#L88-L105](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.cpp#L88-L105)）。
- `loop` / `once` 只用于 `*angle` 动画（borderangle / shadowangle / glowangle），不是窗口 style（[wiki#L57-L59](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L57-L59), [AnimationManager.cpp#L435-L438](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L435-L438)）。
- 源码校验器因为用 `style.starts_with("slide")`，会放行 `slidevert` / `slidefade` 这类字符串；但渲染器只精确匹配 `slide`（[WindowAnimationController.cpp#L124-L150](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L124-L150)），所以这些字符串在窗口上不会得到“垂直 slide”，而会落入 popin 回退。这是校验器与渲染器的一处宽松/不一致点。

## 4. 关闭动画（windowsOut）

### 4.1 v0.57.2：快照淡出流程

1. 客户端 unmap 触发 `CWindow::unmapWindow()`。此时先从渲染器抓取窗口快照与工作区快照（`makeSnapshotFB`），若窗口规则 `no_anim` 或不需要渲染则快照为 null（[Window.cpp#L1649-L1652](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1649-L1652)）。
2. 布局 target 被移除（[Window.cpp#L1687](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1687)），随后 `m_isMapped = false`（[Window.cpp#L1691-L1692](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1691-L1692)）。从这一刻起实时窗口不再参与渲染和输入。
3. 为了让 `getStyle()` 返回 `windowsOut` 的 style，代码先把实时窗口的位置/尺寸/alpha 的 config 指向 `windowsOut` / `fadeOut`（[Window.cpp#L1748-L1751](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1748-L1751)；源码注释直接说明 “style is decided by the fuckin anim from this”）。
4. `CWindowFadeout::create()` 读取 `window->presentation().animateOut()` 的 from/to，创建三个动画变量：位置、尺寸用 `windowsOut`，alpha 用 `fadeOut`；把快照的 from warp 到当前几何/当前 alpha，把 goal 设为 style 目标几何/alpha 0（[WindowFadeout.cpp#L88-L105](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.cpp#L88-L105)）。
5. 快照被加入 `Desktop::fadingOutState()`（[Window.cpp#L1753](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1753)）。`CWindowFadeout` 只持有 monitor、framebuffer、动画变量等，不持有 `PHLWINDOW`（[WindowFadeout.hpp#L15-L31](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.hpp#L15-L31)），因此真实窗口对象可以先被销毁。
6. 快照的 `done()` 条件是：alpha == 0 且 alpha/位置/尺寸都不再处于动画中（[WindowFadeout.cpp#L143-L145](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.cpp#L143-L145)）。`CFadingOutState` 在每次 tick 后清理已完成的 fadeout（[FadingOutState.cpp#L10-L13](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/FadingOutState.cpp#L10-L13), [FadingOutState.cpp#L34-L41](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/FadingOutState.cpp#L34-L41)）。
7. 快照按平面（tiled / floating / over-fullscreen）插入渲染顺序（[Renderer.cpp#L380-L390](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L380-L390), [Renderer.cpp#L406-L408](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L406-L408)）。

**关闭 style 的效果**与打开对称：`animateOut()` 的 from 是当前几何，to 是 goal，然后 `applyWindowStyle(..., close=true)` 改写 `to`（[WindowAnimationController.cpp#L172-L186](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L172-L186)）。因此 popin 会缩到 `N%`、gnomed 会压扁高度到 0、slide 会滑向显示器边缘。

**动画期间窗口是否仍渲染**：实时窗口在 `m_isMapped = false` 后不再渲染（[Renderer.cpp#L486-L496](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/render/Renderer.cpp#L486-L496)）；仍在屏幕上的是 `CWindowFadeout` 的快照。**输入**也在 `m_isMapped = false` 后停止，因为窗口已从布局移除且不再 mapped。

**结束后如何销毁**：快照由 `CFadingOutState` 在 alpha 0 且动画结束时移除；`CWindow` 的生命周期与快照解耦，客户端 unmap/destroy 驱动 `destroyWindow()`，后者把窗口从 `Desktop::windowState()` 移除（[Window.cpp#L1825-L1843](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1825-L1843)）。因此**关闭动画不再延迟合成器侧的对象销毁，也不把窗口对象钉在动画时长上**。

### 4.2 `close_special_on_empty` 的影响

`misc:close_special_on_empty` 默认 `true`（[ConfigValues.cpp#L590](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/values/ConfigValues.cpp#L590)）。`onUnmap()` 中，如果 special workspace 已经没有窗口，会把 `m_lastWorkspaceAddress` / `m_lastWorkspaceSpecial` 改到主 workspace，以免 special workspace 被关闭后快照无法通过渲染检查；随后如果配置为 true 且当前 special workspace 为空，则关闭 special workspace（[Window.cpp#L511-L528](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L511-L528)）。这解释了“关闭 special workspace 最后一个窗口时，关闭动画仍然可见”的源码路径。

### 4.3 历史对比：v0.54 把窗口留在 fading-out 列表

v0.54.0 没有快照 fadeout；它把窗口对象本身保留：

- `unmapWindow()` 中 `m_fadingOut = true`，加入 `m_windowsFadingOut`，再启动 `ANIMATION_TYPE_OUT`（[Window.cpp#L2264-L2272](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/desktop/view/Window.cpp#L2264-L2272)）。
- `visible()` 在 `m_fadingOut && m_alpha->value() != 0` 时仍返回 true（[Window.cpp#L174-L175](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/desktop/view/Window.cpp#L174-L175)），渲染器也会继续绘制 fading-out 窗口（例如 [Renderer.cpp#L482](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/render/Renderer.cpp#L482)）。
- `m_isMapped = false` 仍然在 unmap 早期设置（[Window.cpp#L2217-L2218](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/desktop/view/Window.cpp#L2217-L2218)），所以输入停止；但窗口对象保留到 alpha 0。
- `CCompositor::cleanupFadingOut()` 在 `!m_fadingOut || alpha == 0` 时把 `m_fadingOut` 置 false，并在 `m_readyToDelete` 时 `removeWindowFromVectorSafe`（[Compositor.cpp#L1286-L1303](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/Compositor.cpp#L1286-L1303)）。这是合成器内部的对象生命周期，不等同于 Wayland 协议层的 close 通知时序。

v0.54 的 style 实现在 `CDesktopAnimationManager`，与 v0.57.2 的 `CWindowAnimationController` 逻辑基本同构：popin 同样 clamp 到 5×5（[DesktopAnimationManager.cpp#L368-L379](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/managers/animation/DesktopAnimationManager.cpp#L368-L379)），slide/gnomed 的几何也一致（[DesktopAnimationManager.cpp#L381-L453](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/managers/animation/DesktopAnimationManager.cpp#L381-L453)）。

## 5. resize 动画

### 5.1 作用在哪些变量上

窗口几何由两个 `AnimatedVariable<Vector2D>` 驱动：`m_realPosition` 与 `m_realSize`，它们定义在 `CGeometricMovableAnimated` 中（[GeometricMovableAnimated.hpp#L10-L24](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/types/GeometricMovableAnimated.hpp#L10-L24)）；`CAnimatedVariable` 是 hyprutils 的 `CGenericAnimatedVariable<VarType, SAnimationContext>` 别名（[AnimatedVariable.hpp#L68-L75](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/helpers/AnimatedVariable.hpp#L68-L75)）。

几何目标通过赋值设置：

```cpp
void CGeometricMovableAnimated::move(const Vector2D& x)   { *m_realPosition = x; }
void CGeometricMovableAnimated::resize(const Vector2D& x) { *m_realSize = x; }
void CGeometricMovableAnimated::setBox(const CBox& x) {
    *m_realPosition = x.pos();
    *m_realSize     = x.size();
}
```

见 [GeometricMovableAnimated.cpp#L35-L46](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/types/GeometricMovableAnimated.cpp#L35-L46)。赋值触发 `onAnimationBegin()`，之后每个 tick 按 `value = begun + (goal - begun) * curveValue` 插值（[AnimatedVariable.hpp#L272-L292](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/include/hyprutils/animation/AnimatedVariable.hpp#L272-L292)）。

### 5.2 用哪个动画配置

**所有普通几何变化共用 `windowsMove`**：

- 打开动画结束时，回调 `setVector2DAnimToMove` 把位置/尺寸的 config 从 `windowsIn` 切换为 `windowsMove`（[Window.cpp#L1144-L1157](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1144-L1157)）。
- 窗口移动到其他 workspace 时，`CWindowPresentation::setAnimationsToMove()` 也把位置动画设为 `windowsMove`（[WindowPresentation.cpp#L418-L421](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/WindowPresentation.cpp#L418-L421)）。
- 动画树中只有 `windowsIn` / `windowsOut` / `windowsMove` 三个窗口叶子，没有 `windowsResize` 之类的节点（[AnimationTree.cpp#L34-L37](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L34-L37)）；wiki 也把 `windowsMove` 描述为 “everything in between, moving, dragging, resizing”（[wiki#L36-L38](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L36-L38)）。
- `windowsMove` 的 `style` 不会被消费：窗口控制器只在 `animateIn()` / `animateOut()` 里读取 `positionAnimation()->getStyle()`（[WindowAnimationController.cpp#L124-L150](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L124-L150), [WindowAnimationController.cpp#L156-L186](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L156-L186)）；几何移动路径不应用 popin/slide 等 style。因此给 `windowsMove` 写 style 没有视觉效果。

**没有独立的 resize 曲线/速度**：`windowsMove` 有自己的 `speed` 与 `enabled`，但它同时覆盖移动、拖拽、布局重排、全屏/浮动切换。想只改 resize 手感是做不到的；只能改 `windowsMove` 并接受它同时影响所有几何变化。

### 5.3 不同 resize 路径

| 场景 | 源码路径 | 是否走窗口几何动画 |
| --- | --- | --- |
| 平铺布局重排（layout recalculate） | Dwindle `recalcSizePosRecursive` → `pTarget->setPositionGlobal(box)`（[DwindleAlgorithm.cpp#L28-L60](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/algorithm/tiled/dwindle/DwindleAlgorithm.cpp#L28-L60)）→ `CWindowTarget::updatePos` → `m_window->setBox(wb)`（[WindowTarget.cpp#L250-L257](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/target/WindowTarget.cpp#L250-L257)） | 是，目标值变化由当前配置（通常是 `windowsMove`）动画到新 goal |
| 浮动窗口被布局设置几何 | `CWindowTarget::updatePos` 的 floating 分支直接 `m_window->setBox(m_box.logicalBox)`（[WindowTarget.cpp#L81-L89](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/target/WindowTarget.cpp#L81-L89)） | 是，除非随后被显式 warp |
| 鼠标拖拽移动/浮动 resize | `DragController` 设置位置/尺寸后立刻 `warpPositionSize()`（[DragController.cpp#L401-L412](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/supplementary/DragController.cpp#L401-L412), [DragController.cpp#L468-L475](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/supplementary/DragController.cpp#L468-L475)） | 否：`warpPositionSize()` → `finishAnimation()` → 位置/尺寸 `warp()`（[WindowTarget.cpp#L477-L479](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/target/WindowTarget.cpp#L477-L479), [GeometricMovableAnimated.cpp#L48-L50](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/types/GeometricMovableAnimated.cpp#L48-L50)），窗口跟手 |
| 鼠标拖拽平铺 resize | `DragController` 调 `resizeTarget()` 后再 `warpPositionSize()`；Dwindle 在目标等于拖拽 target 时还会把该布局所有窗口 `warpPositionSize()`（[DwindleAlgorithm.cpp#L470-L478](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/algorithm/tiled/dwindle/DwindleAlgorithm.cpp#L470-L478)） | 否：显式 warp |
| 键盘 `resizeactive` / `moveactive` | `Actions::resize()` / `Actions::move()` → `g_layoutManager->resizeTarget/moveTarget`（[ConfigActions.cpp#L681-L713](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/actions/ConfigActions.cpp#L681-L713)）→ 空间/算法 → `setPositionGlobal` / `setBox` | 通常会动画；没有拖拽时的显式 warp |
| 全屏 / 最大化切换 | `IFullscreenHandler::setTargetSizeAndPosition()` 对全屏用 `setPositionGlobal(MONBOX)`，最大化用 `setPositionGlobal(WORKAREA)`（[FullscreenHandler.cpp#L207-L214](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/managers/fullscreen/handler/FullscreenHandler.cpp#L207-L214)）→ `setBox` | 是，使用当前 `windowsMove` 配置；没有专门的全屏动画叶子 |
| 初次 map 时就带全屏请求 | `mapWindow()` 在应用 fullscreen 状态前 `m_realPosition->warp(); m_realSize->warp();`（[Window.cpp#L1540-L1548](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1540-L1548)） | 否：先 warp，避免从旧几何飞入 |
| 浮动/平铺切换 | `setFloating()` 改变 target 状态后由布局重算，最终仍走 `setBox`（[WindowTarget.cpp#L282-L297](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/target/WindowTarget.cpp#L282-L297)） | 是，使用 `windowsMove` |

### 5.4 `misc:animate_manual_resizes` 与 `misc:animate_mouse_windowdragging`

两者默认都是 `false`：

- `misc:animate_manual_resizes`：描述为 “If true, will animate manual window resizes/moves”（[ConfigValues.cpp#L578](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/values/ConfigValues.cpp#L578)）。
- `misc:animate_mouse_windowdragging`：描述为 “If true, will animate windows being dragged by mouse.”（[ConfigValues.cpp#L579](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/values/ConfigValues.cpp#L579)）。

源码事实：

- `animate_manual_resizes` 在 v0.57.2 全仓库的唯一读取点是 `CDwindleAlgorithm::resizeTarget()`（[DwindleAlgorithm.cpp#L312-L321](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/algorithm/tiled/dwindle/DwindleAlgorithm.cpp#L312-L321)）。它把 `*PANIMATE == 0` 作为 `force` 传给 `recalcSizePosRecursive()`（例如 [DwindleAlgorithm.cpp#L375](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/algorithm/tiled/dwindle/DwindleAlgorithm.cpp#L375)）。但在当前实现里，`recalcSizePosRecursive(bool force, ...)` 只把 `force` 继续传给子节点，最终仍然只是 `pTarget->setPositionGlobal(box)`，没有用 `force` 去 `warp` 或切换“是否动画”（[DwindleAlgorithm.cpp#L28-L60](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/algorithm/tiled/dwindle/DwindleAlgorithm.cpp#L28-L60)）。
- 鼠标拖拽路径又会显式 `warpPositionSize()`（[DragController.cpp#L468-L475](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/supplementary/DragController.cpp#L468-L475)），因此从源码看，**鼠标 resize 是跟手的、不播放 `windowsMove` 动画**，这个行为不因 `animate_manual_resizes` 改变。
- `animate_mouse_windowdragging` 在 v0.57.2 与 v0.54.0 中都没有配置声明之外的读取点（对 `src/` 全树搜索；v0.54 只有 [ConfigDescriptions.hpp#L1219-L1224](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigDescriptions.hpp#L1219-L1224) 与注册代码，v0.57.2 只有 [ConfigValues.cpp#L579](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/values/ConfigValues.cpp#L579)）。按源码判断它是遗留/无效项；历史用途未确认。

结论：**不要指望 `misc:animate_manual_resizes = true` 在 v0.57.2 中把鼠标拖拽 resize 变成有追随动画**；从源码看，鼠标拖拽仍是显式 warp。键盘 resize 因为没有拖拽 target，通常会走 `windowsMove` 动画，但 `force` 参数当前没有被有效利用。这一点更像实现遗留或回归，是否会在未来版本修复未确认。

## 6. 曲线与缓动

### 6.1 Bezier

当前 Lua 语法：

```lua
hl.curve("NAME", { type = "bezier", points = { {X0, Y0}, {X1, Y1} } })
```

- 端点固定为 (0,0) 与 (1,1)，中间两个控制点由 `points` 给出（[BezierCurve.cpp#L11-L17](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/BezierCurve.cpp#L11-L17)）。
- 当前解析器要求 `points` 正好 2 个点、每个点 2 个数字，每个坐标接受 -1..2 的 float（[LuaBindingsConfigRules.cpp#L303-L341](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L303-L341)）。历史 hyprlang 的 `bezier = NAME, X0, Y0, X1, Y1` 没有这个范围限制（[ConfigManager.cpp#L2400-L2434](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigManager.cpp#L2400-L2434)）。
- 求值方式：按时间占比 x 在预烘焙的 256 个点上做二分/线性插值，返回 y（[BezierCurve.cpp#L33-L37](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/BezierCurve.cpp#L33-L37), [BezierCurve.cpp#L55-L100](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/BezierCurve.cpp#L55-L100)）。

**内置曲线只有两个**：

- `default`：控制点 `(0.0, 0.75)`、`(0.15, 1.0)`（[hyprutils AnimationManager.cpp#L13-L21](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimationManager.cpp#L13-L21)）。
- `linear`：Hyprland 在构造 `CHyprAnimationManager` 时注册 `(0,0)`、`(1,1)`（[AnimationManager.cpp#L36-L42](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L36-L42)）。

`easeIn` / `easeOut` / `easeInOut` 等**不是内置曲线**；v0.57.2 的示例配置显式定义了 `easeOutQuint`、`easeInOutCubic`、`linear`、`almostLinear`、`quick`（[example/hyprland.lua#L135-L140](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L135-L140)）。v0.54 的示例同样定义这些 bezier（[example/hyprland.conf#L128-L134](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/example/hyprland.conf#L128-L134)）。

### 6.2 Spring

当前 Lua 语法：

```lua
hl.curve("NAME", { type = "spring", mass = 1, stiffness = 200, damping = 24 })
```

- 参数：`mass`、`stiffness`、`damping`；当前解析器要求三者都 >= 0.5（[LuaBindingsConfigRules.cpp#L342-L395](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L342-L395)）。`damping` 缺失时会回退读取拼写错误的历史字段 `dampening`（[LuaBindingsConfigRules.cpp#L360-L372](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L360-L372)）。
- 默认 spring 结构体：`stiffness = 250`、`damping = 25`、`mass = 1`、`valueEpsilon = 0.001`、`velocityEpsilon = 0.001`（[hyprutils AnimationManager.hpp#L16-L22](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/include/hyprutils/animation/AnimationManager.hpp#L16-L22)）。
- 物理推进：`OMEGA0 = sqrt(stiffness / mass)`，`GAMMA = damping / (2 * mass)`；分 underdamped（`GAMMA < OMEGA0`，会振荡）、临界、overdamped 三种解析解逐帧推进（[Spring.cpp#L9-L56](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/Spring.cpp#L9-L56)）。
- 结束条件不是固定时长，而是同时满足 `|1 - value| <= valueEpsilon` 与 `|velocity| <= velocityEpsilon`（[AnimatedVariable.cpp#L120-L137](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L120-L137)）。
- **`speed` 对 spring 不生效**：spring 分支完全不调用 `getPercent()`，hyprutils 全仓库对 `internalSpeed` 的唯一读取也在 `getPercent()` 里（[AnimatedVariable.cpp#L74-L81](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L74-L81)）。但 `hl.animation` 仍然要求 `speed > 0`（[LuaBindingsConfigRules.cpp#L426-L434](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L426-L434)），所以 spring 配置里的 `speed` 是一个“必填但被忽略”的字段。示例配置里 `windowsIn` 写 `speed = 4.1, spring = "easy"`，实际时长由 spring 决定（[example#L148](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/example/hyprland.lua#L148)）。

### 6.3 speed 与 curve 如何配合

对 bezier：

```
percent = clamp((elapsed_ms / 100) / speed, 0, 1)
value   = begun + (goal - begun) * bezier_y(percent)
```

- `speed` 是总时长（decisecond），不是“倍率”；`speed = 8` 表示 800 ms，数值越大越慢。公式见 [AnimatedVariable.cpp#L74-L81](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L74-L81)，wiki 的单位说明见 [wiki#L16](https://github.com/hyprwm/hyprland-wiki/blob/8a914cdfce28b332961778816c967467afd76a18/content/configuring/core/animations.md#L16)。
- `percent >= 1` 时直接返回 1（完成），`AnimationManager` 随后 warp 到 goal（[AnimatedVariable.cpp#L105-L117](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L105-L117)）。
- 对 spring，曲线值来自物理状态，不使用上述 `percent`/`speed`。

## 7. 实现要点：tick、结束回调与关闭/销毁关系

### 7.1 动画 tick

- `CHyprAnimationManager` 构造时创建一个 **500 µs** 的 `CEventLoopTimer`（[AnimationManager.cpp#L36-L39](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L36-L39)）。
- `frameTick()` 在每次 timer 回调里检查是否还有活动变量；实际 `tick()` 被限制为最多约每 1 ms 一次，之后触发 `Event::bus()->m_events.tick`（[AnimationManager.cpp#L339-L360](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L339-L360)）。
- `tick()` 收集活动变量、按 owner 批量 damage、对每个变量调用 `handleUpdate`，最后 `tickDone()` 清理已结束的变量（[AnimationManager.cpp#L153-L336](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L153-L336), [hyprutils AnimationManager.cpp#L68-L86](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimationManager.cpp#L68-L86)）。
- `updateVariable()` 对 Vector2D 做 `begun + delta * POINTY` 插值；`POINTY` 来自 `getCurveStep()`（[AnimationManager.cpp#L44-L55](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L44-L55), [AnimationManager.cpp#L140-L150](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/animation/AnimationManager.cpp#L140-L150)）。

### 7.2 动画结束回调

- `setCallbackOnEnd(func)` 注册结束回调；`onAnimationEnd()` 会 swap 取出回调、执行，并按 `remove` 标志决定是否保留（[hyprutils AnimatedVariable.cpp#L153-L190](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L153-L190)）。
- 打开动画的两个几何变量注册的是同一个 `setVector2DAnimToMove`：动画结束 → config 换成 `windowsMove` → `setAnimatingIn(false)`（[Window.cpp#L1589-L1590](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1589-L1590), [Window.cpp#L1144-L1157](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1144-L1157)）。
- 关闭快照的结束不是靠回调销毁，而是 `FadingOutState` 每 tick 检查 `done()` 后 `erase_if`（[FadingOutState.cpp#L10-L41](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/FadingOutState.cpp#L10-L41)）。

### 7.3 窗口 open/close 的真实“hook”叫什么

源码中没有 `onWindowOpen` / `onWindowClose` 这种函数；对应的是：

- `CWindow::mapWindow()` / `CWindow::unmapWindow()` / `CWindow::destroyWindow()`（[Window.cpp#L1160](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1160), [Window.cpp#L1613](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1613), [Window.cpp#L1825](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1825)）。
- EventBus 事件：`window.openEarly`、`window.open`、`window.openLate`、`window.close`（[Window.cpp#L1444](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1444), [Window.cpp#L1508](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1508), [Window.cpp#L1609](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1609), [Window.cpp#L1632](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1632)）。

v0.54 的对应实现使用 `CDesktopAnimationManager::startAnimation(window, ANIMATION_TYPE_IN/OUT)`（[DesktopAnimationManager.cpp#L15-L92](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/managers/animation/DesktopAnimationManager.cpp#L15-L92)），在 v0.57.2 中这一职责被 `CWindowAnimationController` 与 `CWindowFadeout` 分担。

## 8. 实用配置示例

以下均为 **v0.57.2 的 Lua 语法**。`speed` 单位为 decisecond；`bezier`/`spring` 二选一；`style` 可省略。

### 8.1 只改打开/关闭，保留其余默认

```lua
hl.curve("snappy", { type = "bezier", points = { {0.2, 0}, {0.1, 1} } })

hl.animation({ leaf = "windowsIn",  enabled = true, speed = 4, bezier = "snappy", style = "popin 85%" })
hl.animation({ leaf = "windowsOut", enabled = true, speed = 2, bezier = "linear", style = "popin 85%" })

hl.animation({ leaf = "fadeIn",  enabled = true, speed = 2, bezier = "snappy" })
hl.animation({ leaf = "fadeOut", enabled = true, speed = 2, bezier = "linear" })
```

依据：`windowsIn`/`windowsOut` 是独立叶子（[AnimationTree.cpp#L34-L37](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L34-L37)）；popin 百分比语义见 [WindowAnimationController.cpp#L37-L49](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/animationControllers/WindowAnimationController.cpp#L37-L49)；fadeIn/fadeOut 控制 alpha（[Window.cpp#L1584-L1587](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/view/window/Window.cpp#L1584-L1587), [WindowFadeout.cpp#L88-L105](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/desktop/state/WindowFadeout.cpp#L88-L105)）。

### 8.2 只改 resize/几何手感

```lua
hl.curve("snappy", { type = "bezier", points = { {0.2, 0}, {0.1, 1} } })
hl.animation({ leaf = "windowsMove", enabled = true, speed = 2, bezier = "snappy" })
```

注意：这会同时影响布局重排、移动、全屏/浮动切换等所有几何变化；鼠标拖拽 resize 仍会被 `warpPositionSize()` 立即对齐（见 §5.3），所以主要能感知到的是布局重排、键盘 move/resize、全屏/浮动切换。依据：`windowsMove` 是唯一几何叶子（[AnimationTree.cpp#L37](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/shared/animation/AnimationTree.cpp#L37)），鼠标/拖拽 warp 见 [DragController.cpp#L468-L475](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/layout/supplementary/DragController.cpp#L468-L475)。

### 8.3 关闭更快

```lua
hl.animation({ leaf = "windowsOut", enabled = true, speed = 1, bezier = "linear", style = "popin 90%" })
hl.animation({ leaf = "fadeOut",    enabled = true, speed = 1, bezier = "linear" })
```

`speed = 1` 即 100 ms；`windowsOut` 与 `fadeOut` 是两个独立叶子，需要同时改。依据同 §4.1 / §6.3。

### 8.4 spring 打开（带回弹）

```lua
hl.curve("soft", { type = "spring", mass = 1, stiffness = 200, damping = 24 })
hl.animation({ leaf = "windowsIn", enabled = true, speed = 1, spring = "soft", style = "popin 85%" })
```

`speed = 1` 是必填但会被 spring 忽略；实际时长由 stiffness/damping 决定。依据：spring 解析要求 `speed > 0`（[LuaBindingsConfigRules.cpp#L426-L434](https://github.com/hyprwm/Hyprland/blob/11cbc6c577b84ffb1edf4df3fc5b6128c3ae6947/src/config/lua/bindings/LuaBindingsConfigRules.cpp#L426-L434)），spring 推进不使用 speed（[AnimatedVariable.cpp#L120-L137](https://github.com/hyprwm/hyprutils/blob/7e1f69209cd910615d0573a93fa970408f643539/src/animation/AnimatedVariable.cpp#L120-L137)）。

### 8.5 旧版（≤ v0.56，hyprlang / legacy）

```ini
animations {
    enabled = yes
    bezier = snappy, 0.2, 0, 0.1, 1
    animation = windowsIn,  1, 4, snappy, popin 85%
    animation = windowsOut, 1, 2, linear, popin 85%
    animation = windowsMove, 1, 2, snappy
}
```

依据：旧语法与字段顺序见 [ConfigManager.cpp#L2400-L2492](https://github.com/hyprwm/Hyprland/blob/0002f148c9a4fe421a9d33c0faa5528cdc411e62/src/config/ConfigManager.cpp#L2400-L2492) 和 [0.54 wiki](https://wiki.hypr.land/0.54.0/Configuring/Animations/)。`windowsMove` 上的 style 即使写了也不会被消费。

## 9. 未确认 / 待核实

1. **`misc:animate_mouse_windowdragging` 的实际作用**：v0.54.0 与 v0.57.2 源码中除配置声明/注册外没有读取点。判断为遗留或无效项，但其历史用途、是否由插件读取未确认。
2. **`misc:animate_manual_resizes` 的实际有效性**：当前唯一读取点把值作为 `force` 传入 `recalcSizePosRecursive()`，而该参数没有改变最终 `setPositionGlobal()` 行为；鼠标拖拽又在 `DragController` 中显式 warp。是否属于重构后的回归、还是本来只对键盘 resize 有意义，未确认。
3. **`first_launch_animation`**：在 v0.54.0、v0.55.0、v0.57.2 的源码与示例配置中均未找到该名字。它可能属于更早版本、插件或非官方分支；本报告无法确认其存在与语义。
4. **`worst` style**：在 v0.54.0 与 v0.57.2 全树搜索没有结果；无法确认它是否来自旧版本、fork 或用户自定义配置。
5. **wiki 与源码不一致**：当前 wiki 的 `hl.animation` 签名和若干示例使用 `curve = "..."`，但 v0.57.2 源码只接受 `bezier` / `spring`。本报告以源码为准；wiki 这些示例是否会在后续版本修复、或是否为文档笔误，未确认。
6. **主分支差异**：本报告固定在 v0.57.2 tag；`main` 已前进到 `423da969...`（2026-11-22），未逐文件审计 main 与 tag 的动画实现差异。
7. **打开动画期间的输入策略**：源码确认没有 `animatingIn` 输入屏蔽，但“是否应该屏蔽”属于设计意图，未从文档或 issue 中核实。
8. **`windowsMove` 的 style 被校验器接受但不被应用**：这是源码观察；是否未来会支持 move/resize style 未确认。
9. **运行时行为**：本机为 macOS，无法运行 Hyprland；所有结论来自源码，未做实际合成器行为验证（例如 spring 过冲时的 actual damage/输入命中、快照与多显示器边界情况）。

# Interface Style is a second axis beside the color preset

The interface has two independent appearance axes. The **color preset** (the
124 light and dark presets plus the accent) decides colors only. The
**Interface Style** decides shapes and materials: radii, control and bar
heights, the type scale, glass and vibrancy, and the layout recipes of the
window shells. Classic is the original's look; macOS is the macOS 26/27
Liquid Glass look. Any preset combines with either style.

`ui/kit` and `ui/layout` keep one API for every surface. Each component asks
the active style for its recipe, so feature packages name a component and a
role and never write sizes or colors. MyGo has no context values beside
`Context.Theme`, so the active style is a process value in `kit` that the app
sets before each frame from the Appearance setting, the way the window theme
is already resolved per frame. The setting is app-wide, so one value per
process is enough; tests set it explicitly.

**Status**: accepted

**Considered Options**:
- New color presets for the macOS look: colors cannot express radii,
  materials, or layout, and every preset would need a macOS twin.
- Passing the style through every view function: hundreds of signatures
  change for a value that never differs within a process.
- Encoding the style in `ui.Theme` (e.g. through `Radius`): MyGo's widgets
  read those fields, so the style could not change kit's layout recipes, and
  the encoding would be implicit.

**Consequences**:
- `Appearance.InterfaceStyle` is device-local, as `AppIcon` and the Vault's
  view mode are: `Portable` strips it and `MergePortable` keeps the local
  value. A fresh profile starts in the macOS style on darwin and Classic
  elsewhere.
- MyGo sets `TrafficLightPosition` and `Transparent` only when a window is
  created. Both styles therefore keep the 36 DIP title bar
  (`titlebar.Height`), and windows are created `Transparent` in both: Classic
  paints an opaque root, and macOS clears it when `Context.Vibrancy` reports
  a material. Switching style never recreates a window.
- Linux has no window vibrancy; the macOS style paints a solid background
  there. `plugins/glass` draws the same on every renderer.
- Classic must look the same after the shared components land; the visual
  baseline guards it.

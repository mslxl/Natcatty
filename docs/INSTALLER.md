## Building installers (Electron Builder)

This project uses `electron-builder` and `electron-builder.json`.

### Windows (NSIS .exe installer)

- Build: `npm run pack:win`
- Output: `release/` (look for `Netcatty-<version>-win-x64.exe`)

If you also want the unpacked folder build:

- Build: `npm run pack:dir`
- Output: `release/win-unpacked/`

### macOS (DMG)

- Build: `npm run pack:mac`
- Output: `release/` (dmg/zip + unpacked dir)

### Linux (AppImage + deb)

- Build: `npm run pack:linux`
- Output: `release/` (AppImage/deb + unpacked dir)


# Upstream

This package is natcatty's own terminal, forked from MyGo's terminal plugin so
every platform can link the same core.

- Upstream: `github.com/egoist/mygo`, `plugins/terminal`
- Source version: MyGo v0.10.0 (the version the mobile plan targets). natcatty
  itself still pins MyGo v0.9.2; the two terminal trees differ only in the PTY
  backend and the FFI build tags, and the PTY backend is deleted here.
- Bound library: libghostty-vt ABI `0.1.0-dev`, Ghostty commit
  `befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028`.
- Manifest: `native/mygo-plugin.json` keeps MyGo's released desktop builds for
  that commit. Re-hosting them later is a manifest-only change.

## Layout

```
ui/terminal/                 shared core: grid, view, paint, keys, links,
                             search, images, shell integration
  internal/vt/               the libghostty-vt binding
  native/                    !ios && !android: plugin manifest + run-time loader
  load.go                    !ios && !android: loads through native
  load_mobile.go             ios || android: no loader; reports unsupported
                             until the static backend lands
```

`native` is the only directory in the module with a `mygo-plugin.json`, and
its build tag keeps it (and the manifest) out of the phone module graph by
construction: `go list -deps` never lists it for `ios` or `android`.
`internal/vt`'s FFI files carry the same asymmetric tags: the purego
`Dlopen`/`Dlsym` open and symbol lookup build on desktop only, and phones
select the `Supported = false` stubs. The purego call mechanism is otherwise
shared.

## Deltas from upstream

1. Import path `github.com/egoist/mygo/plugins/terminal` becomes
   `github.com/mslxl/natcatty/ui/terminal`; `internal/vt` moves under it.
2. The local program path is deleted: `internal/pty`, `Options.Command/Dir/Env`,
   the launch timer, `start`, `command`, `environment`, the zsh `ZDOTDIR`
   staging, `resetLeftModes`, and every `*pty.PTY` branch. `Options.Conn` is
   required and `New` returns an error without it. natcatty only ever opens
   remote Sessions, so this removes code it never exercised.
3. Library lookup is split. The root package keeps `Load` and `LibraryPath`;
   `native.LibraryPath` finds or downloads the dylib, and `load.go` /
   `load_mobile.go` dispatch by build tag. The shared core has no discovery,
   download or manifest code.
4. The eight localized strings the view shows now live in natcatty's catalog
   (`terminalClearTerminal`, `terminalClipboardRead`, and the rest) and are
   called through `ui/locales`.
5. FFI tags follow v0.10.0, so the desktop loader is absent from phone builds
   rather than present and disabled at run time.
6. Tests: `TestQuotePaths`, the render/selection/search/link/mirror suites and
   the `internal/vt` suite are copied. The two suites that ran local programs
   are ported: their shared helpers moved to `helpers_test.go`, the
   local-command cases are dropped, and MyGo's internal wait helper is copied
   in. `internal/vt`'s helper takes the library from `NATCATTY_GHOSTTY_VT`
   instead of reading the manifest.

## Syncing

One upstream sync is one PR. Keep the golden terminal tests unchanged while
syncing; a test that has to change is a behavior change and belongs in its own
commit with a note here.

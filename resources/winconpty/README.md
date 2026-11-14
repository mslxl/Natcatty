# Windows ConPTY passthrough assets

The release build downloads these files from the pinned Microsoft Windows
Terminal ConPTY package. The DLL and executable are intentionally ignored by
Git and are not stored in the source tree.

- Package:
  `Microsoft.Windows.Console.ConPTY.1.24.260710001.nupkg`
- Source:
  `https://github.com/microsoft/terminal/releases/download/v1.24.11911.0/Microsoft.Windows.Console.ConPTY.1.24.260710001.nupkg`
- Package SHA-256:
  `9382AD7BECB7E4D84E300578D8E4F4DF28F43D979D9055D978C42913C47E0E9D`
- `conpty.dll` SHA-256:
  `39FBA2713E2495117B1591AE8C32A3B904BEA7AA66069CF7815E2844C76D75D8`
- `OpenConsole.exe` SHA-256:
  `B7FD936C2668B87B9ECF7B3366DC6568AFC1C6F981874CBA3E955A1C35CF8160`

Run `nu scripts/conptyfetch.nu --out resources/winconpty` to fetch
the pair locally. `platform/pty` loads it from the executable directory,
`resources/winconpty`, or `NATCATTY_CONPTY_DIR`; if it is unavailable, Windows'
system ConPTY remains the fallback.

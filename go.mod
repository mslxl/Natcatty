module github.com/mslxl/natcatty

go 1.27.3

// In-process network provider dependencies. Both versions are pinned exactly;
// do not follow upstream main. tailscale.com v1.104.5 requires Go >= 1.27.3.
//
// Upgrading either module requires a fresh dependency audit before the change:
// CGO_ENABLED=0 cross-builds for windows/amd64, linux/amd64, linux/arm64,
// darwin/amd64, and darwin/arm64; the five-target minimal import probe in
// internal/netprobe; an updated resources/THIRD-PARTY-NOTICES.md; and a
// passing govulncheck run. wireguard-go's
// tun/netstack ambiguity note and the replace directive below must be
// re-checked against the new module graph.
require (
	github.com/egoist/mygo v0.6.0
	github.com/glebarez/sqlite v1.11.0
	github.com/kayrus/putty v1.0.5
	github.com/pkg/sftp v1.13.11
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.46.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee // in-process WireGuard Net (tun/netstack)
	google.golang.org/protobuf v1.36.12
	gorm.io/gorm v1.31.1
	tailscale.com v1.104.5 // in-process Tailscale Net (tsnet)
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/akutz/memconn v0.1.0 // indirect
	github.com/alexbrainman/sspi v0.0.0-20250919150558-7d374ff0d59e // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/creachadair/msync v0.10.1 // indirect
	github.com/dblohm7/wingoes v0.0.0-20260526185140-fb298caac7ca // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.3 // indirect
	github.com/gaissmai/bart v0.29.0 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hdevalence/ed25519consensus v0.2.0 // indirect
	github.com/huin/goupnp v1.3.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jsimonetti/rtnetlink v1.4.2 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mdlayher/netlink v1.11.2 // indirect
	github.com/mdlayher/socket v0.7.0 // indirect
	github.com/mitchellh/go-ps v1.0.0 // indirect
	github.com/pires/go-proxyproto v0.15.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/safchain/ethtool v0.7.0 // indirect
	github.com/tailscale/certstore v0.1.1-0.20260409135935-3638fb84b77d // indirect
	github.com/tailscale/go-winio v0.0.0-20231025203758-c4f33415bf55 // indirect
	github.com/tailscale/hujson v0.0.0-20260727124030-b80ff77dac4f // indirect
	github.com/tailscale/peercred v0.0.0-20250107143737-35a0c7bd7edc // indirect
	github.com/tailscale/web-client-prebuilt v0.0.0-20260917222731-e0ed2d0d0fea // indirect
	github.com/tailscale/wireguard-go v0.0.0-20260928213032-417aef361226 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go4.org/mem v0.0.0-20240501181205-ae6ca9944745 // indirect
	go4.org/netipx v0.0.0-20260823151212-3075585bcbeb // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	golang.zx2c4.com/wireguard/windows v1.0.1 // indirect
	gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

// golang.zx2c4.com/wireguard/tun/netstack still exists as a standalone legacy
// module and provides the same import path as the pinned main module, so
// `go mod tidy` fails with "ambiguous import". This replace forces the package
// to resolve from the pinned main module. Remove it only after upstream stops
// publishing the standalone module and the module graph no longer selects it;
// verify the removal with `go mod tidy`, internal/netprobe, and the
// CGO_ENABLED=0 five-target build.
replace golang.zx2c4.com/wireguard/tun/netstack => golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee

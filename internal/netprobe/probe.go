// SPDX-License-Identifier: GPL-3.0-or-later

// Package netprobe is the dependency baseline probe for the native network
// providers. It references the tsnet and wireguard-go APIs that the Tailscale
// Net and WireGuard Net adapters will use, so `go build ./...` and the release
// cross-compiles prove both modules build with CGO_ENABLED=0 on every
// supported target before those adapters land.
//
// The package is deliberately not imported by the application: it adds no
// behavior and no linked code to a release binary. Delete it once the real
// adapters exist and their packages keep these modules in the build.
package netprobe

import (
	"context"
	"net"
	"net/netip"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"tailscale.com/tsnet"
)

// Compile-time probes for the API shapes the network adapters rely on. The
// function-typed declarations fail to compile if a pinned upgrade changes a
// signature the adapters will need.
var (
	_ func(*tsnet.Server, context.Context, string, string) (net.Conn, error)   = (*tsnet.Server).Dial
	_ func([]netip.Addr, []netip.Addr, int) (tun.Device, *netstack.Net, error) = netstack.CreateNetTUN
	_ func(tun.Device, conn.Bind, *device.Logger) *device.Device               = device.NewDevice
	_ func(*device.Device, string) error                                       = (*device.Device).IpcSet
)

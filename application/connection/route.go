package connection

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/mslxl/natcatty/domain/model"
)

// Hop is one jump Host in a route. The target Host is not a Hop; it is the
// Request itself. Jumps are ordered first = closest to the client.
type Hop struct {
	HostID     model.HostID
	Label      string
	Hostname   string
	Port       int
	Username   string
	Algorithms AlgorithmPolicy
	// ConfigFingerprint is the opaque digest of this jump's credential
	// selection, so a credential change opens a fresh transport.
	ConfigFingerprint string
}

// AlgorithmPolicy is one endpoint's SSH negotiation policy. Legacy widens the
// offer for older servers; SkipECDSAHostKey removes every ECDSA host-key
// algorithm; a non-empty category replaces the transport default for that
// category. Adjacent to the route rather than embedded in it because every
// endpoint carries its own policy.
type AlgorithmPolicy struct {
	Legacy           bool
	SkipECDSAHostKey bool
	Ciphers          []string
	KeyExchanges     []string
	MACs             []string
	HostKeys         []string
}

// LabelOrAddress returns the progress label for a hop, matching the original:
// the Host label when present, otherwise host:port with IPv6 bracketed.
func (h Hop) LabelOrAddress() string {
	if h.Label != "" {
		return h.Label
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(h.Hostname, strconv.Itoa(port))
}

// ProxyType is the transport used to reach the first endpoint of a route.
type ProxyType string

const (
	// ProxyHTTP is an HTTP CONNECT proxy.
	ProxyHTTP ProxyType = "http"
	// ProxySOCKS5 is a SOCKS5 proxy.
	ProxySOCKS5 ProxyType = "socks5"
	// ProxyCommand spawns a process and speaks SSH over its stdio.
	ProxyCommand ProxyType = "command"
)

// Proxy is a resolved transport configuration for the first endpoint, not the
// saved "Proxy Profile" entity that ticket 20 adds. Password is credential
// plaintext in memory only and is never persisted on a domain entity.
type Proxy struct {
	Type     ProxyType
	Host     string
	Port     int
	Command  string
	Username string
	Password string
}

// Route is the non-secret route a Host uses: its jump chain and the proxy that
// applies to the first endpoint. Proxy password material is carried here only
// for the duration of a connection attempt.
type Route struct {
	Jumps      []Hop
	Proxy      *Proxy
	Algorithms AlgorithmPolicy
	// ConfigFingerprint is the target Host's opaque credential and policy
	// digest. It scopes pool reuse without carrying credential material.
	ConfigFingerprint string
}

// ChainStatus is one step in a jump chain's progress, mirroring the original
// netcatty:chain:progress status vocabulary.
type ChainStatus string

const (
	ChainConnecting     ChainStatus = "connecting"
	ChainTCPConnected   ChainStatus = "tcp-connected"
	ChainAuthenticating ChainStatus = "authenticating"
	ChainConnected      ChainStatus = "connected"
	ChainForwarding     ChainStatus = "forwarding"
	ChainError          ChainStatus = "error"
)

// ChainProgress reports one endpoint's progress. Index is 1-based over all
// endpoints, including the target; Total counts jumps plus the target.
type ChainProgress struct {
	Index  int
	Total  int
	Label  string
	Status ChainStatus
	Err    error
}

// HopInfo identifies the endpoint a failure came from. Index is 1-based over
// jumps plus the target, so it matches ChainProgress.
type HopInfo struct {
	Index int
	Total int
	Label string
}

// TagHop attaches hop context to a typed error without changing a kind the
// transport already chose. Adapters and the Manager use it when a failure
// comes from one chain endpoint.
func TagHop(err error, index, total int, label string) error {
	if err == nil {
		return nil
	}
	info := &HopInfo{Index: index, Total: total, Label: label}
	var typed *Error
	if errors.As(err, &typed) {
		if typed.Hop != nil {
			return err
		}
		copy := *typed
		copy.Hop = info
		return &copy
	}
	return &Error{Kind: KindNetwork, Cause: err, Hop: info}
}

func hopErrorText(info *HopInfo) string {
	if info == nil {
		return ""
	}
	if info.Label != "" {
		return fmt.Sprintf(" (hop %d/%d %s)", info.Index, info.Total, info.Label)
	}
	return fmt.Sprintf(" (hop %d/%d)", info.Index, info.Total)
}

// SPDX-License-Identifier: GPL-3.0-or-later

package connection

import (
	"errors"
	"net"
)

// ProviderConnState reports a Network Provider's live state after one of its
// connections failed an I/O operation. A phase other than running means the
// failure belongs to the Provider lifecycle, so the returned error becomes a
// typed KindProvider failure. A nil info is allowed and still classifies the
// error, just without profile identity.
type ProviderConnState func() (ProviderPhase, *ProviderInfo)

// WrapProviderConn decorates a connection returned by a Network Provider so an
// I/O failure that happens after the Provider stopped, failed, or lost its
// session surfaces as a typed *Error with KindProvider. A healthy Provider
// keeps its ordinary transport error, including io.EOF from a peer that closed
// normally. A connection that already carries a typed *Error is left unchanged.
//
// This is a Network Provider contract helper: it never opens a new connection
// and never falls back to a direct dial. A nil conn or state function returns
// conn unchanged, so a caller cannot accidentally wrap a nil transport.
func WrapProviderConn(conn net.Conn, state ProviderConnState) net.Conn {
	if conn == nil || state == nil {
		return conn
	}
	return &providerConn{Conn: conn, state: state}
}

// providerConn is a net.Conn whose failed reads and writes are classified
// against the Provider's live phase.
type providerConn struct {
	net.Conn
	state ProviderConnState
}

// Read reads through the Provider connection and classifies an I/O failure.
func (c *providerConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.classify(err)
}

// Write writes through the Provider connection and classifies an I/O failure.
func (c *providerConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.classify(err)
}

// classify tags a transport error with the Provider context when its phase
// proves the Provider, not the target, is the cause. An existing typed error
// keeps its classification.
func (c *providerConn) classify(err error) error {
	if err == nil || c.state == nil {
		return err
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	phase, info := c.state()
	if phase == "" || phase == ProviderPhaseRunning {
		return err
	}
	return &Error{Kind: KindProvider, Cause: err, Provider: info}
}

// SPDX-License-Identifier: GPL-3.0-or-later

// Package et implements the EternalTerminal Session transport with the
// application-owned in-process client. The pure-Go client is vendored in
// adapters/et/internal/etgo; no external et binary is launched.
//
// The client runs the etterminal bootstrap through a connection.Manager lease
// and reaches etserver through the same lease's direct-tcpip forwarding, so the
// ET session id and passkey never leave memory.
package et

//go:build conformance

// Package atuinconformance is the release-acceptance conformance suite for the
// Atuin v18.24.5 history sync protocol. It runs a local, self-hosted pinned
// Atuin server and the official pinned CLI against the real Netcatty HTTP
// adapter, sync engine, history service, and SQLite store.
//
// The suite is deliberately excluded from a normal `go test ./...`: every file
// carries the `conformance` build tag because it needs two external binaries
// and starts local processes. See docs/atuin/conformance.md for the invocation
// and for the evidence a release acceptance run must record.
//
// Nothing here contacts atuin.sh or any hosted Atuin, and nothing here calls
// DELETE /api/v0/store. The suite refuses to run against a non-pinned binary
// instead of silently substituting one.
package atuinconformance

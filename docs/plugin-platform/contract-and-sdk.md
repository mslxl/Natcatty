# Netcatty plugin contract and SDK

Status: internal preview (`0.1.0-internal`)
Tracking issue: [#2269](https://github.com/binaricat/Netcatty/issues/2269)

This document describes the contract delivered by phase 1 of the plugin
platform. It deliberately does not expose a plugin loader. Package installation,
runtime isolation, permissions, UI contributions, terminal providers, connection
providers, synchronization, and distribution are introduced by later phases.

## Contract ownership

`packages/plugin-contract/schema/plugin-contract.schema.json` is the canonical
public protocol. It uses JSON Schema 2020-12 and defines:

- package manifests and entrypoints;
- permission declarations;
- setting, command, menu, view, and provider contributions;
- JSON-RPC requests, notifications, results, cancellation, and errors;
- stream frames and flow-control windows;
- permission requests and decisions;
- provider requests and results.

`npm run generate:plugin-contract` derives two committed artifacts from that
file:

1. TypeScript types exported by `@netcatty/plugin-contract`;
2. a self-contained schema bundle under `electron/plugins/generated/` for the
   future host runtime.

`npm run check:plugin-contract` compares both outputs byte-for-byte. CI can
therefore reject a schema edit whose SDK or Electron representation was not
regenerated.

The contract is intentionally marked internal. Compatibility is not promised
until the final rollout PR freezes API 1.0. During the internal period, every
breaking change must update the schema identifier and generated artifacts in
the same commit.

## Package layout

A plugin is a directory with `netcatty.plugin.json` at its root. The manifest
declares one or both execution entrypoints:

```json
{
  "manifestVersion": 1,
  "id": "com.example.my-plugin",
  "name": "my-plugin",
  "version": "0.1.0",
  "publisher": "example",
  "engines": {
    "netcatty": ">=0.0.0",
    "api": "0.1.0-internal"
  },
  "main": {
    "browser": "dist/browser.js",
    "node": "dist/node.js"
  }
}
```

Paths use relative POSIX syntax. Absolute paths, drive-letter paths,
backslashes, `.` and `..` segments, Unicode normalization ambiguity, Windows
reserved names, and platform-specific trailing dots or spaces are rejected.
Every entrypoint, view document, and companion executable must exist in the
package.

Browser and Node entrypoints express placement, not permission. The future
runtime still evaluates the manifest permissions, trust level, and user grants
before activating either entrypoint.

## TypeScript SDK

`@netcatty/plugin-sdk` exports the generated contract types and a small set of
lifecycle primitives:

- `definePlugin` keeps exact plugin types while checking the activation shape;
- `DisposableStore` gives activation code one cleanup owner;
- `CancellationTokenSource` provides cooperative cancellation without exposing
  host abort controllers;
- `PluginError` carries a stable machine-readable error code and JSON details;
- `PluginContext` defines storage, secret storage, logging, and subscriptions.

The context interfaces are contracts only in phase 1. The isolated host in
phase 2 and capability brokers in phase 3 provide their implementations.

Plugin entrypoints should return or register every acquired resource:

```ts
import { definePlugin } from "@netcatty/plugin-sdk";

export default definePlugin({
  activate(context) {
    context.subscriptions.add(registerSomething());
  },
});
```

Activation code must treat cancellation and deadlines as normal outcomes.
Host-side cancellation can stop waiting for a plugin but cannot forcibly unwind
arbitrary JavaScript without terminating the isolated runtime.

## CLI

`@netcatty/plugin-cli` supplies four commands:

- `init` creates a minimal TypeScript plugin;
- `validate` checks a source directory or packaged archive;
- `build` validates the manifest and runs the plugin's npm build script without
  a shell;
- `pack` emits a deterministic `.ncpkg` archive.

The packer sorts UTF-8 package paths, stores fixed ZIP timestamps and file
modes, and writes entries without platform-dependent compression output. The
same files and manifest therefore produce the same archive bytes.

Package validation rejects:

- path traversal, absolute paths, backslashes, and case-colliding names;
- symbolic links and non-regular files;
- executable files not declared as companion executables;
- companion binaries whose SHA-256 does not match the manifest;
- duplicate entries, encrypted entries, and unsupported compression methods;
- missing entrypoints and views;
- excessive path, file, archive, or expanded-package sizes.

These checks are repeated when reading `.ncpkg` files. Installation in phase 2
must not trust a package merely because the publisher previously ran the CLI.

## Compatibility rules for later phases

The following rules are already fixed even though their implementations arrive
later:

1. JSON Schema is the wire authority. TypeScript types alone never justify
   accepting an unvalidated message.
2. Unknown manifest properties are rejected within API 0.1. This prevents a
   misspelled security declaration from silently becoming ineffective.
3. Runtime RPC payloads are JSON values. Native objects, functions, Electron
   handles, DOM nodes, and cyclic values cannot cross the boundary.
4. Permission declarations do not grant access. They only make a future user
   grant possible.
5. Required and optional permission sets cannot overlap.
6. Secret settings cannot contain defaults in the manifest.
7. Companion executables are content-addressed and explicitly declared.
8. Cancellation identifiers and deadlines are part of the RPC contract so a
   slow plugin cannot retain an unbounded host request.
9. Stream sequence numbers and receive windows are part of the public protocol;
   producers must stop when the receiver's advertised capacity is exhausted.

## Repository commands

```bash
npm run generate:plugin-contract
npm run check:plugin-contract
npm run test:plugin-contract
npm run build:plugin-packages
```

The complete application checks remain mandatory because workspace and root
dependency changes affect installation and release builds.

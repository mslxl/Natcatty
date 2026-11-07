# Atuin-compatible history sync

Netcatty Native can act as an independent, native Go client for the Atuin v18
history sync protocol, pinned to Atuin v18.24.5 (commit
`ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e`). This directory publishes the
upstream, licensing, trademark, and compatibility material that governs that
feature.

> **Netcatty is an independent client and is not affiliated with Atuin, Inc.**
> "Atuin" is the name of the upstream project. Trademark rights in the name and
> marks are assumed to belong to Atuin, Inc. for the purposes of this feature;
> registration status was not verified. Netcatty is not endorsed by, sponsored
> by, or produced by Atuin, Inc.

## Documents

- [Licensing and trademark](licensing-and-trademark.md): the MIT notice and
  attribution for Atuin, the release-time notice obligation, and the rules for
  naming the feature in the UI, documentation, and marketing.
- [Supported versions and compatibility](compatibility.md): the published
  compatibility statement, the pinned reference (Atuin v18.24.5 at commit
  `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e`), the fail-closed version policy,
  and the product constraints that bound what the client may do.
- [Conformance suite](conformance.md): how to run the pinned, self-hosted
  release-acceptance suite locally and in CI, its artifact checksums, and the
  observed PASS evidence.

## Status

| Item | State |
| --- | --- |
| MIT notice and attribution recorded | Recorded in this directory |
| Trademark and naming constraints recorded | Provisional rules recorded in this directory. |
| Supported-version and compatibility statement published | Published in this directory |
| Independent-client disclaimer | Published here and in the compatibility statement |
| Human/legal review | Pending. These documents are engineering policy, not legal advice. |

These documents publish the legal and compatibility contract for the feature.
They are not evidence that every implementation ticket is merged or that the
conformance suite has passed. The specification makes the golden vectors and the
conformance run release acceptance evidence; this directory claims only the
policy the implementation must satisfy.

## Scope in one paragraph

Under the specification, Netcatty records Command History locally and, when the
user explicitly connects one Atuin account, syncs only the Atuin `history`
record tag. The Host runs nothing: Netcatty never uploads, installs, or runs the
Atuin binary on a Host, never writes the master key there, and never changes
remote shell keybindings.
Local Command History remains plaintext at rest; the Atuin master key protects
only sync payloads. Netcatty never calls `DELETE /api/v0/store`, and Atuin
credentials never enter Vault cloud sync or a portable export. The full
contract is in the [compatibility statement](compatibility.md).

The implementation contract is the project's internal Atuin integration
specification. This directory records the externally published statement; where
the two overlap, the specification governs implementation details and this
directory governs what is claimed publicly.

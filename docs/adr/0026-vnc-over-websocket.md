# VNC over WebSocket carries the same RFB stream

A VNC Host can carry its RFB byte stream inside a WebSocket, the transport a
noVNC page gets from websockify. The Host stays a VNC Host: the protocol, the
credentials, the viewer settings, and the route are unchanged. Only the framing
between the endpoint and the RFB client changes.

The choice lives in `VNCConfig` as three fields: `WebSocket`, `WebSocketPath`
(default `/websockify`), and `WebSocketSecure` (wss instead of ws). The Host's
`Port` is the HTTP port the WebSocket is served on (6080 by default); it is not
an RFB display port, so the editor's display-number input does not apply while
the transport is on. Enabling the transport in a form seeds 6080 and switches
to port input; disabling it clears the path and TLS choice and restores display
0 while the port is still that default. A path that cannot appear in an HTTP
request target is rejected when the Host is saved or the quick-connect form is
submitted, not during the dial.

The adapter keeps its existing seam. The Factory dials the TCP endpoint through
the Host's route — jump chain, Proxy Profile, or Network Profile — and then
upgrades that same connection with an HTTP WebSocket handshake, so routing and
trust work exactly as for raw RFB. The RFB handshake runs afterwards over the
WebSocket's byte-stream view, which means authentication, security-type
selection, plaintext confirmation, and the framebuffer path are the same code
for both transports. The client requests the `binary` subprotocol, sends an
`Origin` of `http(s)://authority`, does not follow redirects (a redirecting
path is a wrong endpoint, not a login page), and caps one inbound message at
16 MiB.

`wss` is verified against the system trust store with the Host name as the
server name; there is no insecure-skip option. Because a wss stream is already
encrypted, the Session is marked routed so the RFB security negotiation does
not add a second TLS layer and the plaintext warning does not fire.

**Status**: accepted (2026-11-23).

**Considered Options**:
- **A separate `websockify` protocol or Host kind.** Rejected: the endpoint is
  the same VNC server, and treating it as another protocol would fork the
  editor, credentials, routing, and Session kind for a transport difference.
- **Hand-rolling the WebSocket client.** Rejected: framing, masking, control
  frames, and fragmentation are easy to get subtly wrong; a maintained client
  library carries them, and the adapter supplies only the connection it already
  dialled.
- **Dialling the WebSocket endpoint directly and skipping the route.** Rejected:
  it would bypass jump chains, proxies, and Network Profiles that already apply
  to every Host.
- **Storing the upgrade path as a scheme or full URL.** Rejected: the Host
  already stores address and port separately, so a path (with an optional
  query, such as a websockify token) is the only missing piece.
- **Skipping certificate verification for self-signed endpoints.** Rejected: a
  hidden trust bypass is worse than a clear authentication failure; the user can
  reach such a server through a Network Profile or a trusted proxy instead.

**Consequences**:
- Portable CSV and JSON exports carry the three WebSocket fields; an older
  reader ignores them and still sees a VNC Host, and an import normalizes a
  missing path to `/websockify`.
- The Host list, search, groups, tags, and sync treat a WebSocket VNC Host as an
  ordinary VNC Host.
- noVNC's browser UI is out of scope: the app speaks the websockify transport,
  not the page that usually sits in front of it.

// Package rfb implements the client side of the Remote Framebuffer protocol
// (RFC 6143) as pure data and state machines: version negotiation for RFB
// 3.3, 3.7 and 3.8, the None security type, the shared-flag client
// initialisation, client and server messages, the Raw, CopyRect, RRE and
// Hextile rectangle decoders, pixel-format normalisation to 32-bit true
// colour, and the X11 keysym and QEMU key-number tables.
//
// The package imports only the standard library and performs no network I/O.
// The transport adapter owns the socket and hands this package a stream, which
// keeps the wire contract exercisable against an in-process server (see the
// rfbtest subpackage) instead of a live desktop.
package rfb

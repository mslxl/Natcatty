package nativeui

import (
	"errors"
	"fmt"
)

// MaxWebviewBridgeBytes bounds one bridge request or response payload. The
// page is untrusted, so both directions enforce the same cap before the
// payload reaches the backend or the page.
const MaxWebviewBridgeBytes = 64 << 10

// WebviewReply is the backend's answer to one page bridge request. Handle is
// the generation-tagged handle from the request event, Request is the page's
// request id, Status is the wire status (ok carries a payload), and Payload
// is the bounded response text.
type WebviewReply struct {
	Handle  uint64
	Request uint64
	Status  Status
	Payload string
}

// EncodeWebviewReply encodes one ui.webview_respond request.
func EncodeWebviewReply(reply WebviewReply) []byte {
	out := make([]byte, 0, 20+len(reply.Payload))
	out = appendU64(out, reply.Handle)
	out = appendU64(out, reply.Request)
	out = appendU32(out, uint32(reply.Status))
	out = appendString(out, reply.Payload)
	return out
}

// DecodeWebviewReply decodes one ui.webview_respond request.
func DecodeWebviewReply(data []byte) (WebviewReply, error) {
	var reply WebviewReply
	reader := &wireReader{data: data}
	reply.Handle = reader.u64()
	reply.Request = reader.u64()
	reply.Status = Status(reader.u32())
	reply.Payload = reader.str()
	if reader.err != nil {
		return reply, reader.err
	}
	if len(reply.Payload) > MaxWebviewBridgeBytes {
		return reply, fmt.Errorf("native ui: webview reply exceeds %d bytes", MaxWebviewBridgeBytes)
	}
	if reader.remaining() != 0 {
		return reply, errors.New("native ui: trailing bytes in webview reply")
	}
	return reply, nil
}

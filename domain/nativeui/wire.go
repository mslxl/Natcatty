package nativeui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The native UI wire format is little-endian and length-prefixed. Strings are
// UTF-8 with a u32 byte length. Every decoder copies the strings it reads, so
// the caller's buffer may be reused as soon as the call returns.

// Status is the result of one native UI service call. Permission denials and
// validation failures travel here rather than through the transport status,
// because the payload is binary and must stay self-describing.
type Status uint32

const (
	StatusOK          Status = 0
	StatusInvalid     Status = 1
	StatusLimit       Status = 2
	StatusDenied      Status = 3
	StatusStaleHandle Status = 4
	StatusUnavailable Status = 5
	StatusClosed      Status = 6
)

// String renders a status for diagnostics.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusInvalid:
		return "invalid"
	case StatusLimit:
		return "limit"
	case StatusDenied:
		return "denied"
	case StatusStaleHandle:
		return "stale-handle"
	case StatusUnavailable:
		return "unavailable"
	case StatusClosed:
		return "closed"
	}
	return fmt.Sprintf("status(%d)", uint32(s))
}

// StatusResponse is the response envelope shared by every native UI method.
// Created carries the handles issued by a successful apply.
type StatusResponse struct {
	Status   Status
	Revision uint64
	Message  string
	Created  []Created
}

// EncodeStatus encodes one response envelope.
func EncodeStatus(response StatusResponse) []byte {
	size := 4 + 8 + 4 + len(response.Message) + 4 + len(response.Created)*(4+8)
	out := make([]byte, 0, size)
	out = appendU32(out, uint32(response.Status))
	out = appendU64(out, response.Revision)
	out = appendString(out, response.Message)
	out = appendU32(out, uint32(len(response.Created)))
	for _, created := range response.Created {
		out = appendU32(out, uint32(created.ID))
		out = appendU64(out, created.Handle)
	}
	return out
}

// DecodeStatus decodes a response envelope.
func DecodeStatus(data []byte) (StatusResponse, error) {
	var response StatusResponse
	reader := &wireReader{data: data}
	response.Status = Status(reader.u32())
	response.Revision = reader.u64()
	response.Message = reader.str()
	count := reader.u32()
	if reader.err != nil {
		return response, reader.err
	}
	if count > MaxNodes {
		return response, fmt.Errorf("native ui: %d created nodes exceed the limit", count)
	}
	for i := uint32(0); i < count; i++ {
		created := Created{ID: NodeID(reader.u32()), Handle: reader.u64()}
		if reader.err != nil {
			return response, reader.err
		}
		response.Created = append(response.Created, created)
	}
	if reader.remaining() != 0 {
		return response, errors.New("native ui: trailing bytes in response")
	}
	return response, nil
}

// BatchErrorStatus maps a batch failure to its wire status.
func BatchErrorStatus(err error) Status {
	var stale *StaleHandleError
	if errors.As(err, &stale) {
		return StatusStaleHandle
	}
	return StatusInvalid
}

// EncodeBatch encodes one apply request.
func EncodeBatch(batch Batch) []byte {
	out := make([]byte, 0, 12+len(batch.Ops)*16)
	out = appendU64(out, uint64(batch.Root))
	out = appendU32(out, uint32(len(batch.Ops)))
	for _, op := range batch.Ops {
		out = append(out, byte(op.Kind))
		switch op.Kind {
		case OpCreate:
			out = appendU32(out, uint32(op.ID))
			out = appendU32(out, uint32(op.NodeKind))
		case OpDestroy:
			out = appendU64(out, uint64(op.Node))
		case OpInsert:
			out = appendU64(out, uint64(op.Parent))
			out = appendU32(out, op.Index)
			out = appendU64(out, uint64(op.Child))
		case OpMove:
			out = appendU64(out, uint64(op.Node))
			out = appendU64(out, uint64(op.Parent))
			out = appendU32(out, op.Index)
		case OpSetProp:
			out = appendU64(out, uint64(op.Node))
			out = appendU32(out, uint32(op.Prop))
			out = appendU32(out, uint32(op.Value.Kind))
			out = appendU64(out, uint64(op.Value.Int))
			out = appendU64(out, math.Float64bits(op.Value.Float))
			out = appendU64(out, uint64(op.Value.Ref))
			out = appendString(out, op.Value.Text)
		case OpSetText:
			out = appendU64(out, uint64(op.Node))
			out = appendString(out, op.Text)
		case OpSetChildren:
			out = appendU64(out, uint64(op.Node))
			out = appendU32(out, uint32(len(op.Children)))
			for _, child := range op.Children {
				out = appendU64(out, uint64(child))
			}
		}
	}
	return out
}

// DecodeBatch decodes one apply request. The byte and op caps apply before
// any operation is built.
func DecodeBatch(data []byte) (Batch, error) {
	if len(data) > MaxBatchBytes {
		return Batch{}, fmt.Errorf("native ui: batch is %d bytes, above %d", len(data), MaxBatchBytes)
	}
	reader := &wireReader{data: data}
	batch := Batch{Root: NodeRef(reader.u64())}
	count := reader.u32()
	if reader.err != nil {
		return batch, reader.err
	}
	if count > MaxBatchOps {
		return batch, fmt.Errorf("native ui: batch has %d ops, above %d", count, MaxBatchOps)
	}
	batch.Ops = make([]Op, 0, count)
	for i := uint32(0); i < count; i++ {
		kind := OpKind(reader.u8())
		op := Op{Kind: kind}
		switch kind {
		case OpCreate:
			op.ID = NodeID(reader.u32())
			op.NodeKind = NodeKind(reader.u32())
		case OpDestroy:
			op.Node = NodeRef(reader.u64())
		case OpInsert:
			op.Parent = NodeRef(reader.u64())
			op.Index = reader.u32()
			op.Child = NodeRef(reader.u64())
		case OpMove:
			op.Node = NodeRef(reader.u64())
			op.Parent = NodeRef(reader.u64())
			op.Index = reader.u32()
		case OpSetProp:
			op.Node = NodeRef(reader.u64())
			op.Prop = PropID(reader.u32())
			op.Value.Kind = PropValueKind(reader.u32())
			op.Value.Int = int64(reader.u64())
			op.Value.Float = math.Float64frombits(reader.u64())
			op.Value.Ref = NodeRef(reader.u64())
			op.Value.Text = reader.str()
		case OpSetText:
			op.Node = NodeRef(reader.u64())
			op.Text = reader.str()
		case OpSetChildren:
			op.Node = NodeRef(reader.u64())
			children := reader.u32()
			if reader.err != nil {
				break
			}
			if children > MaxNodes {
				return batch, fmt.Errorf("native ui: %d children exceed the node limit", children)
			}
			op.Children = make([]NodeRef, 0, children)
			for j := uint32(0); j < children; j++ {
				op.Children = append(op.Children, NodeRef(reader.u64()))
			}
		default:
			return batch, fmt.Errorf("native ui: unknown op kind %d", kind)
		}
		if reader.err != nil {
			return batch, reader.err
		}
		batch.Ops = append(batch.Ops, op)
	}
	if reader.remaining() != 0 {
		return batch, errors.New("native ui: trailing bytes in batch")
	}
	return batch, nil
}

// EventFrameSize is the fixed part of one encoded event.
const EventFrameSize = 76

// EncodeEvents encodes queued events for a poll response.
func EncodeEvents(events []Event) []byte {
	out := make([]byte, 0, len(events)*(EventFrameSize+16))
	for _, ev := range events {
		out = appendU32(out, uint32(ev.Kind))
		out = appendU32(out, 0)
		out = appendU64(out, uint64(ev.Node))
		out = appendU64(out, uint64(ev.Time))
		out = appendU32(out, uint32(ev.Key))
		out = appendU32(out, uint32(ev.Mods))
		out = appendU32(out, uint32(ev.Button))
		out = appendU32(out, uint32(ev.Index))
		out = appendF32(out, ev.X)
		out = appendF32(out, ev.Y)
		out = appendF32(out, ev.DX)
		out = appendF32(out, ev.DY)
		out = appendF32(out, ev.Width)
		out = appendF32(out, ev.Height)
		out = appendU64(out, uint64(ev.Related))
		out = appendString(out, ev.Text)
	}
	return out
}

// DecodeEvents decodes a poll payload.
func DecodeEvents(data []byte) ([]Event, error) {
	reader := &wireReader{data: data}
	var events []Event
	for reader.remaining() > 0 {
		event, err := decodeEventFrame(reader)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func decodeEventFrame(reader *wireReader) (Event, error) {
	if reader.remaining() < EventFrameSize {
		return Event{}, errors.New("native ui: truncated event")
	}
	event := Event{
		Kind: EventKind(reader.u32()),
	}
	reader.u32() // reserved field keeps the frame aligned
	event.Node = NodeRef(reader.u64())
	event.Time = int64(reader.u64())
	event.Key = Key(reader.u32())
	event.Mods = Modifiers(reader.u32())
	event.Button = int32(reader.u32())
	event.Index = int32(reader.u32())
	event.X = reader.f32()
	event.Y = reader.f32()
	event.DX = reader.f32()
	event.DY = reader.f32()
	event.Width = reader.f32()
	event.Height = reader.f32()
	event.Related = NodeRef(reader.u64())
	event.Text = reader.str()
	if reader.err != nil {
		return Event{}, reader.err
	}
	if !event.Kind.Valid() {
		return Event{}, fmt.Errorf("native ui: unknown event kind %d", event.Kind)
	}
	return event, nil
}

// ListenRequest asks the host to queue one event kind for one node.
type ListenRequest struct {
	Node  NodeRef
	Event EventKind
}

// DecodeListenRequest decodes a listen request.
func DecodeListenRequest(data []byte) (ListenRequest, error) {
	reader := &wireReader{data: data}
	request := ListenRequest{Node: NodeRef(reader.u64()), Event: EventKind(reader.u32())}
	if reader.err != nil {
		return request, reader.err
	}
	if reader.remaining() != 0 {
		return request, errors.New("native ui: trailing bytes in listen request")
	}
	if !request.Event.Valid() {
		return request, fmt.Errorf("native ui: unknown event kind %d", request.Event)
	}
	return request, nil
}

// EncodeListenRequest encodes a listen request.
func EncodeListenRequest(request ListenRequest) []byte {
	return appendU32(appendU64(nil, uint64(request.Node)), uint32(request.Event))
}

// MaxPollEvents caps one poll request. Zero asks for everything queued, which
// is already bounded by MaxQueuedEvents.
const MaxPollEvents = MaxQueuedEvents

// EncodeUnlistenRequest encodes one listener handle.
func EncodeUnlistenRequest(listener uint64) []byte {
	return appendU64(nil, listener)
}

// DecodeUnlistenRequest decodes one listener handle.
func DecodeUnlistenRequest(data []byte) (uint64, error) {
	if len(data) != 8 {
		return 0, errors.New("native ui: unlisten request must be one u64")
	}
	return binary.LittleEndian.Uint64(data), nil
}

// EncodePollRequest encodes the maximum number of events to drain, or zero for
// everything queued.
func EncodePollRequest(max uint32) []byte {
	return appendU32(nil, max)
}

// DecodePollRequest decodes a poll request.
func DecodePollRequest(data []byte) (int, error) {
	if len(data) != 4 {
		return 0, errors.New("native ui: poll request must be one u32")
	}
	max := binary.LittleEndian.Uint32(data)
	if max > MaxPollEvents {
		return 0, fmt.Errorf("native ui: poll of %d events exceeds the limit of %d", max, MaxPollEvents)
	}
	return int(max), nil
}

// PollResponse carries queued events. The host never calls the guest
// synchronously; the guest drains this response from inside its poll export.
type PollResponse struct {
	Status  Status
	Message string
	Events  []Event
}

// EncodePollResponse encodes one poll response.
func EncodePollResponse(response PollResponse) []byte {
	out := appendU32(nil, uint32(response.Status))
	out = appendString(out, response.Message)
	out = appendU32(out, uint32(len(response.Events)))
	return append(out, EncodeEvents(response.Events)...)
}

// DecodePollResponse decodes one poll response and validates the declared
// event count against the frames that follow.
func DecodePollResponse(data []byte) (PollResponse, error) {
	reader := &wireReader{data: data}
	response := PollResponse{Status: Status(reader.u32()), Message: reader.str()}
	count := reader.u32()
	if reader.err != nil {
		return response, reader.err
	}
	if count > MaxQueuedEvents {
		return response, fmt.Errorf("native ui: poll response carries %d events, above %d", count, MaxQueuedEvents)
	}
	for i := uint32(0); i < count; i++ {
		event, err := decodeEventFrame(reader)
		if err != nil {
			return response, err
		}
		response.Events = append(response.Events, event)
	}
	if reader.remaining() != 0 {
		return response, errors.New("native ui: trailing bytes in poll response")
	}
	return response, nil
}

// ListenResponse carries the new listener handle.
type ListenResponse struct {
	Status   Status
	Listener uint64
	Message  string
}

// EncodeListenResponse encodes a listen response.
func EncodeListenResponse(response ListenResponse) []byte {
	out := appendU32(nil, uint32(response.Status))
	out = appendU64(out, response.Listener)
	return appendString(out, response.Message)
}

// DecodeListenResponse decodes a listen response.
func DecodeListenResponse(data []byte) (ListenResponse, error) {
	reader := &wireReader{data: data}
	response := ListenResponse{Status: Status(reader.u32()), Listener: reader.u64()}
	response.Message = reader.str()
	if reader.err != nil {
		return response, reader.err
	}
	return response, nil
}

// --- primitive helpers ---

func appendU32(out []byte, value uint32) []byte {
	return binary.LittleEndian.AppendUint32(out, value)
}

func appendU64(out []byte, value uint64) []byte {
	return binary.LittleEndian.AppendUint64(out, value)
}

func appendF32(out []byte, value float32) []byte {
	return binary.LittleEndian.AppendUint32(out, math.Float32bits(value))
}

func appendString(out []byte, value string) []byte {
	out = appendU32(out, uint32(len(value)))
	return append(out, value...)
}

type wireReader struct {
	data []byte
	pos  int
	err  error
}

func (r *wireReader) remaining() int { return len(r.data) - r.pos }

func (r *wireReader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.pos+n > len(r.data) {
		r.err = errors.New("native ui: truncated payload")
		return false
	}
	return true
}

func (r *wireReader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	value := r.data[r.pos]
	r.pos++
	return value
}

func (r *wireReader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	value := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return value
}

func (r *wireReader) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	value := binary.LittleEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return value
}

func (r *wireReader) f32() float32 {
	return math.Float32frombits(r.u32())
}

func (r *wireReader) str() string {
	length := int(r.u32())
	if r.err != nil {
		return ""
	}
	if length < 0 || length > MaxTextBytes {
		r.err = fmt.Errorf("native ui: string of %d bytes exceeds %d", length, MaxTextBytes)
		return ""
	}
	if !r.need(length) {
		return ""
	}
	value := string(r.data[r.pos : r.pos+length])
	r.pos += length
	return value
}

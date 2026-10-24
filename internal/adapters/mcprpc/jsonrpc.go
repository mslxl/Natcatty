package mcprpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol error codes from the original bridge.
const (
	// CodeAuthRequired rejects a connection's first message when the token or
	// method is wrong. The server closes the connection afterwards.
	CodeAuthRequired = -32001
	// CodeDispatchFailed reports an authenticated request the app could not
	// complete.
	CodeDispatchFailed = -32000
)

// MaxLineBytes bounds one newline-delimited JSON-RPC message, matching the
// original 10 MiB TCP buffer cap.
const MaxLineBytes = 10 << 20

// ErrLineTooLong reports a line beyond the configured buffer cap. The caller
// closes the connection.
var ErrLineTooLong = errors.New("mcprpc: JSON-RPC line exceeds the buffer limit")

// RPCError is a JSON-RPC error response.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ErrorCode returns the JSON-RPC error code carried by err, or 0 for a
// transport or local error.
func ErrorCode(err error) int {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	return 0
}

// request is one inbound JSON-RPC message. ID is raw so a number or string is
// echoed unchanged, and omitted IDs are notifications.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// readLine reads one newline-delimited message without ever buffering more
// than max bytes. A final line without a newline is returned with io.EOF.
func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = MaxLineBytes
	}
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > max {
			return nil, ErrLineTooLong
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return line, io.EOF
		default:
			return line, err
		}
	}
}

// ReadLine exposes the bounded newline reader to the stdio protocol package,
// which speaks the same framing on stdin.
func ReadLine(reader *bufio.Reader, max int) ([]byte, error) { return readLine(reader, max) }

// WriteMessage exposes the newline-delimited writer to the stdio protocol
// package.
func WriteMessage(writer io.Writer, message any) error { return writeMessage(writer, message) }

// writeMessage writes one JSON message and its newline.
func writeMessage(writer io.Writer, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("mcprpc: encode message: %w", err)
	}
	payload = append(payload, '\n')
	_, err = writer.Write(payload)
	return err
}

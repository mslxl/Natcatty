// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// maxAssembledInstruction bounds a decompressed TransportInstruction so a
// malicious peer cannot expand a small datagram into an unbounded allocation.
const maxAssembledInstruction = 4 << 20

func compressInstruction(data []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zlib.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		return nil, fmt.Errorf("%w: compress: %v", ErrMalformed, err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("%w: compress: %v", ErrMalformed, err)
	}
	return buffer.Bytes(), nil
}

func decompressInstruction(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: decompress: %v", ErrMalformed, err)
	}
	defer reader.Close()
	plain, err := io.ReadAll(io.LimitReader(reader, maxAssembledInstruction+1))
	if err != nil {
		return nil, fmt.Errorf("%w: decompress: %v", ErrMalformed, err)
	}
	if len(plain) > maxAssembledInstruction {
		return nil, fmt.Errorf("%w: instruction exceeds %d bytes", ErrMalformed, maxAssembledInstruction)
	}
	return plain, nil
}

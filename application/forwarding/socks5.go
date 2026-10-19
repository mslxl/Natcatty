package forwarding

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// SOCKS5 wire constants from RFC 1928. Only CONNECT with no authentication is
// offered, matching the original dynamic forwarding implementation.
const (
	socks5Version          = 0x05
	socks5AuthNone         = 0x00
	socks5AuthUnacceptable = 0xff
	socks5CommandConnect   = 0x01

	socks5ReplySucceeded          = 0x00
	socks5ReplyConnectionRefused  = 0x05
	socks5ReplyCommandUnsupported = 0x07
	socks5ReplyAddressUnsupported = 0x08

	socks5ATYPIPv4   = 0x01
	socks5ATYPDomain = 0x03
	socks5ATYPIPv6   = 0x04

	// maxSocks5Handshake bounds the greeting and request so a malformed peer
	// cannot make the server allocate without limit.
	maxSocks5Handshake = 1024
)

// socks5Handshake negotiates a no-auth greeting and returns the CONNECT
// destination as host:port.
func socks5Handshake(conn net.Conn) (string, error) {
	limited := &limitedReader{reader: conn, remaining: maxSocks5Handshake}

	var greeting [2]byte
	if _, err := io.ReadFull(limited, greeting[:]); err != nil {
		return "", err
	}
	if greeting[0] != socks5Version {
		return "", errors.New("forwarding: unsupported SOCKS version")
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(limited, methods); err != nil {
		return "", err
	}
	accepted := false
	for _, method := range methods {
		if method == socks5AuthNone {
			accepted = true
			break
		}
	}
	if !accepted {
		_, _ = conn.Write([]byte{socks5Version, socks5AuthUnacceptable})
		return "", errors.New("forwarding: SOCKS client requires authentication")
	}
	if _, err := conn.Write([]byte{socks5Version, socks5AuthNone}); err != nil {
		return "", err
	}

	var request [4]byte
	if _, err := io.ReadFull(limited, request[:]); err != nil {
		return "", err
	}
	if request[0] != socks5Version {
		return "", errors.New("forwarding: malformed SOCKS request")
	}
	if request[1] != socks5CommandConnect {
		_ = socks5Reply(conn, socks5ReplyCommandUnsupported)
		return "", errors.New("forwarding: unsupported SOCKS command")
	}

	var host string
	switch request[3] {
	case socks5ATYPIPv4:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(limited, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case socks5ATYPIPv6:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(limited, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case socks5ATYPDomain:
		var length [1]byte
		if _, err := io.ReadFull(limited, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(limited, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		_ = socks5Reply(conn, socks5ReplyAddressUnsupported)
		return "", fmt.Errorf("forwarding: unsupported SOCKS address type %d", request[3])
	}

	var portBytes [2]byte
	if _, err := io.ReadFull(limited, portBytes[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if host == "" || port == 0 {
		_ = socks5Reply(conn, socks5ReplyAddressUnsupported)
		return "", errors.New("forwarding: SOCKS request has an empty destination")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// socks5Reply writes a reply with a zero bound address, as the original does.
func socks5Reply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{
		socks5Version, code, 0x00,
		socks5ATYPIPv4, 0, 0, 0, 0, 0, 0,
	})
	return err
}

// limitedReader stops a handshake after remaining bytes.
type limitedReader struct {
	reader    io.Reader
	remaining int
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("forwarding: SOCKS handshake exceeded its limit")
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= n
	return n, err
}

package rfb

import "fmt"

// Version is an RFB protocol version, such as 3.8.
type Version struct {
	Major int
	Minor int
}

// The versions this package speaks.
var (
	// Version3_3 is RFB 3.3, which negotiates a single U32 security type.
	Version3_3 = Version{Major: 3, Minor: 3}
	// Version3_7 is RFB 3.7, which negotiates a U8 list of security types.
	Version3_7 = Version{Major: 3, Minor: 7}
	// Version3_8 is RFB 3.8, which adds failure reason strings.
	Version3_8 = Version{Major: 3, Minor: 8}
)

// String returns the version in dotted form, such as "3.8".
func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// Wire returns the 12-byte protocol string a peer sends on the wire, such as
// "RFB 003.008\n".
func (v Version) Wire() string {
	return fmt.Sprintf("RFB %03d.%03d\n", v.Major, v.Minor)
}

// ParseVersion parses the 12-byte protocol string a peer sends.
func ParseVersion(data []byte) (Version, error) {
	if len(data) != 12 {
		return Version{}, fmt.Errorf("%w: version string is %d bytes, want 12", ErrMalformed, len(data))
	}
	if string(data[:4]) != "RFB " || data[7] != '.' || data[11] != '\n' {
		return Version{}, fmt.Errorf("%w: version string %q", ErrMalformed, data)
	}
	major, err := parseThreeDigits(data[4:7])
	if err != nil {
		return Version{}, err
	}
	minor, err := parseThreeDigits(data[8:11])
	if err != nil {
		return Version{}, err
	}
	return Version{Major: major, Minor: minor}, nil
}

func parseThreeDigits(data []byte) (int, error) {
	if len(data) != 3 {
		return 0, fmt.Errorf("%w: version field is %d bytes, want 3", ErrMalformed, len(data))
	}
	value := 0
	for _, c := range data {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: version field %q", ErrMalformed, data)
		}
		value = value*10 + int(c-'0')
	}
	return value, nil
}

// NegotiateVersion returns the highest version this client supports that is
// not newer than the server's.
func NegotiateVersion(server Version) (Version, error) {
	return NegotiateVersionPair(Version3_8, server)
}

// NegotiateVersionPair returns the newest version both peers speak. RFB
// 3.4-3.6 were never released, so those minors fall back to 3.3.
func NegotiateVersionPair(a, b Version) (Version, error) {
	if a.Major != 3 || b.Major != 3 {
		return Version{}, fmt.Errorf("%w: peers offered %s and %s", ErrUnsupportedVersion, a, b)
	}
	minor := a.Minor
	if b.Minor < minor {
		minor = b.Minor
	}
	switch {
	case minor >= Version3_8.Minor:
		return Version3_8, nil
	case minor == Version3_7.Minor:
		return Version3_7, nil
	default:
		return Version3_3, nil
	}
}

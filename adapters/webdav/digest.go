package webdav

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"

	appsync "github.com/mslxl/natcatty/application/sync"
)

// digestChallenge is the subset of an RFC 7616 challenge this adapter answers.
// qop=auth-int and userhash are not supported; a server that requires them
// gets a typed credential failure instead of a silently wrong response.
type digestChallenge struct {
	Realm     string
	Nonce     string
	Opaque    string
	Algorithm string
	QopAuth   bool
	Stale     bool
}

// digestChallengeFrom returns the first usable Digest challenge in a 401.
func digestChallengeFrom(header http.Header) (*digestChallenge, error) {
	for _, value := range header.Values("WWW-Authenticate") {
		params, ok := digestParams(value)
		if !ok {
			continue
		}
		challenge, err := parseDigestChallenge(params)
		if err == nil {
			return challenge, nil
		}
	}
	return nil, errors.New("webdav: no Digest challenge")
}

// digestParams extracts the parameter text of the Digest challenge from a
// WWW-Authenticate value that may also carry Basic or Bearer challenges.
// Commas inside quoted strings are not separators.
func digestParams(header string) (string, bool) {
	positions := []int{0}
	inQuotes := false
	for index := 0; index < len(header); index++ {
		switch header[index] {
		case '\\':
			if inQuotes && index+1 < len(header) {
				index++
			}
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				positions = append(positions, index+1)
			}
		}
	}
	for _, position := range positions {
		index := position
		for index < len(header) && (header[index] == ' ' || header[index] == '\t') {
			index++
		}
		start := index
		for index < len(header) && isTokenChar(header[index]) {
			index++
		}
		if strings.EqualFold(header[start:index], "Digest") {
			return header[index:], true
		}
	}
	return "", false
}

func isTokenChar(value byte) bool {
	switch value {
	case ' ', '\t', ',', '=', '"', '\\':
		return false
	}
	return value > 0x1f && value < 0x7f
}

// parseDigestChallenge reads auth-params. It stops at a token that is not
// followed by '=', which is how the next challenge scheme appears.
func parseDigestChallenge(input string) (*digestChallenge, error) {
	params := map[string]string{}
	index := 0
	for index < len(input) {
		for index < len(input) && (input[index] == ' ' || input[index] == '\t' || input[index] == ',') {
			index++
		}
		start := index
		for index < len(input) && isTokenChar(input[index]) {
			index++
		}
		key := strings.ToLower(input[start:index])
		if key == "" {
			break
		}
		for index < len(input) && (input[index] == ' ' || input[index] == '\t') {
			index++
		}
		if index >= len(input) || input[index] != '=' {
			// The next challenge scheme starts here.
			break
		}
		index++
		for index < len(input) && (input[index] == ' ' || input[index] == '\t') {
			index++
		}
		var value string
		if index < len(input) && input[index] == '"' {
			index++
			var builder strings.Builder
			for index < len(input) {
				if input[index] == '\\' && index+1 < len(input) {
					builder.WriteByte(input[index+1])
					index += 2
					continue
				}
				if input[index] == '"' {
					index++
					break
				}
				builder.WriteByte(input[index])
				index++
			}
			value = builder.String()
		} else {
			start := index
			for index < len(input) && input[index] != ',' && input[index] != ' ' && input[index] != '\t' {
				index++
			}
			value = input[start:index]
		}
		params[key] = value
	}

	challenge := &digestChallenge{
		Realm:     params["realm"],
		Nonce:     params["nonce"],
		Opaque:    params["opaque"],
		Algorithm: params["algorithm"],
		Stale:     strings.EqualFold(params["stale"], "true"),
	}
	if challenge.Realm == "" || challenge.Nonce == "" {
		return nil, errors.New("webdav: Digest challenge without realm or nonce")
	}
	if qop, ok := params["qop"]; ok {
		// A server may offer several qop values; choose auth when it is one of
		// them. auth-int alone is unusable because this client sends no body
		// digest, but its presence must not reject a challenge that also
		// permits plain auth (RFC 7616 section 3.3).
		supported := false
		for _, option := range strings.Split(qop, ",") {
			if strings.EqualFold(strings.TrimSpace(option), "auth") {
				supported = true
			}
		}
		if !supported {
			return nil, fmt.Errorf("webdav: Digest qop %q offers no auth option", qop)
		}
		challenge.QopAuth = true
	}
	if _, _, _, err := digestAlgorithm(challenge.Algorithm); err != nil {
		return nil, err
	}
	return challenge, nil
}

// digestAlgorithm maps a server algorithm onto its hash and canonical
// spelling. MD5 is used only as the RFC 7616 Digest hash, never for storage.
func digestAlgorithm(algorithm string) (func() hash.Hash, string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "", "md5":
		return md5.New, "MD5", false, nil
	case "md5-sess":
		return md5.New, "MD5-sess", true, nil
	case "sha-256":
		return sha256.New, "SHA-256", false, nil
	case "sha-256-sess":
		return sha256.New, "SHA-256-sess", true, nil
	default:
		return nil, "", false, fmt.Errorf("webdav: Digest algorithm %q is not supported", algorithm)
	}
}

// buildDigestAuthorization answers one challenge. nc starts at 1 for every new
// challenge because a new nonce means a new nonce-count sequence.
func buildDigestAuthorization(challenge *digestChallenge, username, password, method, uri string) (string, error) {
	newHash, canonical, session, err := digestAlgorithm(challenge.Algorithm)
	if err != nil {
		return "", fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	cnonceBytes := make([]byte, 16)
	if _, err := rand.Read(cnonceBytes); err != nil {
		return "", fmt.Errorf("%w: generate Digest cnonce: %v", appsync.ErrNetwork, err)
	}
	cnonce := hex.EncodeToString(cnonceBytes)
	digest := func(value string) string {
		sum := newHash()
		sum.Write([]byte(value))
		return hex.EncodeToString(sum.Sum(nil))
	}

	ha1 := digest(username + ":" + challenge.Realm + ":" + password)
	if session {
		ha1 = digest(ha1 + ":" + challenge.Nonce + ":" + cnonce)
	}
	ha2 := digest(method + ":" + uri)
	const nonceCount = "00000001"
	var response string
	if challenge.QopAuth {
		response = digest(ha1 + ":" + challenge.Nonce + ":" + nonceCount + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = digest(ha1 + ":" + challenge.Nonce + ":" + ha2)
	}

	parts := []string{
		`username="` + quoteAuthParam(username) + `"`,
		`realm="` + quoteAuthParam(challenge.Realm) + `"`,
		`nonce="` + quoteAuthParam(challenge.Nonce) + `"`,
		`uri="` + quoteAuthParam(uri) + `"`,
		`response="` + response + `"`,
		"algorithm=" + canonical,
	}
	if challenge.Opaque != "" {
		parts = append(parts, `opaque="`+quoteAuthParam(challenge.Opaque)+`"`)
	}
	if challenge.QopAuth {
		parts = append(parts, "qop=auth", "nc="+nonceCount, `cnonce="`+cnonce+`"`)
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

// quoteAuthParam escapes a quoted-string value per RFC 7235.
func quoteAuthParam(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

// Package webdav is the WebDAV cloud-sync adapter. It stores the encrypted
// Vault as netcatty-vault.json directly inside a user-supplied WebDAV
// collection and authenticates with Basic, Digest, or a bearer token.
//
// The adapter normalizes and validates the endpoint, follows only same-host
// redirects that do not downgrade HTTPS, never disables certificate
// verification, and reports a typed error for every transport or status
// failure. Basic credentials are encoded as UTF-8 before base64, matching RFC
// 7617 and the original Electron adapter's fix for non-ASCII credentials.
// Digest implements RFC 7616 challenge-response with qop=auth, nonce, nc, and
// cnonce for MD5, MD5-sess, SHA-256, and SHA-256-sess.
package webdav

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

const (
	// defaultTimeout bounds one WebDAV request.
	defaultTimeout = 60 * time.Second
	// maxBodyBytes bounds the sync file and error bodies read into memory.
	maxBodyBytes = 64 << 20
	// maxRedirects matches the Go client's default and the adapter's policy.
	maxRedirects = 5
)

// Options configure Client. The zero value uses a standard http.Client with
// system trust roots. HTTPClient lets tests trust a local test CA; the
// production path never sets InsecureSkipVerify.
type Options struct {
	HTTPClient *http.Client
}

// Client is the WebDAV adapter. It holds no per-profile state: every call
// receives the credential through domainsync.Token, so one Client serves any
// number of profiles and is safe from any goroutine.
type Client struct {
	http *http.Client
}

var (
	_ appsync.Remote          = (*Client)(nil)
	_ appsync.WebDAVConnector = (*Client)(nil)
)

// New returns a Client.
func New(opts Options) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{http: httpClient}
}

// NormalizeEndpoint validates a WebDAV collection URL and returns its
// canonical form. A URL with no scheme is treated as HTTPS, matching the
// original adapter. Credentials in the URL, a query string, a fragment, or a
// non-HTTP scheme are refused rather than guessed at.
func NormalizeEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: enter a WebDAV collection URL", appsync.ErrInvalidEndpoint)
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("%w: the URL must use http or https", appsync.ErrInvalidEndpoint)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: the URL has no host", appsync.ErrInvalidEndpoint)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: put credentials in the fields, not in the URL", appsync.ErrInvalidEndpoint)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: the URL must not carry a query or fragment", appsync.ErrInvalidEndpoint)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	cleaned := path.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	if cleaned == "/" {
		cleaned = ""
	}
	// A WebDAV collection is addressed without a trailing slash; fileURL
	// appends exactly one separator.
	parsed.Path = strings.TrimRight(cleaned, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// fileURL returns the sync file's URL inside a normalized collection.
func fileURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint + "/" + domainsync.SyncFileName
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + domainsync.SyncFileName
	return parsed.String()
}

// Prepare validates and normalizes form settings and reports whether the
// endpoint would send credentials without transport encryption.
func (c *Client) Prepare(settings appsync.WebDAVSettings) (appsync.WebDAVSettings, bool, error) {
	endpoint, err := NormalizeEndpoint(settings.Endpoint)
	if err != nil {
		return settings, false, err
	}
	authType := domainsync.NormalizeWebDAVAuthType(settings.AuthType)
	normalized := appsync.WebDAVSettings{
		Endpoint: endpoint,
		AuthType: authType,
		Username: strings.TrimSpace(settings.Username),
		Password: settings.Password,
		Token:    strings.TrimSpace(settings.Token),
	}
	switch authType {
	case domainsync.WebDAVAuthBearer:
		if normalized.Token == "" {
			return settings, false, fmt.Errorf("%w: enter a bearer token", appsync.ErrInvalidEndpoint)
		}
	default:
		if normalized.Username == "" {
			return settings, false, fmt.Errorf("%w: enter a username", appsync.ErrInvalidEndpoint)
		}
		if normalized.Password == "" {
			return settings, false, fmt.Errorf("%w: enter a password", appsync.ErrInvalidEndpoint)
		}
	}
	return normalized, strings.EqualFold(mustScheme(endpoint), "http"), nil
}

func mustScheme(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}

// Connect verifies the credentials and returns the credential blob and the
// account identity. A 401 is reported as ErrCredentialsRejected and a 403 as
// ErrForbidden; the caller turns either into an actionable state instead of
// retrying the same credential.
func (c *Client) Connect(ctx context.Context, settings appsync.WebDAVSettings) (domainsync.Token, appsync.Grant, error) {
	normalized, _, err := c.Prepare(settings)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	token := domainsync.Token{
		Provider:  appsync.ProviderWebDAV,
		TokenType: "WebDAV",
		WebDAV: &domainsync.WebDAVCredential{
			Endpoint: normalized.Endpoint,
			AuthType: normalized.AuthType,
			Username: normalized.Username,
			Password: normalized.Password,
			Token:    normalized.Token,
		},
	}
	grant, err := c.User(ctx, token)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	return token, grant, nil
}

// User verifies access to the collection by reading the sync file's metadata.
// A missing file is a valid empty account, so 404 succeeds. The identity is the
// normalized collection plus the username: two collections on one host, or two
// credentials, must not share a merge anchor.
func (c *Client) User(ctx context.Context, token domainsync.Token) (appsync.Grant, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return appsync.Grant{}, err
	}
	if err := c.probe(ctx, token, fileURL(credential.Endpoint)); err != nil {
		return appsync.Grant{}, err
	}
	return grantFor(credential)
}

// grantFor derives the account identity without a network call.
func grantFor(credential *domainsync.WebDAVCredential) (appsync.Grant, error) {
	parsed, err := url.Parse(credential.Endpoint)
	if err != nil {
		return appsync.Grant{}, fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	host := parsed.Host
	login := host
	identity := credential.Endpoint
	if credential.AuthType == domainsync.WebDAVAuthBasic || credential.AuthType == domainsync.WebDAVAuthDigest {
		login = credential.Username + "@" + host
		identity = credential.Endpoint + "#" + credential.Username
	}
	return appsync.Grant{ID: identity, Login: login}, nil
}

// probe issues a HEAD for the sync file. Some servers do not implement HEAD;
// a 405 falls back to a GET. Both a present and a missing file mean the
// credentials were accepted.
func (c *Client) probe(ctx context.Context, token domainsync.Token, target string) error {
	_, err := c.do(ctx, http.MethodHead, target, token, nil, nil)
	if err == nil || errors.Is(err, errNotFound) {
		return nil
	}
	var status *statusError
	if errors.As(err, &status) && status.status == http.StatusMethodNotAllowed {
		_, getErr := c.do(ctx, http.MethodGet, target, token, nil, nil)
		if getErr == nil || errors.Is(getErr, errNotFound) {
			return nil
		}
		return getErr
	}
	return err
}

// Read downloads the sync file. A 404 is the empty state. The ETag is the
// conditional-write validator used by the next update.
func (c *Client) Read(ctx context.Context, token domainsync.Token) (appsync.RemoteFile, bool, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	target := fileURL(credential.Endpoint)
	response, err := c.do(ctx, http.MethodGet, target, token, nil, nil)
	if errors.Is(err, errNotFound) {
		return appsync.RemoteFile{}, false, nil
	}
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	return appsync.RemoteFile{
		ID:        target,
		ETag:      response.header.Get("ETag"),
		Content:   response.body,
		UpdatedAt: updatedAtOf(response.body),
	}, true, nil
}

// Write creates or replaces the sync file. Creation is conditional with
// If-None-Match: *; an update is conditional with If-Match on the ETag the
// caller observed. When a server returns no ETag, an update first re-reads the
// file and refuses to write if it changed, so a peer's copy is never silently
// overwritten. A lost condition is ErrConflict and the service re-reads and
// decides again.
func (c *Client) Write(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	target := fileURL(credential.Endpoint)
	headers := map[string]string{"Content-Type": "application/json"}
	switch {
	case current.ID == "":
		headers["If-None-Match"] = "*"
	case current.ETag != "":
		headers["If-Match"] = current.ETag
	default:
		// Without a validator the only safe conditional write is a fresh
		// observation immediately before the PUT.
		existing, exists, readErr := c.Read(ctx, token)
		if readErr != nil {
			return appsync.RemoteFile{}, readErr
		}
		if !exists {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the file was deleted after the read", appsync.ErrConflict)
		}
		if !bytes.Equal(existing.Content, current.Content) {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the file changed after the read", appsync.ErrConflict)
		}
	}

	response, err := c.do(ctx, http.MethodPut, target, token, content, headers)
	if err != nil {
		var status *statusError
		if errors.As(err, &status) && status.status == http.StatusConflict && current.ID == "" {
			// The parent collection does not exist. The original adapter did
			// not create it, and a wrong endpoint should say so plainly.
			return appsync.RemoteFile{}, fmt.Errorf("%w: the WebDAV collection does not exist", appsync.ErrInvalidEndpoint)
		}
		if errors.Is(err, errNotFound) && current.ID == "" {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the WebDAV collection does not exist", appsync.ErrInvalidEndpoint)
		}
		if errors.Is(err, errNotFound) {
			// The file was deleted after the read; a conflict makes the
			// service re-observe and create a fresh copy.
			return appsync.RemoteFile{}, fmt.Errorf("%w: the file was deleted after the read", appsync.ErrConflict)
		}
		return appsync.RemoteFile{}, err
	}

	etag := response.header.Get("ETag")
	if etag == "" {
		// Not every server returns the new validator on PUT. A HEAD keeps the
		// next update conditional; failure here is not a write failure.
		if head, headErr := c.do(ctx, http.MethodHead, target, token, nil, nil); headErr == nil {
			etag = head.header.Get("ETag")
		}
	}
	id := current.ID
	if id == "" {
		id = target
	}
	return appsync.RemoteFile{
		ID:        id,
		ETag:      etag,
		Content:   content,
		UpdatedAt: updatedAtOf(content),
	}, nil
}

func updatedAtOf(content []byte) int64 {
	if envelope, err := domainsync.UnmarshalFile(content); err == nil {
		return envelope.Meta.UpdatedAt
	}
	return 0
}

func credentialFor(token domainsync.Token) (*domainsync.WebDAVCredential, error) {
	if token.WebDAV == nil || strings.TrimSpace(token.WebDAV.Endpoint) == "" {
		return nil, fmt.Errorf("%w: no WebDAV credential", appsync.ErrNotConfigured)
	}
	return token.WebDAV, nil
}

// response is one fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// errNotFound is the internal marker Read and User translate.
var errNotFound = errors.New("webdav: not found")

// statusError carries the HTTP status alongside a typed sentinel.
type statusError struct {
	status int
	err    error
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %v", e.status, e.err)
}

func (e *statusError) Unwrap() error { return e.err }

// do performs one logical request, runs Digest challenge-response when needed,
// follows only the adapter's redirect policy, and maps every status onto the
// application's error taxonomy. It never includes a response body, URL user
// info, or Authorization value in an error.
func (c *Client) do(
	ctx context.Context,
	method string,
	target string,
	token domainsync.Token,
	payload []byte,
	headers map[string]string,
) (*response, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return nil, err
	}
	authType := domainsync.NormalizeWebDAVAuthType(credential.AuthType)
	switch authType {
	case domainsync.WebDAVAuthDigest:
		return c.doDigest(ctx, method, target, credential, payload, headers)
	default:
		authorization := ""
		switch authType {
		case domainsync.WebDAVAuthBearer:
			authorization = "Bearer " + credential.Token
		default:
			authorization = basicAuthorization(credential.Username, credential.Password)
		}
		response, err := c.send(ctx, method, target, payload, headers, authorization)
		if err != nil {
			return nil, mapTransportError(err)
		}
		return c.mapStatus(response)
	}
}

// basicAuthorization builds the RFC 7617 header with UTF-8 encoded
// credentials. The original Electron bridge built this header itself because
// the upstream library encoded Latin-1 and corrupted non-ASCII passwords.
func basicAuthorization(username, password string) string {
	return "Basic " + base64UTF8(username+":"+password)
}

// base64UTF8 encodes the UTF-8 bytes of credentials. Go strings already hold
// UTF-8, so this is exactly RFC 7617's charset.
func base64UTF8(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// doDigest sends the request without Authorization, answers the Digest
// challenge, and retries once. A second 401 with stale=true is answered with
// the new nonce once more; any other 401 is a rejected credential.
func (c *Client) doDigest(
	ctx context.Context,
	method string,
	target string,
	credential *domainsync.WebDAVCredential,
	payload []byte,
	headers map[string]string,
) (*response, error) {
	response, err := c.send(ctx, method, target, payload, headers, "")
	if err != nil {
		return nil, mapTransportError(err)
	}
	if response.status != http.StatusUnauthorized {
		return c.mapStatus(response)
	}
	challenge, err := digestChallengeFrom(response.header)
	if err != nil {
		return nil, fmt.Errorf("%w: the endpoint did not offer a usable Digest challenge", appsync.ErrCredentialsRejected)
	}
	authorization, err := buildDigestAuthorization(challenge, credential.Username, credential.Password, method, requestURI(target))
	if err != nil {
		return nil, err
	}
	response, err = c.send(ctx, method, target, payload, headers, authorization)
	if err != nil {
		return nil, mapTransportError(err)
	}
	if response.status == http.StatusUnauthorized {
		if retry, retryErr := digestChallengeFrom(response.header); retryErr == nil && retry.Stale {
			authorization, authErr := buildDigestAuthorization(retry, credential.Username, credential.Password, method, requestURI(target))
			if authErr != nil {
				return nil, authErr
			}
			response, err = c.send(ctx, method, target, payload, headers, authorization)
			if err != nil {
				return nil, mapTransportError(err)
			}
		}
	}
	return c.mapStatus(response)
}

func requestURI(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	return parsed.RequestURI()
}

// send performs the HTTP exchange and reads the body. It does not interpret
// the status.
func (c *Client) send(ctx context.Context, method, target string, payload []byte, headers map[string]string, authorization string) (*response, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	client := c.client()
	httpResponse, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer httpResponse.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxBodyBytes))
	if readErr != nil {
		return nil, readErr
	}
	return &response{status: httpResponse.StatusCode, header: httpResponse.Header, body: body}, nil
}

// client returns a copy of the configured client with the redirect policy
// layered on top. A supplied CheckRedirect still runs, but cannot loosen the
// security policy.
func (c *Client) client() *http.Client {
	base := c.http
	if base == nil {
		base = &http.Client{Timeout: defaultTimeout}
	}
	clone := *base
	previous := clone.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := checkRedirect(request, via); err != nil {
			return err
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &clone
}

// checkRedirect refuses a redirect to another host or an HTTPS to HTTP
// downgrade, so a credential can never be forwarded somewhere it was not
// entered for. Same-host HTTP to HTTPS upgrades are allowed.
func checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", appsync.ErrRedirectRefused, maxRedirects)
	}
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1]
	if !strings.EqualFold(request.Method, previous.Method) {
		// net/http rewrites 301/302/303 PUTs to GET, which would drop the
		// upload body and let a redirect look like a successful write. Only a
		// redirect that preserves the method (307/308) may be followed.
		return fmt.Errorf("%w: redirect changed %s to %s", appsync.ErrRedirectRefused, previous.Method, request.Method)
	}
	original := via[0].URL
	target := request.URL
	if !strings.EqualFold(target.Scheme, "http") && !strings.EqualFold(target.Scheme, "https") {
		return fmt.Errorf("%w: redirect to scheme %q", appsync.ErrRedirectRefused, target.Scheme)
	}
	if !strings.EqualFold(target.Host, original.Host) {
		return fmt.Errorf("%w: redirect from %s to a different host", appsync.ErrRedirectRefused, original.Host)
	}
	if strings.EqualFold(original.Scheme, "https") && strings.EqualFold(target.Scheme, "http") {
		return fmt.Errorf("%w: redirect from HTTPS to plain HTTP", appsync.ErrRedirectRefused)
	}
	return nil
}

// mapStatus turns a final response into the adapter's result or typed error.
func (c *Client) mapStatus(response *response) (*response, error) {
	switch {
	case response.status >= 200 && response.status < 300:
		return response, nil
	case response.status == http.StatusNotFound:
		return response, errNotFound
	case response.status == http.StatusUnauthorized:
		return response, fmt.Errorf("%w: the endpoint rejected the credentials", appsync.ErrCredentialsRejected)
	case response.status == http.StatusForbidden:
		return response, fmt.Errorf("%w: the endpoint refused this operation", appsync.ErrForbidden)
	case response.status == http.StatusPreconditionFailed:
		return response, fmt.Errorf("%w: the cloud copy changed", appsync.ErrConflict)
	case response.status == http.StatusLocked:
		return response, fmt.Errorf("%w: the cloud copy is locked", appsync.ErrConflict)
	case response.status == http.StatusConflict:
		return response, &statusError{status: response.status, err: appsync.ErrConflict}
	case response.status == http.StatusTooManyRequests:
		return response, appsync.ErrRateLimited
	case response.status == http.StatusBadRequest:
		return response, &statusError{status: response.status, err: appsync.ErrInvalidEndpoint}
	case response.status >= 500:
		return response, fmt.Errorf("%w: the endpoint returned HTTP %d", appsync.ErrNetwork, response.status)
	default:
		return response, &statusError{status: response.status, err: appsync.ErrNetwork}
	}
}

// mapTransportError classifies TLS and redirect failures ahead of the generic
// network bucket. Certificate verification is never skipped, so a bad chain
// must reach the user as ErrTLS rather than "network".
func mapTransportError(err error) error {
	if errors.Is(err, appsync.ErrRedirectRefused) {
		return fmt.Errorf("%w: %v", appsync.ErrRedirectRefused, err)
	}
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, verification)
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, unknownAuthority)
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, hostname)
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, invalid)
	}
	var systemRoots x509.SystemRootsError
	if errors.As(err, &systemRoots) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, systemRoots)
	}
	var record tls.RecordHeaderError
	if errors.As(err, &record) {
		return fmt.Errorf("%w: %v", appsync.ErrTLS, record)
	}
	return fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
}

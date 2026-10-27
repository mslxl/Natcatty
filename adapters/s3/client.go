// This file is the client used by the sync service: the production
// counterpart of the signer, talking to the fake or to a real S3-compatible
// endpoint over HTTP with SigV4. The package documentation lives in sigv4.go.
package s3

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
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
	// defaultTimeout bounds one S3 request.
	defaultTimeout = 60 * time.Second
	// maxBodyBytes bounds the sync file and error bodies read into memory.
	maxBodyBytes = 64 << 20
	// maxKeyBytes keeps an unreasonable prefix from producing an unusable URL.
	maxKeyBytes = 1024
)

// Options configure Client. The zero value uses a standard http.Client with
// system trust roots. HTTPClient lets tests trust a local test CA; the
// production path never sets InsecureSkipVerify, and a plain-HTTP endpoint is
// gated by the application service before Connect is called.
type Options struct {
	HTTPClient *http.Client
	// Now is the signing clock. Tests pin it; production uses time.Now.
	Now func() time.Time
}

// Client is the S3 adapter. It holds no per-profile state: every call receives
// the credential through domainsync.Token, so one Client serves any number of
// profiles and is safe from any goroutine.
type Client struct {
	http *http.Client
	now  func() time.Time
}

var (
	_ appsync.Remote      = (*Client)(nil)
	_ appsync.S3Connector = (*Client)(nil)
)

// New returns a Client.
func New(opts Options) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{http: httpClient, now: now}
}

// NormalizeEndpoint validates an S3 endpoint and returns its canonical form.
// A URL with no scheme is treated as HTTPS. Credentials in the URL, a query
// string, a fragment, or a non-HTTP scheme are refused. A base path is
// honored: <endpoint>/<base>/<bucket>/<key>.
func NormalizeEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: enter an S3 endpoint URL", appsync.ErrInvalidEndpoint)
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
		return "", fmt.Errorf("%w: put the signing key in the fields, not in the URL", appsync.ErrInvalidEndpoint)
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
	parsed.Path = strings.TrimRight(cleaned, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// NormalizePrefix canonicalizes the object-key prefix: surrounding slashes and
// spaces are removed, repeated slashes collapse, and "." segments are dropped.
// ".." and control characters are refused rather than encoded into a key some
// S3-compatible servers interpret as a path traversal.
func NormalizePrefix(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	segments := strings.Split(trimmed, "/")
	kept := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("%w: a prefix segment may not be \"..\"", appsync.ErrInvalidEndpoint)
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("%w: the prefix contains a control character", appsync.ErrInvalidEndpoint)
			}
		}
		kept = append(kept, segment)
	}
	prefix := strings.Join(kept, "/")
	if len(prefix)+len(domainsync.SyncFileName)+1 > maxKeyBytes {
		return "", fmt.Errorf("%w: the prefix is too long", appsync.ErrInvalidEndpoint)
	}
	return prefix, nil
}

// ObjectKey returns the object key for a normalized prefix.
func ObjectKey(prefix string) string {
	if prefix == "" {
		return domainsync.SyncFileName
	}
	return prefix + "/" + domainsync.SyncFileName
}

// Prepare validates and normalizes form settings and reports whether the
// endpoint would send the signing material without transport encryption.
func (c *Client) Prepare(settings appsync.S3Settings) (appsync.S3Settings, bool, error) {
	endpoint, err := NormalizeEndpoint(settings.Endpoint)
	if err != nil {
		return settings, false, err
	}
	prefix, err := NormalizePrefix(settings.Prefix)
	if err != nil {
		return settings, false, err
	}
	region := strings.TrimSpace(settings.Region)
	if err := validateRegion(region); err != nil {
		return settings, false, err
	}
	bucket := strings.TrimSpace(settings.Bucket)
	if err := validateBucket(bucket); err != nil {
		return settings, false, err
	}
	accessKeyID := strings.TrimSpace(settings.AccessKeyID)
	if accessKeyID == "" {
		return settings, false, fmt.Errorf("%w: enter an access key id", appsync.ErrInvalidEndpoint)
	}
	if strings.ContainsAny(accessKeyID, " \t\r\n/") {
		return settings, false, fmt.Errorf("%w: the access key id contains whitespace or a slash", appsync.ErrInvalidEndpoint)
	}
	secret := settings.SecretAccessKey
	if secret == "" {
		return settings, false, fmt.Errorf("%w: enter a secret access key", appsync.ErrInvalidEndpoint)
	}
	normalized := appsync.S3Settings{
		Endpoint:        endpoint,
		Region:          region,
		Bucket:          bucket,
		Prefix:          prefix,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secret,
		SessionToken:    strings.TrimSpace(settings.SessionToken),
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

func validateRegion(region string) error {
	if region == "" {
		return fmt.Errorf("%w: enter a region", appsync.ErrInvalidEndpoint)
	}
	if len(region) > 64 {
		return fmt.Errorf("%w: the region is too long", appsync.ErrInvalidEndpoint)
	}
	for _, r := range region {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%w: the region may contain only letters, digits, '-', '_', and '.'", appsync.ErrInvalidEndpoint)
		}
	}
	return nil
}

func validateBucket(bucket string) error {
	if bucket == "" {
		return fmt.Errorf("%w: enter a bucket", appsync.ErrInvalidEndpoint)
	}
	if len(bucket) > 255 {
		return fmt.Errorf("%w: the bucket name is too long", appsync.ErrInvalidEndpoint)
	}
	if bucket == "." || bucket == ".." {
		return fmt.Errorf("%w: the bucket name is invalid", appsync.ErrInvalidEndpoint)
	}
	for _, r := range bucket {
		if r <= 0x20 || r == 0x7f || r == '/' {
			return fmt.Errorf("%w: the bucket name contains a space, a slash, or a control character", appsync.ErrInvalidEndpoint)
		}
	}
	return nil
}

// Connect verifies the signing material and returns the credential blob and
// the account identity. A 401 or 403 is ErrCredentialsRejected; the caller
// turns it into an actionable reconfigure state instead of retrying the same
// credential.
func (c *Client) Connect(ctx context.Context, settings appsync.S3Settings) (domainsync.Token, appsync.Grant, error) {
	normalized, _, err := c.Prepare(settings)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	token := domainsync.Token{
		Provider:  appsync.ProviderS3,
		TokenType: "S3",
		S3: &domainsync.S3Credential{
			Endpoint:        normalized.Endpoint,
			Region:          normalized.Region,
			Bucket:          normalized.Bucket,
			Prefix:          normalized.Prefix,
			AccessKeyID:     normalized.AccessKeyID,
			SecretAccessKey: normalized.SecretAccessKey,
			SessionToken:    normalized.SessionToken,
		},
	}
	grant, err := c.User(ctx, token)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	return token, grant, nil
}

// User verifies access to the bucket by probing the sync object. A missing
// object is a valid empty account, so 404 NoSuchKey succeeds; a missing bucket
// is a wrong endpoint. The identity is the endpoint plus bucket plus prefix:
// two prefixes in one bucket, or two endpoints, must not share a merge anchor.
func (c *Client) User(ctx context.Context, token domainsync.Token) (appsync.Grant, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return appsync.Grant{}, err
	}
	if err := c.probe(ctx, credential); err != nil {
		return appsync.Grant{}, err
	}
	return grantFor(credential)
}

func (c *Client) probe(ctx context.Context, credential *domainsync.S3Credential) error {
	_, err := c.do(ctx, http.MethodHead, credential, nil, nil)
	if err == nil {
		return nil
	}
	var api *apiError
	if errors.As(err, &api) && api.status == http.StatusMethodNotAllowed {
		// Some S3-compatible servers do not implement HEAD. A GET is the
		// fallback and has the same success and empty-state meaning.
		_, getErr := c.do(ctx, http.MethodGet, credential, nil, nil)
		if getErr == nil || errors.Is(getErr, errNotFound) {
			return nil
		}
		return getErr
	}
	if !errors.Is(err, errNotFound) {
		return err
	}
	// A HEAD 404 carries no body, so it cannot distinguish a missing object
	// from a missing bucket. A GET can, and a missing object is the normal
	// empty state for a fresh profile.
	_, getErr := c.do(ctx, http.MethodGet, credential, nil, nil)
	if getErr == nil || errors.Is(getErr, errNotFound) {
		return nil
	}
	return getErr
}

func grantFor(credential *domainsync.S3Credential) (appsync.Grant, error) {
	endpoint, err := NormalizeEndpoint(credential.Endpoint)
	if err != nil {
		return appsync.Grant{}, err
	}
	prefix, err := NormalizePrefix(credential.Prefix)
	if err != nil {
		return appsync.Grant{}, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return appsync.Grant{}, fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	login := credential.Bucket + "@" + parsed.Host
	identity := endpoint + "/" + credential.Bucket + "/" + ObjectKey(prefix)
	if prefix != "" {
		login = credential.Bucket + "/" + prefix + "@" + parsed.Host
	}
	return appsync.Grant{ID: identity, Login: login}, nil
}

// Read downloads the sync file. A 404 NoSuchKey is the empty state; a 404
// NoSuchBucket is a wrong endpoint. The ETag is the conditional-write
// validator used by the next update.
func (c *Client) Read(ctx context.Context, token domainsync.Token) (appsync.RemoteFile, bool, error) {
	credential, err := credentialFor(token)
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	response, err := c.do(ctx, http.MethodGet, credential, nil, nil)
	if errors.Is(err, errNotFound) {
		return appsync.RemoteFile{}, false, nil
	}
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	objectURL, _, err := objectAddress(credential)
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	return appsync.RemoteFile{
		ID:        objectURL,
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
			return appsync.RemoteFile{}, fmt.Errorf("%w: the object was deleted after the read", appsync.ErrConflict)
		}
		if !bytes.Equal(existing.Content, current.Content) {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the object changed after the read", appsync.ErrConflict)
		}
	}

	response, err := c.do(ctx, http.MethodPut, credential, content, headers)
	if err != nil {
		if errors.Is(err, errNotFound) && current.ID == "" {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the bucket does not exist", appsync.ErrInvalidEndpoint)
		}
		if errors.Is(err, errNotFound) {
			return appsync.RemoteFile{}, fmt.Errorf("%w: the object was deleted after the read", appsync.ErrConflict)
		}
		return appsync.RemoteFile{}, err
	}

	etag := response.header.Get("ETag")
	if etag == "" {
		// Not every S3-compatible server returns the new validator on PUT. A
		// HEAD keeps the next update conditional; failure here is not a write
		// failure.
		if head, headErr := c.do(ctx, http.MethodHead, credential, nil, nil); headErr == nil {
			etag = head.header.Get("ETag")
		}
	}
	objectURL, _, err := objectAddress(credential)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	id := current.ID
	if id == "" {
		id = objectURL
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

func credentialFor(token domainsync.Token) (*domainsync.S3Credential, error) {
	if token.S3 == nil || strings.TrimSpace(token.S3.Endpoint) == "" {
		return nil, fmt.Errorf("%w: no S3 credential", appsync.ErrNotConfigured)
	}
	return token.S3, nil
}

// response is one fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// errNotFound marks a missing object; Read and User translate it.
var errNotFound = errors.New("s3: object not found")

// apiError carries the HTTP status and S3 error code alongside a sentinel.
type apiError struct {
	status int
	code   string
	err    error
}

func (e *apiError) Error() string {
	if e.code == "" {
		return fmt.Sprintf("S3 HTTP %d: %v", e.status, e.err)
	}
	return fmt.Sprintf("S3 HTTP %d %s: %v", e.status, e.code, e.err)
}

func (e *apiError) Unwrap() error { return e.err }

// objectAddress returns the escaped object URL and its key.
func objectAddress(credential *domainsync.S3Credential) (string, string, error) {
	endpoint, err := NormalizeEndpoint(credential.Endpoint)
	if err != nil {
		return "", "", err
	}
	prefix, err := NormalizePrefix(credential.Prefix)
	if err != nil {
		return "", "", err
	}
	if err := validateBucket(credential.Bucket); err != nil {
		return "", "", err
	}
	key := ObjectKey(prefix)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	decodedPath := strings.TrimRight(parsed.Path, "/") + "/" + credential.Bucket + "/" + key
	parsed.Path = decodedPath
	parsed.RawPath = uriEncode(decodedPath, false)
	return parsed.String(), key, nil
}

// do signs and performs one request. It never includes the Authorization
// value, the session token, or the secret in an error.
func (c *Client) do(
	ctx context.Context,
	method string,
	credential *domainsync.S3Credential,
	payload []byte,
	headers map[string]string,
) (*response, error) {
	target, _, err := objectAddress(credential)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	at := c.now().UTC()
	amzDate := at.Format("20060102T150405Z")
	payloadHash := PayloadSHA256(payload)
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", amzDate)
	if token := strings.TrimSpace(credential.SessionToken); token != "" {
		request.Header.Set("x-amz-security-token", token)
	}
	signed, err := Sign(SignInput{
		Method:      method,
		Host:        request.URL.Host,
		URI:         request.URL.Path,
		Query:       request.URL.RawQuery,
		Headers:     request.Header,
		PayloadHash: payloadHash,
		Region:      credential.Region,
		Service:     Service,
		Time:        at,
	}, Credentials{
		AccessKeyID:     credential.AccessKeyID,
		SecretAccessKey: credential.SecretAccessKey,
		SessionToken:    credential.SessionToken,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", appsync.ErrInvalidEndpoint, err)
	}
	request.Header.Set("Authorization", signed.Authorization)

	httpResponse, err := c.client().Do(request)
	if err != nil {
		return nil, mapTransportError(err)
	}
	defer httpResponse.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxBodyBytes))
	if readErr != nil {
		return nil, fmt.Errorf("%w: %v", appsync.ErrNetwork, readErr)
	}
	return c.mapStatus(&response{status: httpResponse.StatusCode, header: httpResponse.Header, body: body})
}

// client returns a copy of the configured client that never follows a
// redirect. A 301 region redirect is interpreted, not chased: forwarding the
// signed request elsewhere would send a credential to a host it was not
// entered for, and the original SDK client did not follow it either.
func (c *Client) client() *http.Client {
	base := c.http
	if base == nil {
		base = &http.Client{Timeout: defaultTimeout}
	}
	clone := *base
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

// mapStatus turns a final response into the adapter's result or typed error.
func (c *Client) mapStatus(response *response) (*response, error) {
	code := s3ErrorCode(response.body)
	if response.status >= 200 && response.status < 300 {
		return response, nil
	}
	switch {
	case response.status == http.StatusMovedPermanently,
		response.status == http.StatusTemporaryRedirect,
		response.status == http.StatusPermanentRedirect:
		if region := response.header.Get("x-amz-bucket-region"); region != "" {
			return response, fmt.Errorf("%w: the bucket is in region %s; update the region", appsync.ErrWrongRegion, region)
		}
		return response, fmt.Errorf("%w: the endpoint redirected the request", appsync.ErrWrongRegion)
	case response.status == http.StatusBadRequest && (code == "AuthorizationHeaderMalformed" || code == "PermanentRedirect"):
		if region := response.header.Get("x-amz-bucket-region"); region != "" {
			return response, fmt.Errorf("%w: the bucket is in region %s; update the region", appsync.ErrWrongRegion, region)
		}
		return response, &apiError{status: response.status, code: code, err: appsync.ErrWrongRegion}
	case response.status == http.StatusUnauthorized:
		return response, &apiError{status: response.status, code: code, err: appsync.ErrCredentialsRejected}
	case response.status == http.StatusForbidden:
		// Every 403 is actionable, but the two causes need different words.
		// AccessDenied means the signature was valid and the bucket policy
		// refused the operation, so re-entering the key cannot help; every
		// other 403 (SignatureDoesNotMatch, InvalidAccessKeyId, InvalidToken,
		// a disabled key) is a signing-material problem. Neither is retried
		// and neither starts a credential refresh loop.
		if code == "AccessDenied" {
			return response, fmt.Errorf("%w: S3 denied access to the bucket or object", appsync.ErrForbidden)
		}
		return response, fmt.Errorf("%w: S3 refused the signing credential (%s)", appsync.ErrCredentialsRejected, displayCode(code))
	case response.status == http.StatusNotFound:
		if code == "NoSuchBucket" {
			return response, &apiError{status: response.status, code: code, err: appsync.ErrInvalidEndpoint}
		}
		// NoSuchKey (or a bodyless 404) is the empty state; Read translates
		// it to exists=false, and Write treats a lost object as a conflict.
		return response, &apiError{status: response.status, code: code, err: errNotFound}
	case response.status == http.StatusPreconditionFailed:
		return response, fmt.Errorf("%w: the cloud object changed", appsync.ErrConflict)
	case response.status == http.StatusTooManyRequests:
		return response, fmt.Errorf("%w: S3 rate limited the request", appsync.ErrRateLimited)
	case response.status == http.StatusServiceUnavailable && (code == "SlowDown" || code == "RequestLimitExceeded"):
		return response, fmt.Errorf("%w: S3 asked the client to slow down", appsync.ErrRateLimited)
	case response.status >= 500:
		return response, fmt.Errorf("%w: S3 returned HTTP %d (%s)", appsync.ErrNetwork, response.status, displayCode(code))
	case response.status == http.StatusBadRequest:
		return response, &apiError{status: response.status, code: code, err: appsync.ErrInvalidEndpoint}
	default:
		return response, &apiError{status: response.status, code: code, err: appsync.ErrNetwork}
	}
}

func displayCode(code string) string {
	if code == "" {
		return "no error code"
	}
	return code
}

// s3ErrorCode extracts <Code> from an S3 error document. The body never
// contains the signing material.
func s3ErrorCode(body []byte) string {
	var document struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &document); err != nil {
		return ""
	}
	return strings.TrimSpace(document.Code)
}

// mapTransportError classifies TLS failures ahead of the generic network
// bucket. Certificate verification is never skipped. Redirects never reach
// here: client() answers a 3xx as a response rather than following it.
func mapTransportError(err error) error {
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

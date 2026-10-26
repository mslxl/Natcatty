// Package github is the GitHub cloud-sync adapter. It implements the device
// authorization grant (RFC 8628) and the Gist read/write surface the sync
// service consumes. Both base URLs are injectable so an acceptance run can use
// a local fake server instead of the real network.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// Defaults match the original Electron app.
const (
	DefaultAPIBase  = "https://api.github.com"
	DefaultAuthBase = "https://github.com"
	DefaultScope    = "gist read:user"

	deviceCodePath = "/login/device/code"
	tokenPath      = "/login/oauth/access_token"
)

// Options configure Client. Zero values use the documented defaults.
type Options struct {
	APIBase    string
	AuthBase   string
	ClientID   string
	Scope      string
	HTTPClient *http.Client
}

// Client is the GitHub adapter. It is safe from any goroutine; the conditional
// read cache is guarded.
type Client struct {
	apiBase  string
	authBase string
	clientID string
	scope    string
	http     *http.Client

	mu    sync.Mutex
	cache map[string]cachedFile
}

type cachedFile struct {
	gistID    string
	etag      string
	content   []byte
	updatedAt int64
}

var _ appsync.Remote = (*Client)(nil)

// New returns a Client. APIBase and AuthBase are trimmed of a trailing slash.
func New(opts Options) *Client {
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	authBase := strings.TrimRight(strings.TrimSpace(opts.AuthBase), "/")
	if authBase == "" {
		authBase = DefaultAuthBase
	}
	scope := opts.Scope
	if scope == "" {
		scope = DefaultScope
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		apiBase:  apiBase,
		authBase: authBase,
		clientID: opts.ClientID,
		scope:    scope,
		http:     httpClient,
		cache:    make(map[string]cachedFile),
	}
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

// RequestDeviceCode starts the grant.
func (c *Client) RequestDeviceCode(ctx context.Context) (appsync.DeviceCode, error) {
	if c.clientID == "" {
		return appsync.DeviceCode{}, fmt.Errorf("%w: no GitHub OAuth client id configured", appsync.ErrNotConfigured)
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("scope", c.scope)
	var response deviceCodeResponse
	if err := c.form(ctx, c.authBase+deviceCodePath, form, &response); err != nil {
		return appsync.DeviceCode{}, err
	}
	if response.DeviceCode == "" || response.UserCode == "" {
		return appsync.DeviceCode{}, fmt.Errorf("%w: device code response missing fields", appsync.ErrNetwork)
	}
	return appsync.DeviceCode{
		DeviceCode:      response.DeviceCode,
		UserCode:        response.UserCode,
		VerificationURI: response.VerificationURI,
		ExpiresIn:       response.ExpiresIn,
		Interval:        response.Interval,
	}, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// PollDeviceToken performs one grant poll. Pending and slow-down are typed so
// the service owns the cadence.
func (c *Client) PollDeviceToken(ctx context.Context, code appsync.DeviceCode) (domainsync.Token, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("device_code", code.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	response, err := c.postForm(ctx, c.authBase+tokenPath, form)
	if err != nil {
		return domainsync.Token{}, err
	}
	var token tokenResponse
	if err := json.Unmarshal(response, &token); err != nil {
		return domainsync.Token{}, fmt.Errorf("%w: decode token response: %v", appsync.ErrNetwork, err)
	}
	if token.AccessToken != "" {
		result := domainsync.Token{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			TokenType:    token.TokenType,
			Scope:        token.Scope,
		}
		if token.ExpiresIn > 0 {
			result.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UnixMilli()
		}
		return result, nil
	}
	switch token.Error {
	case "authorization_pending":
		return domainsync.Token{}, appsync.ErrAuthorizationPending
	case "slow_down":
		return domainsync.Token{}, appsync.ErrSlowDown
	case "expired_token":
		return domainsync.Token{}, appsync.ErrDeviceCodeExpired
	case "access_denied":
		return domainsync.Token{}, appsync.ErrAuthorizationDenied
	case "":
		return domainsync.Token{}, fmt.Errorf("%w: empty token response", appsync.ErrNetwork)
	default:
		return domainsync.Token{}, fmt.Errorf("%w: %s: %s", appsync.ErrUnauthorized, token.Error, token.Description)
	}
}

// Refresh exchanges a refresh token. GitHub device tokens usually do not
// expire, so this is used only by providers or GitHub App user tokens that do.
func (c *Client) Refresh(ctx context.Context, token domainsync.Token) (domainsync.Token, error) {
	if token.RefreshToken == "" {
		return domainsync.Token{}, appsync.ErrUnauthorized
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
	response, err := c.postForm(ctx, c.authBase+tokenPath, form)
	if err != nil {
		return domainsync.Token{}, err
	}
	var refreshed tokenResponse
	if err := json.Unmarshal(response, &refreshed); err != nil {
		return domainsync.Token{}, fmt.Errorf("%w: decode refresh response: %v", appsync.ErrNetwork, err)
	}
	if refreshed.AccessToken == "" {
		return domainsync.Token{}, appsync.ErrUnauthorized
	}
	result := domainsync.Token{
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		TokenType:    refreshed.TokenType,
		Scope:        refreshed.Scope,
	}
	if refreshed.ExpiresIn > 0 {
		result.ExpiresAt = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second).UnixMilli()
	}
	return result, nil
}

type userResponse struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// User fetches the authorized account.
func (c *Client) User(ctx context.Context, token domainsync.Token) (appsync.Grant, error) {
	body, _, err := c.do(ctx, http.MethodGet, c.apiBase+"/user", token, nil, nil)
	if err != nil {
		return appsync.Grant{}, err
	}
	var user userResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return appsync.Grant{}, fmt.Errorf("%w: decode user: %v", appsync.ErrNetwork, err)
	}
	if user.Login == "" {
		return appsync.Grant{}, fmt.Errorf("%w: user response has no login", appsync.ErrNetwork)
	}
	id := ""
	if user.ID != 0 {
		id = strconv.FormatInt(user.ID, 10)
	}
	return appsync.Grant{ID: id, Login: user.Login}, nil
}

type gistFile struct {
	Filename  string `json:"filename"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	RawURL    string `json:"raw_url"`
	Size      int    `json:"size"`
}

type gistResponse struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	UpdatedAt   string              `json:"updated_at"`
	Files       map[string]gistFile `json:"files"`
}

// Read finds the Netcatty Gist, follows a truncated file's raw URL, and caches
// the ETag so the next read can send If-None-Match and accept a 304.
func (c *Client) Read(ctx context.Context, token domainsync.Token) (appsync.RemoteFile, bool, error) {
	c.mu.Lock()
	cached, hasCached := c.cache["gist"]
	c.mu.Unlock()

	gistID := ""
	if hasCached {
		gistID = cached.gistID
	}
	if gistID == "" {
		found, err := c.findGist(ctx, token)
		if err != nil {
			return appsync.RemoteFile{}, false, err
		}
		if found == "" {
			return appsync.RemoteFile{}, false, nil
		}
		gistID = found
	}

	headers := map[string]string{"Accept": "application/vnd.github.v3+json"}
	if hasCached && cached.etag != "" {
		headers["If-None-Match"] = cached.etag
	}
	body, response, err := c.do(ctx, http.MethodGet, c.apiBase+"/gists/"+url.PathEscape(gistID), token, nil, headers)
	if err != nil {
		if errors.Is(err, errNotModified) {
			return appsync.RemoteFile{ID: gistID, ETag: cached.etag, Content: cached.content, UpdatedAt: cached.updatedAt}, true, nil
		}
		if errors.Is(err, errNotFound) {
			c.mu.Lock()
			delete(c.cache, "gist")
			c.mu.Unlock()
			return appsync.RemoteFile{}, false, nil
		}
		return appsync.RemoteFile{}, false, err
	}

	var gist gistResponse
	if err := json.Unmarshal(body, &gist); err != nil {
		return appsync.RemoteFile{}, false, fmt.Errorf("%w: decode gist: %v", appsync.ErrNetwork, err)
	}
	file, ok := gist.Files[domainsync.SyncFileName]
	if !ok {
		return appsync.RemoteFile{}, false, nil
	}
	content := []byte(file.Content)
	if file.Truncated || (file.Size > 0 && file.Size > len(content)) || (len(content) == 0 && file.RawURL != "") {
		if file.RawURL == "" {
			return appsync.RemoteFile{}, false, fmt.Errorf("%w: truncated gist without a raw URL", appsync.ErrRemoteCorrupt)
		}
		content, err = c.raw(ctx, token, file.RawURL)
		if err != nil {
			return appsync.RemoteFile{}, false, err
		}
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		etag = gist.UpdatedAt
	}
	updatedAt := int64(0)
	if envelope, parseErr := domainsync.UnmarshalFile(content); parseErr == nil {
		updatedAt = envelope.Meta.UpdatedAt
	}
	remote := appsync.RemoteFile{ID: gist.ID, ETag: etag, Content: content, UpdatedAt: updatedAt}
	c.mu.Lock()
	c.cache["gist"] = cachedFile{gistID: gist.ID, etag: etag, content: append([]byte(nil), content...), updatedAt: updatedAt}
	c.mu.Unlock()
	return remote, true, nil
}

func (c *Client) findGist(ctx context.Context, token domainsync.Token) (string, error) {
	body, _, err := c.do(ctx, http.MethodGet, c.apiBase+"/gists?per_page=100", token, nil, map[string]string{
		"Accept": "application/vnd.github.v3+json",
	})
	if err != nil {
		return "", err
	}
	var gists []gistResponse
	if err := json.Unmarshal(body, &gists); err != nil {
		return "", fmt.Errorf("%w: decode gist list: %v", appsync.ErrNetwork, err)
	}
	for _, gist := range gists {
		if gist.Description != domainsync.GistDescription {
			continue
		}
		if _, ok := gist.Files[domainsync.SyncFileName]; ok {
			return gist.ID, nil
		}
	}
	return "", nil
}

// Write creates or conditionally updates the Gist. The description is kept at
// the original value so an existing install can still find the file.
func (c *Client) Write(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	files := map[string]any{
		domainsync.SyncFileName: map[string]string{"content": string(content)},
	}
	if current.ID == "" {
		payload := map[string]any{
			"description": domainsync.GistDescription,
			"public":      false,
			"files":       files,
		}
		body, response, err := c.do(ctx, http.MethodPost, c.apiBase+"/gists", token, payload, map[string]string{
			"Accept":       "application/vnd.github.v3+json",
			"Content-Type": "application/json",
		})
		if err != nil {
			return appsync.RemoteFile{}, err
		}
		var gist gistResponse
		if err := json.Unmarshal(body, &gist); err != nil {
			return appsync.RemoteFile{}, fmt.Errorf("%w: decode created gist: %v", appsync.ErrNetwork, err)
		}
		etag := response.Header.Get("ETag")
		if etag == "" {
			etag = gist.UpdatedAt
		}
		return appsync.RemoteFile{ID: gist.ID, ETag: etag, Content: content, UpdatedAt: updatedAtOf(content)}, nil
	}

	headers := map[string]string{
		"Accept":       "application/vnd.github.v3+json",
		"Content-Type": "application/json",
	}
	if current.ETag != "" {
		headers["If-Match"] = current.ETag
	}
	body, response, err := c.do(ctx, http.MethodPatch, c.apiBase+"/gists/"+url.PathEscape(current.ID), token, map[string]any{
		"files": files,
	}, headers)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	var gist gistResponse
	if err := json.Unmarshal(body, &gist); err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: decode updated gist: %v", appsync.ErrNetwork, err)
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		etag = gist.UpdatedAt
	}
	result := appsync.RemoteFile{ID: current.ID, ETag: etag, Content: content, UpdatedAt: updatedAtOf(content)}
	if gist.ID != "" {
		result.ID = gist.ID
	}
	c.mu.Lock()
	delete(c.cache, "gist")
	c.mu.Unlock()
	return result, nil
}

func updatedAtOf(content []byte) int64 {
	if envelope, err := domainsync.UnmarshalFile(content); err == nil {
		return envelope.Meta.UpdatedAt
	}
	return 0
}

func (c *Client) raw(ctx context.Context, token domainsync.Token, rawURL string) ([]byte, error) {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		// A fake server may return a root-relative URL. Join it with the API
		// base rather than following an arbitrary scheme.
		rawURL = c.apiBase + "/" + strings.TrimLeft(rawURL, "/")
	}
	body, _, err := c.do(ctx, http.MethodGet, rawURL, token, nil, map[string]string{
		"Accept": "application/vnd.github.raw",
	})
	return body, err
}

// errNotModified and errNotFound are internal markers the HTTP helper returns;
// Read translates them into the port's shape.
var (
	errNotModified = errors.New("github: not modified")
	errNotFound    = errors.New("github: not found")
)

func (c *Client) form(ctx context.Context, endpoint string, form url.Values, target any) error {
	body, err := c.postForm(ctx, endpoint, form)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("%w: decode response: %v", appsync.ErrNetwork, err)
	}
	return nil
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	body, _, err := c.do(ctx, http.MethodPost, endpoint, domainsync.Token{}, []byte(form.Encode()), map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/x-www-form-urlencoded",
	})
	return body, err
}

// do performs one request and maps transport and GitHub errors onto the
// application's sentinels. A JSON payload or already-encoded form body is sent
// verbatim.
func (c *Client) do(
	ctx context.Context,
	method string,
	endpoint string,
	token domainsync.Token,
	payload any,
	headers map[string]string,
) ([]byte, *http.Response, error) {
	var reader io.Reader
	switch body := payload.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(body)
	default:
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if token.AccessToken != "" {
		request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if readErr != nil {
		return nil, response, fmt.Errorf("%w: read response: %v", appsync.ErrNetwork, readErr)
	}
	switch {
	case response.StatusCode == http.StatusNotModified:
		return nil, response, errNotModified
	case response.StatusCode == http.StatusNotFound:
		return body, response, errNotFound
	case response.StatusCode == http.StatusUnauthorized:
		return body, response, appsync.ErrUnauthorized
	case response.StatusCode == http.StatusPreconditionFailed:
		return body, response, appsync.ErrConflict
	case response.StatusCode == http.StatusTooManyRequests:
		return body, response, appsync.ErrRateLimited
	case response.StatusCode == http.StatusForbidden && isRateLimit(body, response):
		return body, response, appsync.ErrRateLimited
	case response.StatusCode >= 500:
		return body, response, fmt.Errorf("%w: GitHub returned %d", appsync.ErrNetwork, response.StatusCode)
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return body, response, fmt.Errorf("%w: GitHub returned %d", appsync.ErrNetwork, response.StatusCode)
	}
	return body, response, nil
}

func isRateLimit(body []byte, response *http.Response) bool {
	if response.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection")
}

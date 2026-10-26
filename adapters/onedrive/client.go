// Package onedrive is the Microsoft OneDrive cloud-sync adapter. It implements
// the OAuth 2.0 authorization-code grant with PKCE in the system browser and
// the OneDrive approot (app folder) file surface the sync service consumes.
// Every endpoint is injectable so tests and acceptance runs use a local fake
// instead of Microsoft.
//
// The adapter never sends a client secret: the installed-app flow uses only
// the public client id and the PKCE verifier.
package onedrive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mslxl/natcatty/adapters/oauthloopback"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// Defaults mirror the original Electron app's Microsoft endpoints and scope.
const (
	DefaultAuthBase  = "https://login.microsoftonline.com/consumers/oauth2/v2.0"
	DefaultTokenBase = "https://login.microsoftonline.com/consumers/oauth2/v2.0"
	DefaultGraphBase = "https://graph.microsoft.com/v1.0"
	DefaultScope     = "https://graph.microsoft.com/Files.ReadWrite.AppFolder " +
		"https://graph.microsoft.com/User.Read offline_access"

	authorizePath = "/authorize"
	tokenPath     = "/token"
	approotPath   = "/me/drive/special/approot:"
	itemsPath     = "/me/drive/items/"

	defaultAuthTimeout = 5 * time.Minute

	// Graph can briefly 404 a file that another device uploaded seconds
	// ago. Treating the first 404 as authoritative would let the service
	// try to create over the peer's copy; retry a bounded number of times
	// with a short backoff instead, matching the original adapter's #779
	// behavior.
	notFoundRetries        = 2
	defaultNotFoundBackoff = 1500 * time.Millisecond
)

// Environment variables override the endpoints and client id. They let the
// selftest and GUI acceptance runs point the whole app at a local fake
// Microsoft without real credentials or network access.
const (
	EnvAuthBase  = "NATCATTY_ONEDRIVE_AUTH_BASE"
	EnvTokenBase = "NATCATTY_ONEDRIVE_TOKEN_BASE"
	EnvGraphBase = "NATCATTY_ONEDRIVE_GRAPH_BASE"
	EnvClientID  = "NATCATTY_ONEDRIVE_CLIENT_ID"
)

// Options configure Client. Zero values use the documented defaults.
type Options struct {
	// AuthBase is the authorize origin, such as
	// https://login.microsoftonline.com/consumers/oauth2/v2.0.
	AuthBase string
	// TokenBase is the token origin; when empty it follows AuthBase for a
	// fake override, otherwise the documented default.
	TokenBase string
	// GraphBase is the Microsoft Graph base URL.
	GraphBase string
	ClientID  string
	Scope     string
	// AuthTimeout bounds the wait for the loopback redirect after the browser
	// opens. The zero value is five minutes.
	AuthTimeout time.Duration
	// NotFoundBackoff is the pause before retrying a Graph 404, multiplied by
	// the attempt number. A zero value is 1.5 seconds, matching the original
	// adapter's eventual-consistency window; tests use a shorter pause.
	NotFoundBackoff time.Duration
	HTTPClient      *http.Client
	// Now is the clock used for token expiry. Tests may substitute it.
	Now func() time.Time
	// Random feeds PKCE generation.
	Random io.Reader
}

// OptionsFromEnv reads the fake-endpoint overrides used by tests, the
// fakeonedrive tool, and GUI acceptance runs.
func OptionsFromEnv() Options {
	return Options{
		AuthBase:  os.Getenv(EnvAuthBase),
		TokenBase: os.Getenv(EnvTokenBase),
		GraphBase: os.Getenv(EnvGraphBase),
		ClientID:  os.Getenv(EnvClientID),
	}
}

// Client is the OneDrive adapter. It is safe from any goroutine.
type Client struct {
	authBase        string
	tokenBase       string
	graphBase       string
	clientID        string
	scope           string
	authTimeout     time.Duration
	notFoundBackoff time.Duration
	http            *http.Client
	now             func() time.Time
	random          io.Reader
}

var (
	_ appsync.Remote            = (*Client)(nil)
	_ appsync.BrowserAuthorizer = (*Client)(nil)
	_ appsync.TokenRefresher    = (*Client)(nil)
)

// New returns a Client. Base URLs are trimmed of a trailing slash.
func New(opts Options) *Client {
	authBase := strings.TrimRight(strings.TrimSpace(opts.AuthBase), "/")
	if authBase == "" {
		authBase = DefaultAuthBase
	}
	tokenBase := strings.TrimRight(strings.TrimSpace(opts.TokenBase), "/")
	if tokenBase == "" {
		if strings.TrimSpace(opts.AuthBase) != "" {
			// A fake usually serves both OAuth endpoints under one origin.
			tokenBase = authBase
		} else {
			tokenBase = DefaultTokenBase
		}
	}
	graphBase := strings.TrimRight(strings.TrimSpace(opts.GraphBase), "/")
	if graphBase == "" {
		graphBase = DefaultGraphBase
	}
	scope := opts.Scope
	if scope == "" {
		scope = DefaultScope
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	authTimeout := opts.AuthTimeout
	if authTimeout <= 0 {
		authTimeout = defaultAuthTimeout
	}
	notFoundBackoff := opts.NotFoundBackoff
	if notFoundBackoff <= 0 {
		notFoundBackoff = defaultNotFoundBackoff
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	return &Client{
		authBase:        authBase,
		tokenBase:       tokenBase,
		graphBase:       graphBase,
		clientID:        strings.TrimSpace(opts.ClientID),
		scope:           scope,
		authTimeout:     authTimeout,
		notFoundBackoff: notFoundBackoff,
		http:            httpClient,
		now:             now,
		random:          random,
	}
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

// Authorize runs the authorization-code flow with PKCE. It opens the consent
// URL in the system browser through open, listens on an ephemeral 127.0.0.1
// port, validates the redirect's state, exchanges the code with the verifier,
// and returns the authorized account. Microsoft consumer accounts use the
// consumers tenant; no client secret is sent.
func (c *Client) Authorize(ctx context.Context, open func(string) error) (domainsync.Token, appsync.Grant, error) {
	if c.clientID == "" {
		return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: no OneDrive OAuth client id configured", appsync.ErrNotConfigured)
	}
	flow := oauthloopback.Flow{
		Provider:      "Microsoft",
		AuthBase:      c.authBase,
		AuthorizePath: authorizePath,
		Scope:         c.scope,
		ClientID:      c.clientID,
		ExtraQuery: url.Values{
			"response_mode": {"query"},
			"prompt":        {"consent"},
		},
		Timeout: c.authTimeout,
		Random:  c.random,
	}
	authorization, err := flow.Authorize(ctx, open)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	token, err := c.exchange(ctx, authorization.Code, authorization.Verifier, authorization.RedirectURI)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	grant, err := c.User(ctx, token)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	return token, grant, nil
}

func (c *Client) exchange(ctx context.Context, code, verifier, redirectURI string) (domainsync.Token, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("scope", c.scope)
	return c.tokenRequest(ctx, form, tokenPurposeCode)
}

// Refresh exchanges a refresh token. Microsoft rotates consumer refresh tokens
// on most refreshes; a returned replacement is passed through and an absent
// one is left empty for the application to carry forward. invalid_grant and
// the consent/interaction codes are typed so the service starts a fresh
// browser authorization instead of retrying forever.
func (c *Client) Refresh(ctx context.Context, token domainsync.Token) (domainsync.Token, error) {
	if token.RefreshToken == "" {
		return domainsync.Token{}, appsync.ErrReauthorizationRequired
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
	form.Set("scope", c.scope)
	return c.tokenRequest(ctx, form, tokenPurposeRefresh)
}

type tokenPurpose int

const (
	tokenPurposeCode tokenPurpose = iota
	tokenPurposeRefresh
)

func (c *Client) tokenRequest(ctx context.Context, form url.Values, purpose tokenPurpose) (domainsync.Token, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenBase+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return domainsync.Token{}, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return domainsync.Token{}, ctx.Err()
		}
		return domainsync.Token{}, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return domainsync.Token{}, fmt.Errorf("%w: read the token response: %v", appsync.ErrNetwork, readErr)
	}
	var payload tokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		if response.StatusCode >= 500 {
			return domainsync.Token{}, fmt.Errorf("%w: Microsoft returned %d", appsync.ErrNetwork, response.StatusCode)
		}
		return domainsync.Token{}, fmt.Errorf("%w: decode the token response: %v", appsync.ErrNetwork, err)
	}
	if payload.AccessToken == "" {
		return domainsync.Token{}, tokenError(payload, response.StatusCode, purpose)
	}
	result := domainsync.Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
	}
	if payload.ExpiresIn > 0 {
		result.ExpiresAt = c.now().Add(time.Duration(payload.ExpiresIn) * time.Second).UnixMilli()
	}
	return result, nil
}

func tokenError(payload tokenResponse, status int, purpose tokenPurpose) error {
	switch payload.Error {
	case "invalid_grant", "interaction_required", "consent_required", "login_required":
		// These mean the refresh token itself is no longer usable (expired,
		// revoked, or consent withdrawn); retrying cannot recover.
		if purpose == tokenPurposeRefresh {
			return fmt.Errorf("%w: %s", appsync.ErrReauthorizationRequired, payload.Description)
		}
		return fmt.Errorf("%w: %s", appsync.ErrAuthorizationDenied, payload.Description)
	case "access_denied":
		return fmt.Errorf("%w: %s", appsync.ErrAuthorizationDenied, payload.Description)
	case "invalid_client", "unauthorized_client":
		return fmt.Errorf("%w: Microsoft rejected the OAuth client id", appsync.ErrNotConfigured)
	case "temporarily_unavailable":
		return fmt.Errorf("%w: Microsoft returned %d", appsync.ErrNetwork, status)
	case "":
		return fmt.Errorf("%w: Microsoft returned %d", appsync.ErrNetwork, status)
	default:
		return fmt.Errorf("%w: %s: %s", appsync.ErrUnauthorized, payload.Error, payload.Description)
	}
}

type userResponse struct {
	ID                string `json:"id"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
	DisplayName       string `json:"displayName"`
}

// User returns the authorized Microsoft account. The Graph user id is the
// merge-anchor identity; it is immutable, unlike displayName or mail. The mail
// (falling back to the user principal name) is the displayed identity.
func (c *Client) User(ctx context.Context, token domainsync.Token) (appsync.Grant, error) {
	body, err := c.do(ctx, http.MethodGet, c.graphBase+"/me", token, nil, nil)
	if err != nil {
		return appsync.Grant{}, err
	}
	var user userResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return appsync.Grant{}, fmt.Errorf("%w: decode the Microsoft account: %v", appsync.ErrNetwork, err)
	}
	login := user.Mail
	if login == "" {
		login = user.UserPrincipalName
	}
	if login == "" {
		login = user.DisplayName
	}
	if user.ID == "" && login == "" {
		return appsync.Grant{}, fmt.Errorf("%w: the Microsoft account has no identity", appsync.ErrNetwork)
	}
	return appsync.Grant{ID: user.ID, Login: login}, nil
}

type driveItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ETag         string `json:"eTag"`
	CTag         string `json:"cTag"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModifiedDateTime"`
	DownloadURL  string `json:"@microsoft.graph.downloadUrl"`
}

// Read finds the product file in the approot special folder and downloads it.
// exists is false when the account has no copy after the bounded transient-404
// retries, including a file deleted between the metadata read and the
// download.
func (c *Client) Read(ctx context.Context, token domainsync.Token) (appsync.RemoteFile, bool, error) {
	item, found, err := c.findFile(ctx, token)
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	if !found {
		return appsync.RemoteFile{}, false, nil
	}
	content, err := c.download(ctx, token, item)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return appsync.RemoteFile{}, false, nil
		}
		return appsync.RemoteFile{}, false, err
	}
	return appsync.RemoteFile{
		ID:        item.ID,
		ETag:      item.ETag,
		Content:   content,
		UpdatedAt: updatedAtOf(content),
	}, true, nil
}

// findFile addresses the product file by path inside the approot special
// folder. OneDrive resolves the folder even before anything is uploaded, so a
// missing file is a plain 404 rather than an absent folder; a file elsewhere
// in the drive is invisible to sync. Because Graph can briefly 404 a file a
// peer uploaded seconds ago, a 404 is retried a bounded number of times before
// it is accepted as "no copy".
func (c *Client) findFile(ctx context.Context, token domainsync.Token) (driveItem, bool, error) {
	for attempt := 0; ; attempt++ {
		item, found, err := c.findFileOnce(ctx, token)
		if err != nil {
			return driveItem{}, false, err
		}
		if found {
			return item, true, nil
		}
		if attempt >= notFoundRetries {
			return driveItem{}, false, nil
		}
		if err := c.waitNotFound(ctx, attempt+1); err != nil {
			return driveItem{}, false, err
		}
	}
}

func (c *Client) findFileOnce(ctx context.Context, token domainsync.Token) (driveItem, bool, error) {
	endpoint := c.graphBase + approotPath + "/" + url.PathEscape(domainsync.SyncFileName)
	body, err := c.do(ctx, http.MethodGet, endpoint, token, nil, nil)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return driveItem{}, false, nil
		}
		return driveItem{}, false, err
	}
	item, err := decodeItem(body)
	if err != nil {
		return driveItem{}, false, err
	}
	return item, true, nil
}

// download follows the item's pre-authenticated download URL when Graph
// supplies one (the normal shape), otherwise the /content redirect endpoint.
// The download URL is already authorized, so no bearer token is attached to
// it. A download 404 is retried with the same bounded window as the metadata
// lookup: the file was observed a moment ago, so a transient miss must not be
// mistaken for deletion.
func (c *Client) download(ctx context.Context, token domainsync.Token, item driveItem) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		content, err := c.downloadOnce(ctx, token, item)
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, errNotFound) || attempt >= notFoundRetries {
			return nil, err
		}
		if err := c.waitNotFound(ctx, attempt+1); err != nil {
			return nil, err
		}
	}
}

func (c *Client) downloadOnce(ctx context.Context, token domainsync.Token, item driveItem) ([]byte, error) {
	if item.DownloadURL != "" {
		return c.do(ctx, http.MethodGet, item.DownloadURL, domainsync.Token{}, nil, nil)
	}
	endpoint := c.graphBase + itemsPath + url.PathEscape(item.ID) + "/content"
	return c.do(ctx, http.MethodGet, endpoint, token, nil, nil)
}

// waitNotFound waits for the retry backoff while honoring cancellation. The
// backoff grows with the attempt number, so the fake and unit tests can use a
// tiny value without changing the production window.
func (c *Client) waitNotFound(ctx context.Context, attempt int) error {
	timer := time.NewTimer(c.notFoundBackoff * time.Duration(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Write creates the approot file or replaces its content. Both operations are
// conditional: creation sends If-None-Match: * so a peer's new file is never
// overwritten, and an update sends If-Match with the eTag observed by the
// caller's read. An update without an eTag is refused as a conflict instead of
// becoming an unconditional write. A lost condition is ErrConflict and the
// service re-reads and decides again. The payload is small, so the simple PUT
// upload is used.
func (c *Client) Write(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	if current.ID == "" {
		return c.create(ctx, token, content)
	}
	return c.update(ctx, token, current, content)
}

func (c *Client) create(ctx context.Context, token domainsync.Token, content []byte) (appsync.RemoteFile, error) {
	endpoint := c.graphBase + approotPath + "/" + url.PathEscape(domainsync.SyncFileName) + ":/content"
	body, err := c.do(ctx, http.MethodPut, endpoint, token, content, map[string]string{
		"Content-Type":  "application/octet-stream",
		"If-None-Match": "*",
	})
	if err != nil {
		switch {
		case errors.Is(err, appsync.ErrConflict):
			// A peer created the file after this copy's read; the service
			// re-observes instead of overwriting it.
			return appsync.RemoteFile{}, appsync.ErrConflict
		case errors.Is(err, errNotFound):
			// The approot could not be resolved; without a folder there is
			// nowhere to create the file, and a retry cannot recover.
			return appsync.RemoteFile{}, fmt.Errorf("%w: the OneDrive approot is unavailable", appsync.ErrNetwork)
		}
		return appsync.RemoteFile{}, err
	}
	item, err := decodeItem(body)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	return appsync.RemoteFile{ID: item.ID, ETag: item.ETag, Content: content, UpdatedAt: updatedAtOf(content)}, nil
}

func (c *Client) update(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	if current.ETag == "" {
		// Without the eTag observed by the read there is no conditional
		// update. Treating that as an unconditional write could overwrite a
		// peer's copy; a conflict makes the service re-read instead.
		return appsync.RemoteFile{}, fmt.Errorf("%w: the OneDrive item has no eTag", appsync.ErrConflict)
	}
	endpoint := c.graphBase + itemsPath + url.PathEscape(current.ID) + "/content"
	headers := map[string]string{
		"Content-Type": "application/octet-stream",
		"If-Match":     current.ETag,
	}
	body, err := c.do(ctx, http.MethodPut, endpoint, token, content, headers)
	if err != nil {
		switch {
		case errors.Is(err, errNotFound):
			// The file was deleted after the read; a conflict makes the
			// service re-observe and create a fresh copy.
			return appsync.RemoteFile{}, appsync.ErrConflict
		case errors.Is(err, appsync.ErrConflict):
			return appsync.RemoteFile{}, appsync.ErrConflict
		}
		return appsync.RemoteFile{}, err
	}
	item, err := decodeItem(body)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	id := item.ID
	if id == "" {
		id = current.ID
	}
	return appsync.RemoteFile{ID: id, ETag: item.ETag, Content: content, UpdatedAt: updatedAtOf(content)}, nil
}

func decodeItem(body []byte) (driveItem, error) {
	var item driveItem
	if err := json.Unmarshal(body, &item); err != nil {
		return driveItem{}, fmt.Errorf("%w: decode the OneDrive item: %v", appsync.ErrNetwork, err)
	}
	if item.ID == "" {
		return driveItem{}, fmt.Errorf("%w: the OneDrive item has no id", appsync.ErrNetwork)
	}
	return item, nil
}

func updatedAtOf(content []byte) int64 {
	if envelope, err := domainsync.UnmarshalFile(content); err == nil {
		return envelope.Meta.UpdatedAt
	}
	return 0
}

// Internal markers the HTTP helper returns; Read and Write translate them into
// the port's shape.
var errNotFound = errors.New("onedrive: not found")

// do performs one request and maps transport and Graph errors onto the
// application's sentinels. The payload is sent verbatim when present.
func (c *Client) do(
	ctx context.Context,
	method string,
	endpoint string,
	token domainsync.Token,
	payload []byte,
	headers map[string]string,
) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
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
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", appsync.ErrNetwork, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if readErr != nil {
		return nil, fmt.Errorf("%w: read the OneDrive response: %v", appsync.ErrNetwork, readErr)
	}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return body, nil
	case response.StatusCode == http.StatusNotFound:
		return body, errNotFound
	case response.StatusCode == http.StatusUnauthorized:
		return body, appsync.ErrUnauthorized
	case response.StatusCode == http.StatusPreconditionFailed:
		return body, fmt.Errorf("%w: %s", appsync.ErrConflict, bodySnippet(body))
	case response.StatusCode == http.StatusConflict:
		return body, fmt.Errorf("%w: %s", appsync.ErrConflict, bodySnippet(body))
	case response.StatusCode == http.StatusLocked:
		return body, fmt.Errorf("%w: %s", appsync.ErrConflict, bodySnippet(body))
	case response.StatusCode == http.StatusTooManyRequests:
		return body, appsync.ErrRateLimited
	case response.StatusCode == http.StatusForbidden && isRateLimit(body):
		return body, appsync.ErrRateLimited
	case response.StatusCode == http.StatusForbidden:
		return body, appsync.ErrUnauthorized
	case response.StatusCode >= 500:
		return body, fmt.Errorf("%w: Microsoft Graph returned %d: %s", appsync.ErrNetwork, response.StatusCode, bodySnippet(body))
	default:
		return body, fmt.Errorf("%w: Microsoft Graph returned %d: %s", appsync.ErrNetwork, response.StatusCode, bodySnippet(body))
	}
}

func bodySnippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 240 {
		return text[:240] + "..."
	}
	return text
}

func isRateLimit(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "activitylimitreached") ||
		strings.Contains(lower, "quotalimitreached") ||
		strings.Contains(lower, "toomanyrequests") ||
		strings.Contains(lower, "rate limit")
}

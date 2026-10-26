// Package gdrive is the Google Drive cloud-sync adapter. It implements the
// OAuth 2.0 authorization-code grant with PKCE in the system browser and the
// appDataFolder file surface the sync service consumes. Every endpoint is
// injectable so tests and acceptance runs use a local fake instead of Google.
//
// The adapter never sends a client secret: Google's installed-app flow requires
// only the public client id.
package gdrive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// Defaults mirror the original Electron app's Google endpoints.
const (
	DefaultAuthBase   = "https://accounts.google.com"
	DefaultTokenBase  = "https://oauth2.googleapis.com"
	DefaultAPIBase    = "https://www.googleapis.com/drive/v3"
	DefaultUploadBase = "https://www.googleapis.com/upload/drive/v3"
	DefaultScope      = "https://www.googleapis.com/auth/drive.appdata " +
		"https://www.googleapis.com/auth/userinfo.profile " +
		"https://www.googleapis.com/auth/userinfo.email"

	authorizePath = "/o/oauth2/v2/auth"
	tokenPath     = "/token"
	callbackPath  = "/oauth/callback"
	userInfoPath  = "/oauth2/v2/userinfo"

	defaultAuthTimeout = 5 * time.Minute
	maxListPages       = 100
)

// Environment variables override the endpoints and client id. They let the
// selftest and GUI acceptance runs point the whole app at a local fake Google
// without real credentials or network access.
const (
	EnvAuthBase   = "NATCATTY_GDRIVE_AUTH_BASE"
	EnvTokenBase  = "NATCATTY_GDRIVE_TOKEN_BASE"
	EnvAPIBase    = "NATCATTY_GDRIVE_API_BASE"
	EnvUploadBase = "NATCATTY_GDRIVE_UPLOAD_BASE"
	EnvClientID   = "NATCATTY_GDRIVE_CLIENT_ID"
)

// Options configure Client. Zero values use the documented defaults.
type Options struct {
	AuthBase   string
	TokenBase  string
	APIBase    string
	UploadBase string
	ClientID   string
	Scope      string
	// AuthTimeout bounds the wait for the loopback redirect after the browser
	// opens. The zero value is five minutes.
	AuthTimeout time.Duration
	HTTPClient  *http.Client
	// Now is the clock used for token expiry. Tests may substitute it.
	Now func() time.Time
	// Random feeds PKCE generation and the multipart boundary.
	Random io.Reader
}

// OptionsFromEnv reads the fake-endpoint overrides used by tests, the
// fakegdrive tool, and GUI acceptance runs.
func OptionsFromEnv() Options {
	return Options{
		AuthBase:   os.Getenv(EnvAuthBase),
		TokenBase:  os.Getenv(EnvTokenBase),
		APIBase:    os.Getenv(EnvAPIBase),
		UploadBase: os.Getenv(EnvUploadBase),
		ClientID:   os.Getenv(EnvClientID),
	}
}

// Client is the Google Drive adapter. It is safe from any goroutine.
type Client struct {
	authBase    string
	tokenBase   string
	apiBase     string
	uploadBase  string
	userInfoURL string
	clientID    string
	scope       string
	authTimeout time.Duration
	http        *http.Client
	now         func() time.Time
	random      io.Reader
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
		tokenBase = DefaultTokenBase
	}
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	uploadBase := strings.TrimRight(strings.TrimSpace(opts.UploadBase), "/")
	if uploadBase == "" {
		uploadBase = DefaultUploadBase
		if apiBase != DefaultAPIBase {
			// A fake API base serves uploads under the same origin.
			if parsed, err := url.Parse(apiBase); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				uploadBase = parsed.Scheme + "://" + parsed.Host + "/upload/drive/v3"
			}
		}
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
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	return &Client{
		authBase:    authBase,
		tokenBase:   tokenBase,
		apiBase:     apiBase,
		uploadBase:  uploadBase,
		userInfoURL: derivedUserInfoURL(apiBase),
		clientID:    strings.TrimSpace(opts.ClientID),
		scope:       scope,
		authTimeout: authTimeout,
		http:        httpClient,
		now:         now,
		random:      random,
	}
}

// derivedUserInfoURL keeps the token's origin but uses Google's userinfo path.
// A Drive API base such as https://www.googleapis.com/drive/v3 yields
// https://www.googleapis.com/oauth2/v2/userinfo.
func derivedUserInfoURL(apiBase string) string {
	parsed, err := url.Parse(apiBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return apiBase + userInfoPath
	}
	parsed.Path = userInfoPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
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
// and returns the authorized account. No client secret is sent.
func (c *Client) Authorize(ctx context.Context, open func(string) error) (domainsync.Token, appsync.Grant, error) {
	if c.clientID == "" {
		return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: no Google OAuth client id configured", appsync.ErrNotConfigured)
	}
	if open == nil {
		return domainsync.Token{}, appsync.Grant{}, appsync.ErrBrowserUnavailable
	}
	proof, err := NewPKCE(c.random)
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: start the loopback listener: %v", appsync.ErrNetwork, err)
	}
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(proof.State, results)}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
	}()

	authURL := c.authorizationURL(redirectURI, proof)
	if err := open(authURL); err != nil {
		return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: %v", appsync.ErrBrowserUnavailable, err)
	}

	timer := time.NewTimer(c.authTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: %v", appsync.ErrAuthorizationTimeout, ctx.Err())
		}
		return domainsync.Token{}, appsync.Grant{}, ctx.Err()
	case <-timer.C:
		return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: no redirect within %s", appsync.ErrAuthorizationTimeout, c.authTimeout)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return domainsync.Token{}, appsync.Grant{}, fmt.Errorf("%w: loopback listener: %v", appsync.ErrNetwork, err)
		}
		return domainsync.Token{}, appsync.Grant{}, appsync.ErrNetwork
	case result := <-results:
		if result.err != nil {
			return domainsync.Token{}, appsync.Grant{}, result.err
		}
		token, err := c.exchange(ctx, result.code, proof.Verifier, redirectURI)
		if err != nil {
			return domainsync.Token{}, appsync.Grant{}, err
		}
		grant, err := c.User(ctx, token)
		if err != nil {
			return domainsync.Token{}, appsync.Grant{}, err
		}
		return token, grant, nil
	}
}

type callbackResult struct {
	code string
	err  error
}

// callbackHandler answers the loopback redirect. The page is written before
// the result is published so the browser is never cut off by authorization
// completing.
func callbackHandler(expectedState string, results chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("state") != expectedState:
			writeCallbackPage(w, http.StatusBadRequest, "Authorization failed", "The authorization state did not match this attempt. Return to Netcatty and try again.")
			publishCallback(results, callbackResult{err: appsync.ErrAuthorizationState})
		case query.Get("error") != "":
			writeCallbackPage(w, http.StatusOK, "Authorization cancelled", "Google did not grant access. You can close this window and try again from Netcatty.")
			publishCallback(results, callbackResult{err: appsync.ErrAuthorizationDenied})
		case query.Get("code") == "":
			writeCallbackPage(w, http.StatusBadRequest, "Authorization failed", "The redirect carried no authorization code. Return to Netcatty and try again.")
			publishCallback(results, callbackResult{err: fmt.Errorf("%w: the redirect carried no code", appsync.ErrAuthorizationDenied)})
		default:
			writeCallbackPage(w, http.StatusOK, "Netcatty is connected", "Authorization finished. You can close this window and return to Netcatty.")
			publishCallback(results, callbackResult{code: query.Get("code")})
		}
	})
	return mux
}

func publishCallback(results chan<- callbackResult, result callbackResult) {
	select {
	case results <- result:
	default:
	}
}

func writeCallbackPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>"+
		title+"</title></head><body style=\"font-family: system-ui, sans-serif; margin: 3rem; color: #1f2328;\"><h1 style=\"font-size: 1.35rem;\">"+
		title+"</h1><p style=\"font-size: 0.95rem; max-width: 30rem;\">"+message+"</p></body></html>")
}

func (c *Client) authorizationURL(redirectURI string, proof PKCE) string {
	query := url.Values{}
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", c.scope)
	query.Set("state", proof.State)
	query.Set("code_challenge", proof.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	return c.authBase + authorizePath + "?" + query.Encode()
}

func (c *Client) exchange(ctx context.Context, code, verifier, redirectURI string) (domainsync.Token, error) {
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	return c.tokenRequest(ctx, form, tokenPurposeCode)
}

// Refresh exchanges a refresh token. The provider may rotate the refresh token;
// an absent replacement is left empty for the application to carry forward.
// invalid_grant is typed so the service starts a fresh browser authorization.
func (c *Client) Refresh(ctx context.Context, token domainsync.Token) (domainsync.Token, error) {
	if token.RefreshToken == "" {
		return domainsync.Token{}, appsync.ErrReauthorizationRequired
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
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
			return domainsync.Token{}, fmt.Errorf("%w: Google returned %d", appsync.ErrNetwork, response.StatusCode)
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
	case "invalid_grant":
		if purpose == tokenPurposeRefresh {
			return fmt.Errorf("%w: %s", appsync.ErrReauthorizationRequired, payload.Description)
		}
		return fmt.Errorf("%w: %s", appsync.ErrAuthorizationDenied, payload.Description)
	case "access_denied":
		return fmt.Errorf("%w: %s", appsync.ErrAuthorizationDenied, payload.Description)
	case "invalid_client":
		return fmt.Errorf("%w: Google rejected the OAuth client id", appsync.ErrNotConfigured)
	case "":
		return fmt.Errorf("%w: Google returned %d", appsync.ErrNetwork, status)
	default:
		return fmt.Errorf("%w: %s: %s", appsync.ErrUnauthorized, payload.Error, payload.Description)
	}
}

type userInfoResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// User returns the authorized Google account. The id is the merge-anchor
// identity; the email is the displayed identity.
func (c *Client) User(ctx context.Context, token domainsync.Token) (appsync.Grant, error) {
	body, err := c.do(ctx, http.MethodGet, c.userInfoURL, token, nil, nil)
	if err != nil {
		return appsync.Grant{}, err
	}
	var user userInfoResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return appsync.Grant{}, fmt.Errorf("%w: decode userinfo: %v", appsync.ErrNetwork, err)
	}
	login := user.Email
	if login == "" {
		login = user.Name
	}
	if user.ID == "" && login == "" {
		return appsync.Grant{}, fmt.Errorf("%w: userinfo has no account identity", appsync.ErrNetwork)
	}
	return appsync.Grant{ID: user.ID, Login: login}, nil
}

type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ModifiedTime string `json:"modifiedTime"`
	Size         string `json:"size"`
}

type driveList struct {
	Files         []driveFile `json:"files"`
	NextPageToken string      `json:"nextPageToken"`
}

// Read finds the product file in appDataFolder and downloads it. exists is
// false when the account has no copy, including a file deleted between the
// list and the download.
func (c *Client) Read(ctx context.Context, token domainsync.Token) (appsync.RemoteFile, bool, error) {
	file, found, err := c.findFile(ctx, token)
	if err != nil {
		return appsync.RemoteFile{}, false, err
	}
	if !found {
		return appsync.RemoteFile{}, false, nil
	}
	endpoint := c.apiBase + "/files/" + url.PathEscape(file.ID) + "?alt=media"
	content, err := c.do(ctx, http.MethodGet, endpoint, token, nil, nil)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return appsync.RemoteFile{}, false, nil
		}
		return appsync.RemoteFile{}, false, err
	}
	return appsync.RemoteFile{
		ID:        file.ID,
		ETag:      file.ModifiedTime,
		Content:   content,
		UpdatedAt: updatedAtOf(content),
	}, true, nil
}

// findFile pages through appDataFolder until it finds the named product file.
// The query is restricted to appDataFolder; a file elsewhere in the account is
// invisible to sync.
func (c *Client) findFile(ctx context.Context, token domainsync.Token) (driveFile, bool, error) {
	pageToken := ""
	for page := 0; page < maxListPages; page++ {
		query := url.Values{}
		query.Set("spaces", "appDataFolder")
		query.Set("q", "name = '"+domainsync.SyncFileName+"' and trashed = false")
		query.Set("fields", "nextPageToken,files(id,name,modifiedTime,size)")
		query.Set("pageSize", "100")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		body, err := c.do(ctx, http.MethodGet, c.apiBase+"/files?"+query.Encode(), token, nil, nil)
		if err != nil {
			return driveFile{}, false, err
		}
		var listing driveList
		if err := json.Unmarshal(body, &listing); err != nil {
			return driveFile{}, false, fmt.Errorf("%w: decode the Drive file list: %v", appsync.ErrNetwork, err)
		}
		for _, file := range listing.Files {
			if file.Name == domainsync.SyncFileName && file.ID != "" {
				return file, true, nil
			}
		}
		if listing.NextPageToken == "" {
			return driveFile{}, false, nil
		}
		pageToken = listing.NextPageToken
	}
	return driveFile{}, false, fmt.Errorf("%w: the Drive file list did not finish paging", appsync.ErrRemoteCorrupt)
}

// Write creates the appDataFolder file or replaces its media. Google Drive
// media updates are not conditional; the sync service detects concurrent
// changes by comparing modifiedTime before writing and re-reading after a
// conflict.
func (c *Client) Write(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	if current.ID == "" {
		return c.create(ctx, token, content)
	}
	return c.update(ctx, token, current, content)
}

func (c *Client) create(ctx context.Context, token domainsync.Token, content []byte) (appsync.RemoteFile, error) {
	metadata, err := json.Marshal(map[string]any{
		"name":    domainsync.SyncFileName,
		"parents": []string{"appDataFolder"},
	})
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "application/json; charset=UTF-8")
	part, err := writer.CreatePart(header)
	if err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: build the multipart metadata: %v", appsync.ErrNetwork, err)
	}
	if _, err := part.Write(metadata); err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: write the multipart metadata: %v", appsync.ErrNetwork, err)
	}
	mediaHeader := textproto.MIMEHeader{}
	mediaHeader.Set("Content-Type", "application/octet-stream")
	media, err := writer.CreatePart(mediaHeader)
	if err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: build the multipart media: %v", appsync.ErrNetwork, err)
	}
	if _, err := media.Write(content); err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: write the multipart media: %v", appsync.ErrNetwork, err)
	}
	if err := writer.Close(); err != nil {
		return appsync.RemoteFile{}, fmt.Errorf("%w: finish the multipart body: %v", appsync.ErrNetwork, err)
	}
	endpoint := c.uploadBase + "/files?uploadType=multipart&fields=id,name,modifiedTime"
	responseBody, err := c.do(ctx, http.MethodPost, endpoint, token, body.Bytes(), map[string]string{
		"Content-Type": "multipart/related; boundary=" + writer.Boundary(),
	})
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	file, err := decodeDriveFile(responseBody)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	return appsync.RemoteFile{ID: file.ID, ETag: file.ModifiedTime, Content: content, UpdatedAt: updatedAtOf(content)}, nil
}

func (c *Client) update(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	if current.ETag != "" {
		// Drive media updates carry no If-Match, so observe modifiedTime first.
		// A change since the caller's read means the bytes are stale; the service
		// re-observes and merges instead of overwriting a peer's copy.
		metadata, err := c.stat(ctx, token, current.ID)
		if err != nil {
			if errors.Is(err, errNotFound) {
				return appsync.RemoteFile{}, appsync.ErrConflict
			}
			return appsync.RemoteFile{}, err
		}
		if metadata.ModifiedTime != "" && metadata.ModifiedTime != current.ETag {
			return appsync.RemoteFile{}, appsync.ErrConflict
		}
	}
	endpoint := c.uploadBase + "/files/" + url.PathEscape(current.ID) +
		"?uploadType=media&fields=id,name,modifiedTime"
	responseBody, err := c.do(ctx, http.MethodPatch, endpoint, token, content, map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		if errors.Is(err, errNotFound) {
			// The file was deleted after the read. A conflict makes the service
			// re-observe and create a fresh copy instead of retrying forever.
			return appsync.RemoteFile{}, appsync.ErrConflict
		}
		return appsync.RemoteFile{}, err
	}
	file, err := decodeDriveFile(responseBody)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	id := file.ID
	if id == "" {
		id = current.ID
	}
	return appsync.RemoteFile{ID: id, ETag: file.ModifiedTime, Content: content, UpdatedAt: updatedAtOf(content)}, nil
}

// stat reads only the file's metadata. Drive media updates have no conditional
// header, so the adapter compares this modifiedTime with the one it observed
// before choosing to upload.
func (c *Client) stat(ctx context.Context, token domainsync.Token, id string) (driveFile, error) {
	endpoint := c.apiBase + "/files/" + url.PathEscape(id) + "?fields=id,name,modifiedTime"
	body, err := c.do(ctx, http.MethodGet, endpoint, token, nil, nil)
	if err != nil {
		return driveFile{}, err
	}
	var file driveFile
	if err := json.Unmarshal(body, &file); err != nil {
		return driveFile{}, fmt.Errorf("%w: decode the Drive metadata: %v", appsync.ErrNetwork, err)
	}
	return file, nil
}

func decodeDriveFile(body []byte) (driveFile, error) {
	var file driveFile
	if err := json.Unmarshal(body, &file); err != nil {
		return driveFile{}, fmt.Errorf("%w: decode the Drive file: %v", appsync.ErrNetwork, err)
	}
	if file.ID == "" {
		return driveFile{}, fmt.Errorf("%w: the Drive response has no file id", appsync.ErrNetwork)
	}
	return file, nil
}

func updatedAtOf(content []byte) int64 {
	if envelope, err := domainsync.UnmarshalFile(content); err == nil {
		return envelope.Meta.UpdatedAt
	}
	return 0
}

// errNotFound is an internal marker the HTTP helper returns; Read and update
// translate it into the port's shape.
var errNotFound = errors.New("gdrive: not found")

// do performs one request and maps transport and Drive errors onto the
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
		return nil, fmt.Errorf("%w: read the Drive response: %v", appsync.ErrNetwork, readErr)
	}
	switch {
	case response.StatusCode == http.StatusNotFound:
		return body, errNotFound
	case response.StatusCode == http.StatusUnauthorized:
		return body, appsync.ErrUnauthorized
	case response.StatusCode == http.StatusPreconditionFailed, response.StatusCode == http.StatusConflict:
		return body, appsync.ErrConflict
	case response.StatusCode == http.StatusTooManyRequests:
		return body, appsync.ErrRateLimited
	case response.StatusCode == http.StatusForbidden && isRateLimit(body):
		return body, appsync.ErrRateLimited
	case response.StatusCode == http.StatusForbidden:
		return body, appsync.ErrUnauthorized
	case response.StatusCode >= 500:
		return body, fmt.Errorf("%w: Google Drive returned %d: %s", appsync.ErrNetwork, response.StatusCode, bodySnippet(body))
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return body, fmt.Errorf("%w: Google Drive returned %d: %s", appsync.ErrNetwork, response.StatusCode, bodySnippet(body))
	}
	return body, nil
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
	return strings.Contains(lower, "ratelimitexceeded") ||
		strings.Contains(lower, "userratelimitexceeded") ||
		strings.Contains(lower, "quotaexceeded") ||
		strings.Contains(lower, "rate limit")
}

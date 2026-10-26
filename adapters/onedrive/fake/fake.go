// Package fake is an in-process Microsoft identity and OneDrive approot
// server. It mirrors the endpoints the onedrive adapter calls, validates PKCE
// S256, rotates and invalidates refresh tokens, isolates files per Microsoft
// account, enforces the eTag conditional writes, and exposes control/state
// endpoints so acceptance runs can switch accounts, force reauthorization, and
// inspect what the app did. It never contacts Microsoft.
package fake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authorizePath  = "/authorize"
	tokenPath      = "/token"
	mePath         = "/v1.0/me"
	approotPrefix  = "/v1.0/me/drive/special/approot:/"
	itemsPrefix    = "/v1.0/me/drive/items/"
	downloadPrefix = "/download/"
	syncFileName   = "netcatty-vault.json"
	scopeValue     = "https://graph.microsoft.com/Files.ReadWrite.AppFolder https://graph.microsoft.com/User.Read offline_access"
)

// Account is one fake Microsoft account.
type Account struct {
	Key         string `json:"key"`
	ID          string `json:"id"`
	Mail        string `json:"mail"`
	DisplayName string `json:"displayName"`
}

// Control is the acceptance harness's mutable view of the fake. Pointers in
// controlRequest distinguish "leave alone" from "set false".
type Control struct {
	Deny              bool `json:"deny"`
	TamperState       bool `json:"tamperState"`
	InvalidateRefresh bool `json:"invalidateRefresh"`
	// KeepRefresh makes a refresh response omit refresh_token and keep the
	// presented token valid. The default (false) rotates, matching Microsoft's
	// consumer behavior.
	KeepRefresh bool `json:"keepRefresh"`
	// DelayMS adds artificial latency to every non-control response. GUI
	// acceptance uses it to observe the transient syncing state; the default
	// (0) answers immediately.
	DelayMS int `json:"delayMs"`
	// TransientNotFound makes the next N metadata reads return 404 even
	// though the file exists, simulating Graph's eventual consistency after
	// a peer upload. TransientDownloadNotFound does the same for the
	// pre-authenticated download URL.
	TransientNotFound         int    `json:"transientNotFound"`
	TransientDownloadNotFound int    `json:"transientDownloadNotFound"`
	Account                   string `json:"account"`
}

// State is the observable summary returned by /__state and State().
type State struct {
	URL              string         `json:"url"`
	ClientID         string         `json:"clientId"`
	CurrentAccount   string         `json:"currentAccount"`
	Accounts         []AccountState `json:"accounts"`
	Authorizations   int            `json:"authorizations"`
	TokenExchanges   int            `json:"tokenExchanges"`
	Refreshes        int            `json:"refreshes"`
	Reads            int            `json:"reads"`
	Creates          int            `json:"creates"`
	Updates          int            `json:"updates"`
	Downloads        int            `json:"downloads"`
	Conflicts        int            `json:"conflicts"`
	SecretViolations int            `json:"secretViolations"`
	StateMismatches  int            `json:"stateMismatches"`
	Control          Control        `json:"control"`
}

// AccountState reports one account's approot contents.
type AccountState struct {
	Account
	Files []FileState `json:"files"`
}

// FileState is one stored file's metadata.
type FileState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	ETag     string `json:"eTag"`
	Modified string `json:"modifiedTime"`
}

type storedFile struct {
	id       string
	name     string
	content  []byte
	etag     string
	modified time.Time
}

type account struct {
	Account
	files map[string]*storedFile
}

type codeRecord struct {
	accountKey  string
	challenge   string
	redirectURI string
	clientID    string
}

type refreshRecord struct {
	accountKey string
}

// Server is the fake Microsoft surface.
type Server struct {
	mu sync.Mutex

	listener net.Listener
	http     *http.Server
	baseURL  string
	clientID string
	now      func() time.Time

	accounts map[string]*account
	current  string
	access   map[string]string
	expired  map[string]bool
	refresh  map[string]refreshRecord
	codes    map[string]codeRecord
	control  Control
	nextID   int

	authorizations   int
	tokenExchanges   int
	refreshes        int
	reads            int
	creates          int
	updates          int
	downloads        int
	conflicts        int
	secretViolations int
	stateMismatches  int
}

// Start listens on an ephemeral 127.0.0.1 port.
func Start() (*Server, error) {
	return StartAt("127.0.0.1:0")
}

// StartAt listens on address. The GUI acceptance harness uses a fixed port.
func StartAt(address string) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := newServer()
	server.listener = listener
	server.baseURL = "http://" + listener.Addr().String()
	server.http = &http.Server{Handler: server}
	go func() {
		_ = server.http.Serve(listener)
	}()
	return server, nil
}

func newServer() *Server {
	now := time.Now
	server := &Server{
		clientID: "fake-onedrive-client-id",
		now:      now,
		accounts: map[string]*account{
			"primary": {
				Account: Account{Key: "primary", ID: "onedrive-account-1", Mail: "one@example.test", DisplayName: "One Example"},
				files:   make(map[string]*storedFile),
			},
			"second": {
				Account: Account{Key: "second", ID: "onedrive-account-2", Mail: "two@example.test", DisplayName: "Two Example"},
				files:   make(map[string]*storedFile),
			},
		},
		current: "primary",
		access:  make(map[string]string),
		expired: make(map[string]bool),
		refresh: make(map[string]refreshRecord),
		codes:   make(map[string]codeRecord),
		control: Control{},
	}
	return server
}

// URL is the fake's origin, suitable for every NATCATTY_ONEDRIVE_* base.
func (s *Server) URL() string { return s.baseURL }

// ClientID is the id the fake accepts.
func (s *Server) ClientID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientID
}

// Close stops the fake.
func (s *Server) Close() {
	if s.http != nil {
		_ = s.http.Close()
	}
}

// Configure mutates the control surface in-process. The HTTP control endpoint
// offers the same operations to external acceptance drivers.
func (s *Server) Configure(apply func(*Control)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apply(&s.control)
	if _, ok := s.accounts[s.control.Account]; ok {
		s.current = s.control.Account
	}
	if s.control.InvalidateRefresh {
		s.refresh = make(map[string]refreshRecord)
	}
}

// ExpireAccessTokens marks every access token issued so far as expired, so the
// next Graph call returns 401 while the refresh token stays usable.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.access {
		s.expired[token] = true
	}
}

// BumpETag advances one stored file's eTag without changing its content. Tests
// use it to simulate a peer changing the file between the adapter's read and
// its conditional update.
func (s *Server) BumpETag(accountKey, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountKey]
	if !ok {
		return errors.New("fake: unknown account")
	}
	file, ok := account.files[id]
	if !ok {
		return errors.New("fake: unknown file")
	}
	s.nextID++
	file.etag = s.newETag()
	file.modified = file.modified.Add(time.Second)
	return nil
}

// SeedFile inserts a file directly into one account's approot. Tests use it to
// place a non-product file or a pre-existing copy.
func (s *Server) SeedFile(accountKey, name string, content []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountKey]
	if !ok {
		return "", errors.New("fake: unknown account")
	}
	s.nextID++
	id := "fake-file-" + strconv.Itoa(s.nextID)
	account.files[id] = &storedFile{id: id, name: name, content: append([]byte(nil), content...), etag: s.newETag(), modified: s.now()}
	return id, nil
}

// FileContent returns one account's stored product file bytes. The selftest
// uses it to prove a failed reauthorization left the cloud copy untouched
// without minting a new access token.
func (s *Server) FileContent(accountKey, name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountKey]
	if !ok {
		return nil, false
	}
	for _, file := range account.files {
		if file.name == name {
			return append([]byte(nil), file.content...), true
		}
	}
	return nil, false
}

// Reset clears control flags and every token, code, and stored file. Counters
// are zeroed too, so a repeated selftest starts from a known state.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, account := range s.accounts {
		account.files = make(map[string]*storedFile)
	}
	s.current = "primary"
	s.access = make(map[string]string)
	s.expired = make(map[string]bool)
	s.refresh = make(map[string]refreshRecord)
	s.codes = make(map[string]codeRecord)
	s.control = Control{}
	s.nextID = 0
	s.authorizations = 0
	s.tokenExchanges = 0
	s.refreshes = 0
	s.reads = 0
	s.creates = 0
	s.updates = 0
	s.downloads = 0
	s.conflicts = 0
	s.secretViolations = 0
	s.stateMismatches = 0
}

// State returns a snapshot for acceptance assertions.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Server) stateLocked() State {
	state := State{
		URL:              s.baseURL,
		ClientID:         s.clientID,
		CurrentAccount:   s.current,
		Authorizations:   s.authorizations,
		TokenExchanges:   s.tokenExchanges,
		Refreshes:        s.refreshes,
		Reads:            s.reads,
		Creates:          s.creates,
		Updates:          s.updates,
		Downloads:        s.downloads,
		Conflicts:        s.conflicts,
		SecretViolations: s.secretViolations,
		StateMismatches:  s.stateMismatches,
		Control:          s.control,
	}
	for _, key := range []string{"primary", "second"} {
		account := s.accounts[key]
		entry := AccountState{Account: account.Account}
		ids := make([]string, 0, len(account.files))
		for id := range account.files {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			file := account.files[id]
			entry.Files = append(entry.Files, FileState{
				ID: file.id, Name: file.name, Size: len(file.content), ETag: file.etag,
				Modified: file.modified.UTC().Format(time.RFC3339Nano),
			})
		}
		state.Accounts = append(state.Accounts, entry)
	}
	return state
}

// ServeHTTP routes the Microsoft surface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delay := s.control.DelayMS
	s.mu.Unlock()
	if delay > 0 && r.URL.Path != "/__control" && r.URL.Path != "/__state" {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	switch {
	case r.URL.Path == authorizePath && r.Method == http.MethodGet:
		s.authorize(w, r)
	case r.URL.Path == tokenPath && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == mePath && r.Method == http.MethodGet:
		s.userInfo(w, r)
	case strings.HasPrefix(r.URL.Path, downloadPrefix) && r.Method == http.MethodGet:
		s.downloadContent(w, r)
	case strings.HasPrefix(r.URL.Path, approotPrefix) && r.Method == http.MethodGet:
		s.metadataByName(w, r)
	case strings.HasPrefix(r.URL.Path, approotPrefix) && r.Method == http.MethodPut &&
		strings.HasSuffix(r.URL.Path, ":/content"):
		s.createFile(w, r)
	case strings.HasPrefix(r.URL.Path, itemsPrefix) && r.Method == http.MethodPut &&
		strings.HasSuffix(r.URL.Path, "/content"):
		s.updateFile(w, r)
	case strings.HasPrefix(r.URL.Path, itemsPrefix) && r.Method == http.MethodGet:
		s.metadataByID(w, r)
	case r.URL.Path == "/__control" && r.Method == http.MethodPost:
		s.controlEndpoint(w, r)
	case r.URL.Path == "/__state" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.State())
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	redirectURI := query.Get("redirect_uri")
	state := query.Get("state")
	clientID := query.Get("client_id")
	challenge := query.Get("code_challenge")
	if redirectURI == "" || state == "" || clientID != s.clientID ||
		challenge == "" || query.Get("code_challenge_method") != "S256" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	if !strings.Contains(query.Get("scope"), "Files.ReadWrite.AppFolder") {
		http.Error(w, "the app folder scope is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizations++
	if s.control.Deny {
		location := redirectURI + "?error=access_denied&error_description=The+user+denied+access&state=" + url.QueryEscape(state)
		http.Redirect(w, r, location, http.StatusFound)
		return
	}
	if s.control.TamperState {
		s.stateMismatches++
		location := redirectURI + "?code=tampered-code&state=" + url.QueryEscape("not-"+state)
		http.Redirect(w, r, location, http.StatusFound)
		return
	}
	s.nextID++
	code := "fake-code-" + strconv.Itoa(s.nextID)
	s.codes[code] = codeRecord{
		accountKey:  s.current,
		challenge:   challenge,
		redirectURI: redirectURI,
		clientID:    clientID,
	}
	location := redirectURI + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	http.Redirect(w, r, location, http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(r.Form.Get("client_secret")) != "" {
		s.secretViolations++
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the installed-app flow must not send a client secret")
		return
	}
	if r.Form.Get("client_id") != s.clientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client id")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refreshToken(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	code := r.Form.Get("code")
	record, ok := s.codes[code]
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already used code")
		return
	}
	delete(s.codes, code)
	if record.redirectURI != r.Form.Get("redirect_uri") ||
		record.clientID != r.Form.Get("client_id") ||
		record.challenge != challengeFor(r.Form.Get("code_verifier")) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.tokenExchanges++
	access := s.issueAccess(record.accountKey)
	refresh := s.issueRefresh(record.accountKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "Bearer",
		"scope": scopeValue, "expires_in": 3600,
	})
}

func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	refresh := r.Form.Get("refresh_token")
	record, ok := s.refresh[refresh]
	if !ok || s.control.InvalidateRefresh {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is no longer valid")
		return
	}
	s.refreshes++
	access := s.issueAccess(record.accountKey)
	response := map[string]any{
		"access_token": access, "token_type": "Bearer",
		"scope": scopeValue, "expires_in": 3600,
	}
	if s.control.KeepRefresh {
		// The shape where the provider returns no replacement; the adapter
		// keeps the presented token.
	} else {
		// Rotation: the presented refresh token is consumed and the
		// replacement is the only usable one. Microsoft consumer tokens
		// rotate on most refreshes.
		delete(s.refresh, refresh)
		response["refresh_token"] = s.issueRefresh(record.accountKey)
	}
	writeJSON(w, http.StatusOK, response)
}

// issueAccess stores a fresh access token bound to the account. The caller
// holds the lock.
func (s *Server) issueAccess(accountKey string) string {
	s.nextID++
	access := "fake-access-" + accountKey + "-" + strconv.Itoa(s.nextID)
	s.access[access] = accountKey
	return access
}

// issueRefresh stores a fresh refresh token. The caller holds the lock.
func (s *Server) issueRefresh(accountKey string) string {
	s.nextID++
	refresh := "fake-refresh-" + accountKey + "-" + strconv.Itoa(s.nextID)
	s.refresh[refresh] = refreshRecord{accountKey: accountKey}
	return refresh
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) userInfo(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": account.ID, "mail": account.Mail, "userPrincipalName": account.Mail,
		"displayName": account.DisplayName,
	})
}

// accountFor resolves the bearer token and rejects an access token the control
// surface expired.
func (s *Server) accountFor(r *http.Request) (*account, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.access[token]
	if !ok || s.expired[token] {
		return nil, false
	}
	return s.accounts[key], true
}

// metadataByName answers the path-addressed approot metadata read.
func (s *Server) metadataByName(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, approotPrefix)
	name, err := url.PathUnescape(strings.TrimSuffix(rest, ":"))
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalidRequest", "invalid path")
		return
	}
	s.mu.Lock()
	s.reads++
	if s.control.TransientNotFound > 0 {
		s.control.TransientNotFound--
		s.mu.Unlock()
		writeGraphError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	var file *storedFile
	for _, candidate := range account.files {
		if candidate.name == name {
			file = candidate
			break
		}
	}
	var response map[string]any
	if file != nil {
		response = s.itemJSON(file)
	}
	s.mu.Unlock()
	if file == nil {
		writeGraphError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// metadataByID answers GET /me/drive/items/{id}.
func (s *Server) metadataByID(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, itemsPrefix)
	// A /content suffix is handled by the download endpoint in real Graph;
	// the adapter does not use it when the item carries a download URL.
	id = strings.TrimSuffix(id, "/content")
	s.mu.Lock()
	s.reads++
	if s.control.TransientNotFound > 0 {
		s.control.TransientNotFound--
		s.mu.Unlock()
		writeGraphError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	file, exists := account.files[id]
	var response map[string]any
	if exists {
		response = s.itemJSON(file)
	}
	s.mu.Unlock()
	if !exists {
		writeGraphError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// downloadContent serves a pre-authenticated download URL. Real Graph returns
// a short-lived URL that needs no bearer token; the fake requires the opaque
// token query to match the file id so a wrong URL is rejected.
func (s *Server) downloadContent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, downloadPrefix)
	if r.URL.Query().Get("token") != id {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	if s.control.TransientDownloadNotFound > 0 {
		s.control.TransientDownloadNotFound--
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	var file *storedFile
	for _, account := range s.accounts {
		if candidate, ok := account.files[id]; ok {
			file = candidate
			break
		}
	}
	if file != nil {
		s.downloads++
	}
	s.mu.Unlock()
	if file == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(file.content)
}

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, approotPrefix)
	rest = strings.TrimSuffix(rest, ":/content")
	name, err := url.PathUnescape(rest)
	if err != nil || name != syncFileName {
		writeGraphError(w, http.StatusBadRequest, "invalidRequest", "the file must be the sync file in the approot")
		return
	}
	content, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range account.files {
		if candidate.name == name {
			if r.Header.Get("If-None-Match") == "*" {
				s.conflicts++
				writeGraphError(w, http.StatusPreconditionFailed, "nameAlreadyExists", "An item with the same name already exists.")
				return
			}
		}
	}
	s.nextID++
	id := "fake-file-" + strconv.Itoa(s.nextID)
	file := &storedFile{id: id, name: name, content: content, etag: s.newETag(), modified: s.now()}
	account.files[id] = file
	s.creates++
	writeJSON(w, http.StatusCreated, s.itemJSON(file))
}

func (s *Server) updateFile(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, itemsPrefix), "/content")
	content, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	s.mu.Lock()
	defer s.mu.Unlock()
	file, exists := account.files[id]
	if !exists {
		writeGraphError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	condition := r.Header.Get("If-Match")
	if condition == "" || condition != file.etag {
		s.conflicts++
		writeGraphError(w, http.StatusPreconditionFailed, "preconditionFailed", "The eTag does not match the current item.")
		return
	}
	file.content = content
	file.etag = s.newETag()
	file.modified = s.now()
	s.updates++
	writeJSON(w, http.StatusOK, s.itemJSON(file))
}

func (s *Server) newETag() string {
	return `"etag-` + strconv.Itoa(s.nextID) + `"`
}

func (s *Server) itemJSON(file *storedFile) map[string]any {
	return map[string]any{
		"id":                           file.id,
		"name":                         file.name,
		"eTag":                         file.etag,
		"cTag":                         `"c:` + file.etag + `"`,
		"size":                         len(file.content),
		"lastModifiedDateTime":         file.modified.UTC().Format(time.RFC3339),
		"@microsoft.graph.downloadUrl": s.baseURL + downloadPrefix + file.id + "?token=" + url.QueryEscape(file.id),
	}
}

type controlRequest struct {
	Account           *string `json:"account"`
	Deny              *bool   `json:"deny"`
	TamperState       *bool   `json:"tamperState"`
	InvalidateRefresh *bool   `json:"invalidateRefresh"`
	KeepRefresh       *bool   `json:"keepRefresh"`
	DelayMS           *int    `json:"delayMs"`
	ExpireAccess      *bool   `json:"expireAccess"`
	Reset             *bool   `json:"reset"`
	// The transient-failure counts are pointers so an explicit zero clears
	// them while an omitted field leaves the current count alone.
	TransientNotFound         *int `json:"transientNotFound"`
	TransientDownloadNotFound *int `json:"transientDownloadNotFound"`
}

func (s *Server) controlEndpoint(w http.ResponseWriter, r *http.Request) {
	var request controlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	if request.Reset != nil && *request.Reset {
		s.mu.Unlock()
		s.Reset()
		writeJSON(w, http.StatusOK, s.State())
		return
	}
	if request.Account != nil {
		if _, ok := s.accounts[*request.Account]; !ok {
			s.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown account"})
			return
		}
		s.current = *request.Account
	}
	if request.Deny != nil {
		s.control.Deny = *request.Deny
	}
	if request.TamperState != nil {
		s.control.TamperState = *request.TamperState
	}
	if request.InvalidateRefresh != nil {
		s.control.InvalidateRefresh = *request.InvalidateRefresh
		if *request.InvalidateRefresh {
			s.refresh = make(map[string]refreshRecord)
		}
	}
	if request.KeepRefresh != nil {
		s.control.KeepRefresh = *request.KeepRefresh
	}
	if request.DelayMS != nil {
		s.control.DelayMS = *request.DelayMS
	}
	if request.ExpireAccess != nil && *request.ExpireAccess {
		for token := range s.access {
			s.expired[token] = true
		}
	}
	if request.TransientNotFound != nil {
		s.control.TransientNotFound = *request.TransientNotFound
	}
	if request.TransientDownloadNotFound != nil {
		s.control.TransientDownloadNotFound = *request.TransientDownloadNotFound
	}
	state := s.stateLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, state)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error": code, "error_description": description,
	})
}

func writeGraphError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

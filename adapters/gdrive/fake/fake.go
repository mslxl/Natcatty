// Package fake is an in-process Google OAuth and Drive appDataFolder server.
// It mirrors the endpoints the gdrive adapter calls, validates PKCE S256,
// rotates and invalidates refresh tokens, isolates files per Google account,
// and exposes control/state endpoints so acceptance runs can switch accounts,
// force reauthorization, and inspect what the app did. It never contacts
// Google.
package fake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authorizePath  = "/o/oauth2/v2/auth"
	tokenPath      = "/token"
	userInfoPath   = "/oauth2/v2/userinfo"
	driveFilesPath = "/drive/v3/files"
	uploadFiles    = "/upload/drive/v3/files"
	syncFileName   = "netcatty-vault.json"
)

// Account is one fake Google account.
type Account struct {
	Key   string `json:"key"`
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Control is the acceptance harness's mutable view of the fake. Pointers in
// controlRequest distinguish "leave alone" from "set false".
type Control struct {
	Deny              bool `json:"deny"`
	TamperState       bool `json:"tamperState"`
	InvalidateRefresh bool `json:"invalidateRefresh"`
	KeepRefresh       bool `json:"keepRefresh"`
	PageSize          int  `json:"pageSize"`
	IgnoreNameFilter  bool `json:"ignoreNameFilter"`
	Account           string
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
	Lists            int            `json:"lists"`
	Creates          int            `json:"creates"`
	Updates          int            `json:"updates"`
	Downloads        int            `json:"downloads"`
	SecretViolations int            `json:"secretViolations"`
	StateMismatches  int            `json:"stateMismatches"`
	Control          Control        `json:"control"`
}

// AccountState reports one account's appDataFolder contents.
type AccountState struct {
	Account
	Files []FileState `json:"files"`
}

// FileState is one stored file's metadata.
type FileState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Modified string `json:"modifiedTime"`
}

type storedFile struct {
	id       string
	name     string
	content  []byte
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

// Server is the fake Google surface.
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
	lists            int
	creates          int
	updates          int
	downloads        int
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
		clientID: "fake-google-client-id",
		now:      now,
		accounts: map[string]*account{
			"primary": {
				Account: Account{Key: "primary", ID: "google-account-1", Email: "one@example.test", Name: "One Example"},
				files:   make(map[string]*storedFile),
			},
			"second": {
				Account: Account{Key: "second", ID: "google-account-2", Email: "two@example.test", Name: "Two Example"},
				files:   make(map[string]*storedFile),
			},
		},
		current: "primary",
		access:  make(map[string]string),
		expired: make(map[string]bool),
		refresh: make(map[string]refreshRecord),
		codes:   make(map[string]codeRecord),
		control: Control{KeepRefresh: true},
	}
	return server
}

// URL is the fake's origin, suitable for every NATCATTY_GDRIVE_* base.
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
// next Drive call returns 401 while the refresh token stays usable.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.access {
		s.expired[token] = true
	}
}

// BumpModified advances one stored file's modifiedTime without changing its
// content. Tests use it to simulate a peer changing the file between the
// adapter's read and its update.
func (s *Server) BumpModified(accountKey, id string) error {
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
	file.modified = file.modified.Add(time.Second)
	return nil
}

// SeedFile inserts a file directly into one account's appDataFolder. Tests
// use it to place a non-product file on an earlier list page.
func (s *Server) SeedFile(accountKey, name string, content []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountKey]
	if !ok {
		return "", errors.New("fake: unknown account")
	}
	s.nextID++
	id := "fake-file-" + strconv.Itoa(s.nextID)
	account.files[id] = &storedFile{id: id, name: name, content: content, modified: s.now()}
	return id, nil
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
	s.control = Control{KeepRefresh: true}
	s.nextID = 0
	s.authorizations = 0
	s.tokenExchanges = 0
	s.refreshes = 0
	s.lists = 0
	s.creates = 0
	s.updates = 0
	s.downloads = 0
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
		Lists:            s.lists,
		Creates:          s.creates,
		Updates:          s.updates,
		Downloads:        s.downloads,
		SecretViolations: s.secretViolations,
		StateMismatches:  s.stateMismatches,
		Control:          s.control,
	}
	for _, key := range []string{"primary", "second"} {
		account := s.accounts[key]
		entry := AccountState{Account: account.Account}
		for _, file := range account.files {
			entry.Files = append(entry.Files, FileState{
				ID: file.id, Name: file.name, Size: len(file.content),
				Modified: file.modified.UTC().Format(time.RFC3339Nano),
			})
		}
		state.Accounts = append(state.Accounts, entry)
	}
	return state
}

// ServeHTTP routes the Google surface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == authorizePath && r.Method == http.MethodGet:
		s.authorize(w, r)
	case r.URL.Path == tokenPath && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == userInfoPath && r.Method == http.MethodGet:
		s.userInfo(w, r)
	case r.URL.Path == driveFilesPath && r.Method == http.MethodGet:
		s.listFiles(w, r)
	case strings.HasPrefix(r.URL.Path, driveFilesPath+"/") && r.Method == http.MethodGet:
		if r.URL.Query().Get("alt") == "media" {
			s.downloadFile(w, r)
		} else {
			s.fileMetadataGet(w, r)
		}
	case r.URL.Path == uploadFiles && r.Method == http.MethodPost:
		s.createFile(w, r)
	case strings.HasPrefix(r.URL.Path, uploadFiles+"/") && r.Method == http.MethodPatch:
		s.updateFile(w, r)
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
		"scope": "https://www.googleapis.com/auth/drive.appdata", "expires_in": 3600,
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
		"scope": "https://www.googleapis.com/auth/drive.appdata", "expires_in": 3600,
	}
	if !s.control.KeepRefresh {
		// Rotation: the presented refresh token is consumed and the replacement
		// is returned. With KeepRefresh the old token stays valid, matching
		// Google's usual response that omits refresh_token.
		delete(s.refresh, refresh)
		response["refresh_token"] = s.issueRefresh(record.accountKey)
	}
	writeJSON(w, http.StatusOK, response)
}

// issueAccess stores a fresh access token bound to the account.
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
		"id": account.ID, "email": account.Email, "name": account.Name, "verified_email": true,
	})
}

// accountFor resolves the bearer token and rejects an access token the
// control surface expired.
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

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("spaces") != "appDataFolder" {
		writeDriveError(w, http.StatusBadRequest, "only appDataFolder is supported")
		return
	}
	name := syncFileName
	if match := regexp.MustCompile(`name = '([^']+)'`).FindStringSubmatch(r.URL.Query().Get("q")); len(match) == 2 {
		name = match[1]
	}
	pageSize := 100
	s.mu.Lock()
	if s.control.PageSize > 0 {
		pageSize = s.control.PageSize
	}
	s.lists++
	offset := 0
	if raw := r.URL.Query().Get("pageToken"); raw != "" {
		offset, _ = strconv.Atoi(raw)
	}
	var matches []*storedFile
	keys := make([]string, 0, len(account.files))
	for key := range account.files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		file := account.files[key]
		if s.control.IgnoreNameFilter || file.name == name {
			matches = append(matches, file)
		}
	}
	files := make([]map[string]any, 0, pageSize)
	next := ""
	for index := offset; index < len(matches) && len(files) < pageSize; index++ {
		file := matches[index]
		files = append(files, map[string]any{
			"id": file.id, "name": file.name,
			"modifiedTime": file.modified.UTC().Format(time.RFC3339Nano),
			"size":         strconv.Itoa(len(file.content)),
		})
		if index+1 == offset+pageSize && index+1 < len(matches) {
			next = strconv.Itoa(index + 1)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "nextPageToken": next})
}

// fileMetadataGet answers GET /files/{id} without alt=media.
func (s *Server) fileMetadataGet(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, driveFilesPath+"/")
	s.mu.Lock()
	file, exists := account.files[id]
	var response map[string]any
	if exists {
		response = s.fileMetadata(file)
	}
	s.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, driveFilesPath+"/")
	s.mu.Lock()
	file, exists := account.files[id]
	if exists {
		s.downloads++
	}
	s.mu.Unlock()
	if !exists {
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
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		writeDriveError(w, http.StatusBadRequest, "a multipart/related body with a boundary is required")
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	metadataPart, err := reader.NextPart()
	if err != nil {
		writeDriveError(w, http.StatusBadRequest, "metadata part required")
		return
	}
	metadataBytes, _ := io.ReadAll(io.LimitReader(metadataPart, 1<<20))
	var metadata struct {
		Name    string   `json:"name"`
		Parents []string `json:"parents"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil || metadata.Name == "" {
		writeDriveError(w, http.StatusBadRequest, "invalid metadata")
		return
	}
	if len(metadata.Parents) != 1 || metadata.Parents[0] != "appDataFolder" {
		writeDriveError(w, http.StatusBadRequest, "the file must be created in appDataFolder")
		return
	}
	mediaPart, err := reader.NextPart()
	if err != nil {
		writeDriveError(w, http.StatusBadRequest, "media part required")
		return
	}
	content, _ := io.ReadAll(io.LimitReader(mediaPart, 64<<20))
	s.mu.Lock()
	s.nextID++
	id := "fake-file-" + strconv.Itoa(s.nextID)
	file := &storedFile{id: id, name: metadata.Name, content: content, modified: s.now()}
	account.files[id] = file
	s.creates++
	response := s.fileMetadata(file)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) updateFile(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFor(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, uploadFiles+"/")
	content, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	s.mu.Lock()
	file, exists := account.files[id]
	if exists {
		file.content = content
		file.modified = s.now()
		s.updates++
	}
	var response map[string]any
	if exists {
		response = s.fileMetadata(file)
	}
	s.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) fileMetadata(file *storedFile) map[string]any {
	return map[string]any{
		"id": file.id, "name": file.name,
		"modifiedTime": file.modified.UTC().Format(time.RFC3339Nano),
	}
}

type controlRequest struct {
	Account           *string `json:"account"`
	Deny              *bool   `json:"deny"`
	TamperState       *bool   `json:"tamperState"`
	InvalidateRefresh *bool   `json:"invalidateRefresh"`
	KeepRefresh       *bool   `json:"keepRefresh"`
	PageSize          *int    `json:"pageSize"`
	IgnoreNameFilter  *bool   `json:"ignoreNameFilter"`
	ExpireAccess      *bool   `json:"expireAccess"`
	Reset             *bool   `json:"reset"`
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
	if request.PageSize != nil {
		s.control.PageSize = *request.PageSize
	}
	if request.IgnoreNameFilter != nil {
		s.control.IgnoreNameFilter = *request.IgnoreNameFilter
	}
	if request.ExpireAccess != nil && *request.ExpireAccess {
		for token := range s.access {
			s.expired[token] = true
		}
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

func writeDriveError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": status, "message": message},
	})
}

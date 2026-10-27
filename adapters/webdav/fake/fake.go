// Package fake is an in-process WebDAV server. It mirrors the surface the
// webdav adapter uses: Basic, Digest (with a real challenge-response), and
// bearer authentication; OPTIONS, HEAD, GET, PUT, and MKCOL; strong ETags with
// If-Match and If-None-Match; 401, 403, 404, 412, redirects, and an optional
// self-signed TLS listener. It exposes control and state endpoints so tests
// and acceptance runs can force failures and inspect what the app did. All
// traffic stays on loopback; it never contacts a real WebDAV service.
package fake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCollection = "/dav"
	defaultRealm      = "Natcatty Fake"
	defaultUsername   = "dav-user"
	defaultPassword   = "dav-secret"
	defaultToken      = "fake-webdav-token-1"
	maxBodyBytes      = 64 << 20
	maxTraces         = 200
	// methodMKCOL is RFC 4918's collection creation method; net/http has no
	// constant for it.
	methodMKCOL = "MKCOL"
)

// Control is the mutable behavior of the fake. Secret fields are excluded from
// JSON; use ControlRequest on /__control.
type Control struct {
	AuthMode  string `json:"authMode"`
	Username  string `json:"username"`
	Password  string `json:"-"`
	Token     string `json:"-"`
	Forbidden bool   `json:"forbidden"`
	ReadOnly  bool   `json:"readOnly"`
	// ConflictWrites answers every conditional PUT to an existing file with
	// 412, so acceptance can observe the service's lasting conflict state.
	ConflictWrites bool   `json:"conflictWrites"`
	Redirect       string `json:"redirect"`
	DelayMS        int    `json:"delayMs"`
	DigestStale    bool   `json:"digestStale"`
	NoETag         bool   `json:"noETag"`
	DisableHead    bool   `json:"disableHead"`
	// RequireAuthForOptions makes OPTIONS participate in authentication.
	// OPTIONS is unauthenticated by default because capability discovery
	// normally precedes auth, and the adapter does not call it.
	RequireAuthForOptions bool `json:"requireAuthForOptions"`
	// DigestAlgorithm is advertised in the challenge: MD5, MD5-sess,
	// SHA-256, or SHA-256-sess.
	DigestAlgorithm string `json:"digestAlgorithm"`
}

// ControlState is the non-secret observable control surface.
type ControlState struct {
	AuthMode              string `json:"authMode"`
	Forbidden             bool   `json:"forbidden"`
	ReadOnly              bool   `json:"readOnly"`
	ConflictWrites        bool   `json:"conflictWrites"`
	Redirect              string `json:"redirect"`
	DelayMS               int    `json:"delayMs"`
	NoETag                bool   `json:"noETag"`
	DisableHead           bool   `json:"disableHead"`
	RequireAuthForOptions bool   `json:"requireAuthForOptions"`
	DigestAlgorithm       string `json:"digestAlgorithm"`
	StalePending          bool   `json:"stalePending"`
}

// ControlRequest is the /__control body. Pointers distinguish "leave alone"
// from an explicit zero value.
type ControlRequest struct {
	AuthMode              *string `json:"authMode"`
	Username              *string `json:"username"`
	Password              *string `json:"password"`
	Token                 *string `json:"token"`
	Forbidden             *bool   `json:"forbidden"`
	ReadOnly              *bool   `json:"readOnly"`
	ConflictWrites        *bool   `json:"conflictWrites"`
	Redirect              *string `json:"redirect"`
	DelayMS               *int    `json:"delayMs"`
	DigestStale           *bool   `json:"digestStale"`
	NoETag                *bool   `json:"noETag"`
	DisableHead           *bool   `json:"disableHead"`
	RequireAuthForOptions *bool   `json:"requireAuthForOptions"`
	DigestAlgorithm       *string `json:"digestAlgorithm"`
}

// Counters are the acceptance-visible observations.
type Counters struct {
	Requests             int `json:"requests"`
	Options              int `json:"options"`
	Heads                int `json:"heads"`
	Gets                 int `json:"gets"`
	Puts                 int `json:"puts"`
	Mkcols               int `json:"mkcols"`
	Unauthorized         int `json:"unauthorized"`
	Forbidden            int `json:"forbidden"`
	NotFound             int `json:"notFound"`
	PreconditionFailures int `json:"preconditionFailures"`
	Redirects            int `json:"redirects"`
	BasicSuccesses       int `json:"basicSuccesses"`
	DigestSuccesses      int `json:"digestSuccesses"`
	BearerSuccesses      int `json:"bearerSuccesses"`
	DigestChallenges     int `json:"digestChallenges"`
	SecretViolations     int `json:"secretViolations"`
}

// FileState is one stored file.
type FileState struct {
	Path     string `json:"path"`
	Size     int    `json:"size"`
	ETag     string `json:"etag"`
	Modified string `json:"modified"`
}

// Trace is one redacted request observation. Authorization is always the
// scheme followed by "<redacted>"; no secret is ever stored here.
type Trace struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Status        int    `json:"status"`
	AuthScheme    string `json:"authScheme"`
	Authorization string `json:"authorization"`
	IfMatch       string `json:"ifMatch,omitempty"`
	IfNoneMatch   string `json:"ifNoneMatch,omitempty"`
	ETag          string `json:"etag,omitempty"`
	Location      string `json:"location,omitempty"`
	ContentType   string `json:"contentType,omitempty"`
	UserAgent     string `json:"userAgent,omitempty"`
}

// State is the /__state body.
type State struct {
	URL           string       `json:"url"`
	TLS           bool         `json:"tls"`
	CollectionURL string       `json:"collectionUrl"`
	Files         []FileState  `json:"files"`
	Collections   []string     `json:"collections"`
	Counters      Counters     `json:"counters"`
	Control       ControlState `json:"control"`
	Traces        []Trace      `json:"traces"`
}

type storedFile struct {
	content  []byte
	etag     string
	modified time.Time
}

// Server is the fake WebDAV surface.
type Server struct {
	mu sync.Mutex

	listener net.Listener
	http     *http.Server
	baseURL  string
	tls      bool

	collection  string
	collections map[string]bool
	files       map[string]*storedFile
	control     Control
	nonces      map[string]bool
	nextNonce   int
	stale       bool
	redirects   int
	traces      []Trace
	counters    Counters
	now         func() time.Time
}

// Start listens on an ephemeral loopback port with Basic authentication.
func Start() (*Server, error) { return StartAt("127.0.0.1:0") }

// StartAt listens on address with Basic authentication.
func StartAt(address string) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := newServer()
	server.listener = listener
	server.baseURL = "http://" + listener.Addr().String()
	server.http = &http.Server{Handler: server}
	go func() { _ = server.http.Serve(listener) }()
	return server, nil
}

// StartTLS listens on an ephemeral loopback port with a fresh self-signed
// certificate. The certificate is deliberately untrusted, so the production
// adapter must reject it; no CA-ignore setting is added to the product.
func StartTLS() (*Server, error) { return StartTLSAt("127.0.0.1:0") }

// StartTLSAt listens on address with a fresh self-signed certificate.
func StartTLSAt(address string) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	certificate, err := selfSignedCertificate()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := newServer()
	server.listener = listener
	server.tls = true
	server.baseURL = "https://" + listener.Addr().String()
	server.http = &http.Server{Handler: server}
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	go func() { _ = server.http.Serve(tlsListener) }()
	return server, nil
}

func newServer() *Server {
	server := &Server{
		collection:  defaultCollection,
		collections: map[string]bool{"/": true, defaultCollection: true},
		files:       map[string]*storedFile{},
		control: Control{
			AuthMode:        "basic",
			Username:        defaultUsername,
			Password:        defaultPassword,
			Token:           defaultToken,
			DigestAlgorithm: "MD5",
		},
		nonces: map[string]bool{},
		now:    time.Now,
	}
	return server
}

// URL is the fake's origin.
func (s *Server) URL() string { return s.baseURL }

// CollectionURL is the default collection's full URL.
func (s *Server) CollectionURL() string { return s.baseURL + s.collection }

// CollectionPath is the default collection path.
func (s *Server) CollectionPath() string { return s.collection }

// Close stops the fake.
func (s *Server) Close() {
	if s.http != nil {
		_ = s.http.Close()
	}
}

// Configure mutates the control surface in-process.
func (s *Server) Configure(apply func(*Control)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.control.Redirect
	apply(&s.control)
	if s.control.AuthMode == "" {
		s.control.AuthMode = "basic"
	}
	if s.control.DigestAlgorithm == "" {
		s.control.DigestAlgorithm = "MD5"
	}
	if s.control.Redirect != previous {
		s.redirects = 0
	}
	if s.control.DigestStale {
		s.stale = true
	}
}

// Reset returns the fake to its documented initial state: default Basic
// credentials, one empty collection, zero counters, and no traces.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections = map[string]bool{"/": true, s.collection: true}
	s.files = map[string]*storedFile{}
	s.control = Control{
		AuthMode:        "basic",
		Username:        defaultUsername,
		Password:        defaultPassword,
		Token:           defaultToken,
		DigestAlgorithm: "MD5",
	}
	s.nonces = map[string]bool{}
	s.nextNonce = 0
	s.stale = false
	s.redirects = 0
	s.traces = nil
	s.counters = Counters{}
}

// Credentials returns the current non-secret username and the auth mode. It
// intentionally never returns the password or token.
func (s *Server) Credentials() (mode, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.control.AuthMode, s.control.Username
}

// SeedFile places a file directly, as a peer would.
func (s *Server) SeedFile(collectionPath, name string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	collectionPath = normalizeCollection(collectionPath)
	if !s.collections[collectionPath] {
		return fmt.Errorf("fake: collection %q does not exist", collectionPath)
	}
	s.files[path.Join(collectionPath, name)] = &storedFile{
		content:  append([]byte(nil), content...),
		etag:     s.newETagLocked(),
		modified: s.now(),
	}
	return nil
}

// CreateCollection creates an empty collection directly, as an administrator
// would before choosing it as the sync folder. It is used by tests and
// acceptance runs that need a second collection without sending an
// unauthenticated administrative MKCOL.
func (s *Server) CreateCollection(collectionPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	collectionPath = normalizeCollection(collectionPath)
	if s.collections[collectionPath] {
		return nil
	}
	parent := path.Dir(collectionPath)
	if !s.collections[parent] {
		return fmt.Errorf("fake: parent collection %q does not exist", parent)
	}
	s.collections[collectionPath] = true
	return nil
}

// SetCollection chooses the collection CollectionURL names and makes it
// exist, creating one missing level below an existing parent. It is a test
// hook; production clients still address whatever URL the user entered.
func (s *Server) SetCollection(collectionPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	collectionPath = normalizeCollection(collectionPath)
	if !s.collections[path.Dir(collectionPath)] {
		return fmt.Errorf("fake: parent collection %q does not exist", path.Dir(collectionPath))
	}
	s.collections[collectionPath] = true
	s.collection = collectionPath
	return nil
}

// FileContent returns a stored file's bytes.
func (s *Server) FileContent(collectionPath, name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[path.Join(normalizeCollection(collectionPath), name)]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), file.content...), true
}

// BumpETag advances a file's ETag without changing its content, simulating a
// peer that touched the file between the adapter's read and conditional write.
func (s *Server) BumpETag(collectionPath, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[path.Join(normalizeCollection(collectionPath), name)]
	if !ok {
		return fmt.Errorf("fake: %s does not exist", name)
	}
	file.etag = s.newETagLocked()
	file.modified = file.modified.Add(time.Second)
	return nil
}

// State returns an acceptance snapshot.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := State{
		URL:           s.baseURL,
		TLS:           s.tls,
		CollectionURL: s.baseURL + s.collection,
		Counters:      s.counters,
		Control: ControlState{
			AuthMode:              s.control.AuthMode,
			Forbidden:             s.control.Forbidden,
			ReadOnly:              s.control.ReadOnly,
			Redirect:              s.control.Redirect,
			DelayMS:               s.control.DelayMS,
			NoETag:                s.control.NoETag,
			DisableHead:           s.control.DisableHead,
			RequireAuthForOptions: s.control.RequireAuthForOptions,
			DigestAlgorithm:       s.control.DigestAlgorithm,
			StalePending:          s.stale,
		},
	}
	paths := make([]string, 0, len(s.collections))
	for collection := range s.collections {
		paths = append(paths, collection)
	}
	sort.Strings(paths)
	state.Collections = paths
	filePaths := make([]string, 0, len(s.files))
	for filePath := range s.files {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	for _, filePath := range filePaths {
		file := s.files[filePath]
		state.Files = append(state.Files, FileState{
			Path:     filePath,
			Size:     len(file.content),
			ETag:     file.etag,
			Modified: file.modified.UTC().Format(time.RFC3339Nano),
		})
	}
	state.Traces = append([]Trace(nil), s.traces...)
	return state
}

// ServeHTTP routes requests and records a redacted trace.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/__state" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.State())
		return
	case r.URL.Path == "/__control" && r.Method == http.MethodPost:
		s.controlEndpoint(w, r)
		return
	}

	s.mu.Lock()
	delay := s.control.DelayMS
	redirect := s.control.Redirect
	shouldRedirect := redirect != "" && (redirect == "loop" || s.redirects == 0)
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	if shouldRedirect {
		s.redirect(recorder, r, redirect)
	} else {
		s.handle(recorder, r)
	}
	s.record(r, recorder)
}

func (s *Server) handle(w *statusRecorder, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		s.options(w, r)
	case http.MethodHead, http.MethodGet:
		s.read(w, r)
	case http.MethodPut:
		s.put(w, r)
	case methodMKCOL:
		s.mkcol(w, r)
	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, MKCOL")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// options advertises DAV capabilities. It is unauthenticated unless the
// control flag requires auth.
func (s *Server) options(w *statusRecorder, r *http.Request) {
	s.mu.Lock()
	requireAuth := s.control.RequireAuthForOptions
	s.counters.Options++
	s.mu.Unlock()
	if requireAuth && !s.authenticate(w, r) {
		return
	}
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, MKCOL")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) read(w *statusRecorder, r *http.Request) {
	s.mu.Lock()
	disableHead := s.control.DisableHead
	if r.Method == http.MethodHead {
		s.counters.Heads++
	} else {
		s.counters.Gets++
	}
	s.mu.Unlock()
	if r.Method == http.MethodHead && disableHead {
		w.Header().Set("Allow", "GET, PUT, MKCOL")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticate(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.control.Forbidden {
		s.counters.Forbidden++
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	file, ok := s.files[r.URL.Path]
	if !ok {
		s.counters.NotFound++
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if match := r.Header.Get("If-Match"); match != "" && !etagMatches(match, file.etag) {
		s.counters.PreconditionFailures++
		s.writeETagLocked(w, file)
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if none := r.Header.Get("If-None-Match"); none != "" && etagMatches(none, file.etag) {
		s.writeETagLocked(w, file)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.writeETagLocked(w, file)
	http.ServeContent(w, r, "", file.modified, strings.NewReader(string(file.content)))
}

func (s *Server) put(w *statusRecorder, r *http.Request) {
	s.mu.Lock()
	s.counters.Puts++
	s.mu.Unlock()
	if !s.authenticate(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.control.Forbidden || s.control.ReadOnly {
		s.counters.Forbidden++
		http.Error(w, "read only", http.StatusForbidden)
		return
	}
	parent := path.Dir(r.URL.Path)
	if !s.collections[parent] {
		http.Error(w, "collection does not exist", http.StatusConflict)
		return
	}
	current, exists := s.files[r.URL.Path]
	if s.control.ConflictWrites && exists && (r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "") {
		s.counters.PreconditionFailures++
		s.writeETagLocked(w, current)
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	ifMatch := r.Header.Get("If-Match")
	ifNoneMatch := r.Header.Get("If-None-Match")
	if ifNoneMatch == "*" && exists {
		s.counters.PreconditionFailures++
		s.writeETagLocked(w, current)
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if ifMatch != "" && (!exists || !etagMatches(ifMatch, current.etag)) {
		s.counters.PreconditionFailures++
		if exists {
			s.writeETagLocked(w, current)
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	status := http.StatusNoContent
	if !exists {
		status = http.StatusCreated
	}
	file := &storedFile{content: body, etag: s.newETagLocked(), modified: s.now()}
	s.files[r.URL.Path] = file
	s.writeETagLocked(w, file)
	w.WriteHeader(status)
}

func (s *Server) mkcol(w *statusRecorder, r *http.Request) {
	s.mu.Lock()
	s.counters.Mkcols++
	s.mu.Unlock()
	if !s.authenticate(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.control.Forbidden || s.control.ReadOnly {
		s.counters.Forbidden++
		http.Error(w, "read only", http.StatusForbidden)
		return
	}
	collectionPath := normalizeCollection(r.URL.Path)
	if s.collections[collectionPath] {
		http.Error(w, "collection already exists", http.StatusMethodNotAllowed)
		return
	}
	if !s.collections[path.Dir(collectionPath)] {
		http.Error(w, "parent collection does not exist", http.StatusConflict)
		return
	}
	s.collections[collectionPath] = true
	w.WriteHeader(http.StatusCreated)
}

// authenticate validates the Authorization header for the configured mode and
// writes the 401 challenge when it is absent or wrong.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	header := r.Header.Get("Authorization")
	switch strings.ToLower(s.control.AuthMode) {
	case "bearer":
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(header[len(prefix):])), []byte(s.control.Token)) != 1 {
			s.unauthorizedLocked(w, "Bearer")
			return false
		}
		s.counters.BearerSuccesses++
		return true
	case "digest":
		if !strings.HasPrefix(strings.ToLower(header), "digest ") {
			s.challengeDigestLocked(w, false)
			return false
		}
		if !s.verifyDigestLocked(r, header) {
			s.challengeDigestLocked(w, false)
			return false
		}
		if s.stale {
			s.stale = false
			s.control.DigestStale = false
			s.challengeDigestLocked(w, true)
			return false
		}
		s.counters.DigestSuccesses++
		return true
	default:
		const prefix = "Basic "
		if !strings.HasPrefix(header, prefix) {
			s.unauthorizedLocked(w, "Basic")
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
		expected := s.control.Username + ":" + s.control.Password
		if err != nil || subtle.ConstantTimeCompare(decoded, []byte(expected)) != 1 {
			s.unauthorizedLocked(w, "Basic")
			return false
		}
		s.counters.BasicSuccesses++
		return true
	}
}

func (s *Server) unauthorizedLocked(w http.ResponseWriter, scheme string) {
	s.counters.Unauthorized++
	if scheme == "Basic" {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+defaultRealm+`", charset="UTF-8"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+defaultRealm+`"`)
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// challengeDigestLocked issues a fresh nonce and the configured algorithm.
// stale tells the client its request was correct but the nonce expired.
func (s *Server) challengeDigestLocked(w http.ResponseWriter, stale bool) {
	s.nextNonce++
	nonce := "fake-nonce-" + strconv.Itoa(s.nextNonce)
	if len(s.nonces) > 1000 {
		s.nonces = map[string]bool{}
	}
	s.nonces[nonce] = true
	algorithm := s.control.DigestAlgorithm
	if algorithm == "" {
		algorithm = "MD5"
	}
	value := fmt.Sprintf(`Digest realm="%s", nonce="%s", opaque="fake-opaque", algorithm=%s, qop="auth"`,
		defaultRealm, nonce, algorithm)
	if stale {
		value += ", stale=true"
	}
	s.counters.DigestChallenges++
	if !stale {
		s.counters.Unauthorized++
	}
	w.Header().Set("WWW-Authenticate", value)
	w.WriteHeader(http.StatusUnauthorized)
}

func (s *Server) verifyDigestLocked(r *http.Request, header string) bool {
	params := parseDigestAuthorization(header)
	username := params["username"]
	realm := params["realm"]
	nonce := params["nonce"]
	uri := params["uri"]
	response := params["response"]
	qop := params["qop"]
	nc := params["nc"]
	cnonce := params["cnonce"]
	algorithm := params["algorithm"]
	if username != s.control.Username || realm != defaultRealm ||
		!s.nonces[nonce] || uri != r.URL.RequestURI() ||
		qop != "auth" || nc != "00000001" || cnonce == "" || response == "" {
		return false
	}
	newHash, canonical, session, err := challengeHash(algorithm)
	if err != nil || !strings.EqualFold(canonical, s.control.DigestAlgorithm) {
		return false
	}
	digest := func(value string) string {
		sum := newHash()
		sum.Write([]byte(value))
		return fmt.Sprintf("%x", sum.Sum(nil))
	}
	ha1 := digest(username + ":" + realm + ":" + s.control.Password)
	if session {
		ha1 = digest(ha1 + ":" + nonce + ":" + cnonce)
	}
	ha2 := digest(r.Method + ":" + uri)
	expected := digest(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(response)) == 1
}

// challengeHash maps an advertised Digest algorithm onto its hash.
func challengeHash(algorithm string) (func() hash.Hash, string, bool, error) {
	switch strings.ToLower(algorithm) {
	case "", "md5":
		return md5.New, "MD5", false, nil
	case "md5-sess":
		return md5.New, "MD5-sess", true, nil
	case "sha-256":
		return sha256.New, "SHA-256", false, nil
	case "sha-256-sess":
		return sha256.New, "SHA-256-sess", true, nil
	default:
		return nil, "", false, fmt.Errorf("fake: unsupported algorithm %q", algorithm)
	}
}

func (s *Server) redirect(w *statusRecorder, r *http.Request, mode string) {
	s.mu.Lock()
	s.redirects++
	s.counters.Redirects++
	s.mu.Unlock()
	target := r.URL.Path
	switch mode {
	case "other-host":
		host := r.Host
		if strings.HasPrefix(host, "127.0.0.1") {
			host = "localhost" + strings.TrimPrefix(host, "127.0.0.1")
		} else {
			host = "127.0.0.1" + strings.TrimPrefix(host, "localhost")
		}
		target = "http://" + host + r.URL.Path
	case "downgrade":
		target = "http://" + r.Host + r.URL.Path
	case "loop":
		// Same URL again; the client's redirect limit must stop it.
	case "same-host":
		target = r.URL.Path + "?redirected=1"
	default:
		target = r.URL.Path
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

func (s *Server) record(r *http.Request, w *statusRecorder) {
	scheme, redacted := redactAuthorization(r.Header.Get("Authorization"))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters.Requests++
	if s.secretInRequestLocked(r) {
		s.counters.SecretViolations++
	}
	trace := Trace{
		Method:        r.Method,
		Path:          r.URL.Path,
		Status:        w.status,
		AuthScheme:    scheme,
		Authorization: redacted,
		IfMatch:       r.Header.Get("If-Match"),
		IfNoneMatch:   r.Header.Get("If-None-Match"),
		ETag:          w.Header().Get("ETag"),
		Location:      w.Header().Get("Location"),
		ContentType:   r.Header.Get("Content-Type"),
		UserAgent:     r.Header.Get("User-Agent"),
	}
	s.traces = append(s.traces, trace)
	if len(s.traces) > maxTraces {
		s.traces = s.traces[len(s.traces)-maxTraces:]
	}
}

// secretInRequestLocked detects a credential escaping the Authorization
// header, for example a bearer token placed in a query string.
func (s *Server) secretInRequestLocked(r *http.Request) bool {
	secrets := []string{s.control.Password, s.control.Token}
	haystack := r.URL.String()
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		haystack += " " + strings.Join(values, " ")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(haystack, secret) {
			return true
		}
	}
	return false
}

// redactAuthorization keeps only the scheme.
func redactAuthorization(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	scheme := value
	if index := strings.IndexByte(value, ' '); index >= 0 {
		scheme = value[:index]
	}
	return scheme, scheme + " <redacted>"
}

// parseDigestAuthorization parses the adapter's Digest header. It is the
// fake's independent implementation, not shared with the adapter.
func parseDigestAuthorization(header string) map[string]string {
	params := map[string]string{}
	index := strings.IndexByte(header, ' ')
	if index < 0 {
		return params
	}
	input := header[index:]
	position := 0
	for position < len(input) {
		for position < len(input) && (input[position] == ' ' || input[position] == '\t' || input[position] == ',') {
			position++
		}
		start := position
		for position < len(input) && isToken(input[position]) {
			position++
		}
		key := strings.ToLower(input[start:position])
		if key == "" {
			break
		}
		for position < len(input) && (input[position] == ' ' || input[position] == '\t') {
			position++
		}
		if position >= len(input) || input[position] != '=' {
			break
		}
		position++
		for position < len(input) && (input[position] == ' ' || input[position] == '\t') {
			position++
		}
		var value string
		if position < len(input) && input[position] == '"' {
			position++
			var builder strings.Builder
			for position < len(input) {
				if input[position] == '\\' && position+1 < len(input) {
					builder.WriteByte(input[position+1])
					position += 2
					continue
				}
				if input[position] == '"' {
					position++
					break
				}
				builder.WriteByte(input[position])
				position++
			}
			value = builder.String()
		} else {
			start := position
			for position < len(input) && input[position] != ',' && input[position] != ' ' && input[position] != '\t' {
				position++
			}
			value = input[start:position]
		}
		params[key] = value
	}
	return params
}

func isToken(value byte) bool {
	switch value {
	case ' ', '\t', ',', '=', '"', '\\':
		return false
	}
	return value > 0x1f && value < 0x7f
}

func (s *Server) controlEndpoint(w http.ResponseWriter, r *http.Request) {
	var request ControlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid control request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	previous := s.control.Redirect
	if request.AuthMode != nil {
		s.control.AuthMode = *request.AuthMode
	}
	if request.Username != nil {
		s.control.Username = *request.Username
	}
	if request.Password != nil {
		s.control.Password = *request.Password
	}
	if request.Token != nil {
		s.control.Token = *request.Token
	}
	if request.Forbidden != nil {
		s.control.Forbidden = *request.Forbidden
	}
	if request.ReadOnly != nil {
		s.control.ReadOnly = *request.ReadOnly
	}
	if request.ConflictWrites != nil {
		s.control.ConflictWrites = *request.ConflictWrites
	}
	if request.Redirect != nil {
		s.control.Redirect = *request.Redirect
	}
	if request.DelayMS != nil {
		s.control.DelayMS = *request.DelayMS
	}
	if request.DigestStale != nil {
		s.stale = *request.DigestStale
	}
	if request.NoETag != nil {
		s.control.NoETag = *request.NoETag
	}
	if request.DisableHead != nil {
		s.control.DisableHead = *request.DisableHead
	}
	if request.RequireAuthForOptions != nil {
		s.control.RequireAuthForOptions = *request.RequireAuthForOptions
	}
	if request.DigestAlgorithm != nil {
		s.control.DigestAlgorithm = *request.DigestAlgorithm
	}
	if s.control.AuthMode == "" {
		s.control.AuthMode = "basic"
	}
	if s.control.DigestAlgorithm == "" {
		s.control.DigestAlgorithm = "MD5"
	}
	if s.control.Redirect != previous {
		s.redirects = 0
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.State())
}

func (s *Server) newETagLocked() string {
	s.nextNonce++
	return `"fake-etag-` + strconv.Itoa(s.nextNonce) + `"`
}

func (s *Server) writeETagLocked(w http.ResponseWriter, file *storedFile) {
	if !s.control.NoETag {
		w.Header().Set("ETag", file.etag)
	}
}

func etagMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}

func normalizeCollection(value string) string {
	if value == "" {
		return "/"
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(value, "/"))
	if cleaned == "/" {
		return "/"
	}
	return strings.TrimRight(cleaned, "/")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	return r.ResponseWriter.Write(data)
}

// selfSignedCertificate creates an untrusted loopback certificate. It is
// ephemeral and never written to disk.
func selfSignedCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Natcatty Fake WebDAV"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certificatePEM, keyPEM)
}

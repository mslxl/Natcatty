// Package fake is an in-process S3-compatible server. It mirrors the surface
// the s3 adapter uses: path-style GET, HEAD, and conditional PUT on one bucket,
// with AWS Signature Version 4 verified server-side from the received request,
// an enforced optional session token, wrong-region PermanentRedirect, 403,
// 404 NoSuchKey, 412 PreconditionFailed, arbitrary-byte bodies, and MD5 ETags.
//
// Verification is implemented here independently of the adapter's signer, so
// a canonicalization mistake shared by both sides cannot hide: the adapter's
// signer is separately pinned to Amazon's published vectors. All traffic stays
// on loopback; the fake never contacts AWS or any other real service.
package fake

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAccessKeyID     = "fake-s3-access-key"
	DefaultSecretAccessKey = "fake-s3-secret-key"
	DefaultBucket          = "natcatty-test"
	DefaultRegion          = "us-east-1"
	maxBodyBytes           = 64 << 20
	maxTraces              = 200
	algorithm              = "AWS4-HMAC-SHA256"
	service                = "s3"
)

// Control is the mutable behavior of the fake. Secret fields are excluded from
// JSON state; use ControlRequest on /__control.
type Control struct {
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	SessionToken    string `json:"-"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	// BasePath is an optional path prefix the fake serves under, used to prove
	// the client honors an endpoint with a base path. It is normalized to
	// "/name" or "".
	BasePath    string `json:"basePath"`
	Forbidden   bool   `json:"forbidden"`
	ServerError bool   `json:"serverError"`
	ReadOnly    bool   `json:"readOnly"`
	// ConflictWrites answers every conditional PUT with 412, so acceptance can
	// observe the service's lasting conflict state.
	ConflictWrites bool `json:"conflictWrites"`
	DelayMS        int  `json:"delayMs"`
}

// ControlState is the non-secret observable control surface.
type ControlState struct {
	Bucket             string `json:"bucket"`
	Region             string `json:"region"`
	BasePath           string `json:"basePath,omitempty"`
	Forbidden          bool   `json:"forbidden"`
	ServerError        bool   `json:"serverError"`
	ReadOnly           bool   `json:"readOnly"`
	ConflictWrites     bool   `json:"conflictWrites"`
	DelayMS            int    `json:"delayMs"`
	SessionTokenNeeded bool   `json:"sessionTokenNeeded"`
	AccessKeyIDSet     bool   `json:"accessKeyIdSet"`
}

// ControlRequest is the /__control body. Pointers distinguish "leave alone"
// from an explicit zero value. The secret fields are accepted here and never
// echoed back.
type ControlRequest struct {
	AccessKeyID     *string `json:"accessKeyId"`
	SecretAccessKey *string `json:"secretAccessKey"`
	SessionToken    *string `json:"sessionToken"`
	Bucket          *string `json:"bucket"`
	Region          *string `json:"region"`
	BasePath        *string `json:"basePath"`
	Forbidden       *bool   `json:"forbidden"`
	ServerError     *bool   `json:"serverError"`
	ReadOnly        *bool   `json:"readOnly"`
	ConflictWrites  *bool   `json:"conflictWrites"`
	DelayMS         *int    `json:"delayMs"`
}

// Counters are the acceptance-visible observations.
type Counters struct {
	Requests             int `json:"requests"`
	Gets                 int `json:"gets"`
	Heads                int `json:"heads"`
	Puts                 int `json:"puts"`
	Verified             int `json:"verified"`
	SignatureFailures    int `json:"signatureFailures"`
	SessionTokenFailures int `json:"sessionTokenFailures"`
	Unauthorized         int `json:"unauthorized"`
	Forbidden            int `json:"forbidden"`
	NotFound             int `json:"notFound"`
	PreconditionFailures int `json:"preconditionFailures"`
	Redirects            int `json:"redirects"`
	BucketMismatches     int `json:"bucketMismatches"`
	PathStyleRequests    int `json:"pathStyleRequests"`
	SecretViolations     int `json:"secretViolations"`
	BytesWritten         int `json:"bytesWritten"`
	BytesRead            int `json:"bytesRead"`
}

// FileState is one stored object.
type FileState struct {
	Key         string `json:"key"`
	Size        int    `json:"size"`
	ETag        string `json:"etag"`
	Modified    string `json:"modified"`
	ContentType string `json:"contentType,omitempty"`
}

// Trace is one redacted request observation. Authorization is always the
// scheme followed by "<redacted>"; neither the secret access key nor the
// session token is ever stored here.
type Trace struct {
	Method              string `json:"method"`
	Path                string `json:"path"`
	Bucket              string `json:"bucket,omitempty"`
	Key                 string `json:"key,omitempty"`
	PathStyle           bool   `json:"pathStyle,omitempty"`
	Status              int    `json:"status"`
	AuthScheme          string `json:"authScheme,omitempty"`
	Authorization       string `json:"authorization,omitempty"`
	SignedHeaders       string `json:"signedHeaders,omitempty"`
	Region              string `json:"region,omitempty"`
	SessionTokenPresent bool   `json:"sessionTokenPresent,omitempty"`
	IfMatch             string `json:"ifMatch,omitempty"`
	IfNoneMatch         string `json:"ifNoneMatch,omitempty"`
	ETag                string `json:"etag,omitempty"`
	ContentSHA256       string `json:"contentSha256,omitempty"`
	Bytes               int    `json:"bytes,omitempty"`
}

// State is the /__state body.
type State struct {
	URL      string       `json:"url"`
	Bucket   string       `json:"bucket"`
	Region   string       `json:"region"`
	Files    []FileState  `json:"files"`
	Counters Counters     `json:"counters"`
	Control  ControlState `json:"control"`
	Traces   []Trace      `json:"traces"`
}

type storedObject struct {
	content     []byte
	etag        string
	modified    time.Time
	contentType string
}

// Server is the fake S3 surface.
type Server struct {
	mu sync.Mutex

	listener net.Listener
	http     *http.Server
	baseURL  string

	control   Control
	objects   map[string]*storedObject
	traces    []Trace
	counters  Counters
	bumpCount int
	now       func() time.Time
}

// Start listens on an ephemeral loopback port.
func Start() (*Server, error) { return StartAt("127.0.0.1:0") }

// StartAt listens on address.
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

func newServer() *Server {
	return &Server{
		objects: map[string]*storedObject{},
		control: Control{
			AccessKeyID:     DefaultAccessKeyID,
			SecretAccessKey: DefaultSecretAccessKey,
			Bucket:          DefaultBucket,
			Region:          DefaultRegion,
		},
		now: time.Now,
	}
}

// URL is the fake's origin.
func (s *Server) URL() string { return s.baseURL }

// Bucket is the configured bucket name.
func (s *Server) Bucket() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.control.Bucket
}

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
	apply(&s.control)
	s.applyDefaultsLocked()
}

func (s *Server) applyDefaultsLocked() {
	if s.control.AccessKeyID == "" {
		s.control.AccessKeyID = DefaultAccessKeyID
	}
	if s.control.SecretAccessKey == "" {
		s.control.SecretAccessKey = DefaultSecretAccessKey
	}
	if s.control.Bucket == "" {
		s.control.Bucket = DefaultBucket
	}
	if s.control.Region == "" {
		s.control.Region = DefaultRegion
	}
	s.control.BasePath = strings.Trim(strings.TrimSpace(s.control.BasePath), "/")
	if s.control.BasePath != "" {
		s.control.BasePath = "/" + s.control.BasePath
	}
}

// Reset returns the fake to its documented initial state: default credentials,
// an empty bucket, zero counters, and no traces.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.control = Control{
		AccessKeyID:     DefaultAccessKeyID,
		SecretAccessKey: DefaultSecretAccessKey,
		Bucket:          DefaultBucket,
		Region:          DefaultRegion,
	}
	s.objects = map[string]*storedObject{}
	s.traces = nil
	s.counters = Counters{}
	s.bumpCount = 0
}

// Credentials returns the non-secret access key id and bucket. It never
// returns the secret access key or the session token.
func (s *Server) Credentials() (accessKeyID, bucket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.control.AccessKeyID, s.control.Bucket
}

// SeedObject places or replaces an object directly, as a peer would.
func (s *Server) SeedObject(key string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = &storedObject{
		content:     append([]byte(nil), content...),
		etag:        etagOf(content),
		modified:    s.now(),
		contentType: "application/json",
	}
}

// ObjectContent returns a stored object's bytes.
func (s *Server) ObjectContent(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), object.content...), true
}

// ObjectETag returns a stored object's ETag.
func (s *Server) ObjectETag(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return "", false
	}
	return object.etag, true
}

// BumpETag advances an object's ETag without changing its content, simulating
// a peer that touched the object between the adapter's read and conditional
// write.
func (s *Server) BumpETag(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return fmt.Errorf("fake: %s does not exist", key)
	}
	s.bumpCount++
	object.etag = fmt.Sprintf("\"bumped-%d\"", s.bumpCount)
	object.modified = object.modified.Add(time.Second)
	return nil
}

// State returns an acceptance snapshot.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := State{
		URL:      s.baseURL,
		Bucket:   s.control.Bucket,
		Region:   s.control.Region,
		Counters: s.counters,
		Control: ControlState{
			Bucket:             s.control.Bucket,
			Region:             s.control.Region,
			BasePath:           s.control.BasePath,
			Forbidden:          s.control.Forbidden,
			ServerError:        s.control.ServerError,
			ReadOnly:           s.control.ReadOnly,
			ConflictWrites:     s.control.ConflictWrites,
			DelayMS:            s.control.DelayMS,
			SessionTokenNeeded: s.control.SessionToken != "",
			AccessKeyIDSet:     s.control.AccessKeyID != "",
		},
	}
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		object := s.objects[key]
		state.Files = append(state.Files, FileState{
			Key:         key,
			Size:        len(object.content),
			ETag:        object.etag,
			Modified:    object.modified.UTC().Format(time.RFC3339Nano),
			ContentType: object.contentType,
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
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.handle(recorder, r)
	s.record(r, recorder)
}

func (s *Server) handle(w *statusRecorder, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "IncompleteBody", "cannot read body")
		return
	}
	s.mu.Lock()
	bucket := s.control.Bucket
	region := s.control.Region
	basePath := s.control.BasePath
	forbidden := s.control.Forbidden
	serverError := s.control.ServerError
	readOnly := s.control.ReadOnly
	conflictWrites := s.control.ConflictWrites
	s.mu.Unlock()

	scope, authErr := s.authenticate(r, body)
	if authErr != nil {
		s.recordAuthFailure(authErr, r)
		writeError(w, authErr.status, authErr.code, authErr.message)
		return
	}
	if scope.region != region {
		// The signature is valid for its own scope, but this bucket lives
		// elsewhere. Real S3 answers 301 and the original SDK client did not
		// follow it; the adapter reports a wrong region.
		s.mu.Lock()
		s.counters.Redirects++
		s.mu.Unlock()
		w.Header().Set("x-amz-bucket-region", region)
		writeError(w, http.StatusMovedPermanently, "PermanentRedirect",
			"the bucket is in region "+region)
		return
	}
	s.mu.Lock()
	s.counters.Verified++
	s.mu.Unlock()
	if forbidden {
		s.mu.Lock()
		s.counters.Forbidden++
		s.mu.Unlock()
		writeError(w, http.StatusForbidden, "AccessDenied", "access denied")
		return
	}
	if serverError {
		writeError(w, http.StatusInternalServerError, "InternalError", "the fake failed on purpose")
		return
	}

	requestPath := r.URL.Path
	if basePath != "" {
		if requestPath != basePath && !strings.HasPrefix(requestPath, basePath+"/") {
			writeError(w, http.StatusNotFound, "NoSuchBucket", "the request is outside the configured base path")
			return
		}
		requestPath = strings.TrimPrefix(requestPath, basePath)
	}
	path := strings.TrimPrefix(requestPath, "/")
	first, key, hasKey := strings.Cut(path, "/")
	s.mu.Lock()
	if first == bucket {
		s.counters.PathStyleRequests++
	}
	s.mu.Unlock()
	if first != bucket {
		s.mu.Lock()
		s.counters.BucketMismatches++
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "NoSuchBucket", "the bucket does not exist")
		return
	}
	if !hasKey || key == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "the fake supports single-object requests only")
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.read(w, r, key, r.Method == http.MethodHead)
	case http.MethodPut:
		if readOnly {
			s.mu.Lock()
			s.counters.Forbidden++
			s.mu.Unlock()
			writeError(w, http.StatusForbidden, "AccessDenied", "the bucket is read-only")
			return
		}
		s.put(w, r, key, body, conflictWrites)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "unsupported method")
	}
}

type credentialScope struct {
	accessKeyID string
	date        string
	region      string
	service     string
	terminator  string
}

type authError struct {
	status  int
	code    string
	message string
}

func (s *Server) authenticate(r *http.Request, body []byte) (credentialScope, *authError) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "AccessDenied", message: "request is not signed"}
	}
	if !strings.HasPrefix(header, algorithm+" ") {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "unsupported signature algorithm"}
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(header, algorithm+" "), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	credential := fields["Credential"]
	signedHeadersField := fields["SignedHeaders"]
	signature := fields["Signature"]
	if credential == "" || signedHeadersField == "" || signature == "" {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "incomplete Authorization header"}
	}
	scopeParts := strings.Split(credential, "/")
	if len(scopeParts) != 5 {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "malformed credential scope"}
	}
	scope := credentialScope{
		accessKeyID: scopeParts[0], date: scopeParts[1], region: scopeParts[2],
		service: scopeParts[3], terminator: scopeParts[4],
	}

	s.mu.Lock()
	expectedAccessKey := s.control.AccessKeyID
	expectedSecret := s.control.SecretAccessKey
	expectedToken := s.control.SessionToken
	s.mu.Unlock()

	if subtle.ConstantTimeCompare([]byte(scope.accessKeyID), []byte(expectedAccessKey)) != 1 {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "InvalidAccessKeyId", message: "unknown access key"}
	}
	if scope.service != service || scope.terminator != "aws4_request" {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "wrong service scope"}
	}

	amzDate := r.Header.Get("x-amz-date")
	parsedTime, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil || parsedTime.UTC().Format("20060102") != scope.date {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "missing or mismatched x-amz-date"}
	}

	token := r.Header.Get("x-amz-security-token")
	tokenPresent := token != ""
	if expectedToken != "" {
		if subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			s.mu.Lock()
			s.counters.SessionTokenFailures++
			s.mu.Unlock()
			return credentialScope{}, &authError{status: http.StatusForbidden, code: "InvalidToken", message: "the session token is missing or wrong"}
		}
	} else if tokenPresent {
		s.mu.Lock()
		s.counters.SessionTokenFailures++
		s.mu.Unlock()
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "InvalidToken", message: "the request carried an unexpected session token"}
	}

	signedNames := strings.Split(signedHeadersField, ";")
	signed := map[string]bool{}
	for _, name := range signedNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || signed[name] {
			return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "malformed SignedHeaders"}
		}
		signed[name] = true
	}
	for _, required := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		if !signed[required] {
			return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "required header " + required + " is not signed"}
		}
	}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") && !signed[lower] {
			return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "x-amz header " + lower + " is not signed"}
		}
	}

	payloadHash := r.Header.Get("x-amz-content-sha256")
	actualHash := sha256Hex(body)
	if subtle.ConstantTimeCompare([]byte(payloadHash), []byte(actualHash)) != 1 {
		return credentialScope{}, &authError{status: http.StatusBadRequest, code: "XAmzContentSHA256Mismatch", message: "the payload hash does not match the body"}
	}

	canonicalHeaders := canonicalHeadersBlock(r, signedNames)
	canonicalRequest := strings.Join([]string{
		r.Method,
		awsURIEncode(r.URL.Path, false),
		awsCanonicalQuery(r.URL.Query()),
		canonicalHeaders,
		strings.Join(signedNames, ";"),
		payloadHash,
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope.date + "/" + scope.region + "/" + scope.service + "/" + scope.terminator,
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")
	key := signingKey(expectedSecret, scope.date, scope.region, scope.service)
	expected := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return credentialScope{}, &authError{status: http.StatusForbidden, code: "SignatureDoesNotMatch", message: "the signature does not match"}
	}
	return scope, nil
}

// recordAuthFailure counts and stores the category of a rejected signature.
func (s *Server) recordAuthFailure(err *authError, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch err.code {
	case "SignatureDoesNotMatch", "InvalidAccessKeyId":
		s.counters.SignatureFailures++
	case "InvalidToken":
		// Already counted by authenticate.
	default:
		s.counters.Unauthorized++
	}
}

func (s *Server) read(w *statusRecorder, r *http.Request, key string, head bool) {
	s.mu.Lock()
	if head {
		s.counters.Heads++
	} else {
		s.counters.Gets++
	}
	object, ok := s.objects[key]
	if !ok {
		s.counters.NotFound++
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "NoSuchKey", "the object does not exist")
		return
	}
	content := append([]byte(nil), object.content...)
	etag := object.etag
	modified := object.modified
	contentType := object.contentType
	if !head {
		s.counters.BytesRead += len(content)
	}
	s.mu.Unlock()

	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.WriteHeader(http.StatusOK)
	if !head {
		_, _ = w.Write(content)
	}
}

func (s *Server) put(w *statusRecorder, r *http.Request, key string, body []byte, conflictWrites bool) {
	s.mu.Lock()
	s.counters.Puts++
	object, exists := s.objects[key]
	existsETag := ""
	if exists {
		existsETag = object.etag
	}
	s.mu.Unlock()

	ifMatch := r.Header.Get("If-Match")
	ifNoneMatch := r.Header.Get("If-None-Match")
	if conflictWrites && (ifMatch != "" || ifNoneMatch != "") {
		s.mu.Lock()
		s.counters.PreconditionFailures++
		s.mu.Unlock()
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "the fake conflicts on purpose")
		return
	}
	if ifNoneMatch == "*" && exists {
		s.mu.Lock()
		s.counters.PreconditionFailures++
		s.mu.Unlock()
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "the object already exists")
		return
	}
	if ifMatch != "" {
		if !exists || !etagMatches(ifMatch, existsETag) {
			s.mu.Lock()
			s.counters.PreconditionFailures++
			s.mu.Unlock()
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "the object changed")
			return
		}
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	etag := etagOf(body)
	s.mu.Lock()
	s.objects[key] = &storedObject{
		content:     append([]byte(nil), body...),
		etag:        etag,
		modified:    s.now(),
		contentType: contentType,
	}
	s.counters.BytesWritten += len(body)
	s.mu.Unlock()

	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func etagOf(content []byte) string {
	sum := md5.Sum(content)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func (s *Server) controlEndpoint(w http.ResponseWriter, r *http.Request) {
	var request ControlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid control body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if request.AccessKeyID != nil {
		s.control.AccessKeyID = *request.AccessKeyID
	}
	if request.SecretAccessKey != nil {
		s.control.SecretAccessKey = *request.SecretAccessKey
	}
	if request.SessionToken != nil {
		s.control.SessionToken = *request.SessionToken
	}
	if request.Bucket != nil {
		s.control.Bucket = *request.Bucket
	}
	if request.Region != nil {
		s.control.Region = *request.Region
	}
	if request.BasePath != nil {
		s.control.BasePath = *request.BasePath
	}
	if request.Forbidden != nil {
		s.control.Forbidden = *request.Forbidden
	}
	if request.ServerError != nil {
		s.control.ServerError = *request.ServerError
	}
	if request.ReadOnly != nil {
		s.control.ReadOnly = *request.ReadOnly
	}
	if request.ConflictWrites != nil {
		s.control.ConflictWrites = *request.ConflictWrites
	}
	if request.DelayMS != nil {
		s.control.DelayMS = *request.DelayMS
	}
	s.applyDefaultsLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.State())
}

func (s *Server) record(r *http.Request, w *statusRecorder) {
	scheme, redacted := redactAuthorization(r.Header.Get("Authorization"))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters.Requests++
	if s.secretInRequestLocked(r) {
		s.counters.SecretViolations++
	}
	bucket, key := "", ""
	pathStyle := false
	path := strings.TrimPrefix(r.URL.Path, "/")
	if base := strings.TrimPrefix(s.control.BasePath, "/"); base != "" {
		path = strings.TrimPrefix(path, base)
		path = strings.TrimPrefix(path, "/")
	}
	if first, rest, ok := strings.Cut(path, "/"); ok && first == s.control.Bucket {
		bucket = first
		key = rest
		pathStyle = true
	}
	trace := Trace{
		Method:              r.Method,
		Path:                r.URL.EscapedPath(),
		Bucket:              bucket,
		Key:                 key,
		PathStyle:           pathStyle,
		Status:              w.status,
		AuthScheme:          scheme,
		Authorization:       redacted,
		SignedHeaders:       signedHeadersFrom(r.Header.Get("Authorization")),
		Region:              regionFrom(r.Header.Get("Authorization")),
		SessionTokenPresent: r.Header.Get("x-amz-security-token") != "",
		IfMatch:             r.Header.Get("If-Match"),
		IfNoneMatch:         r.Header.Get("If-None-Match"),
		ETag:                w.Header().Get("ETag"),
		ContentSHA256:       r.Header.Get("x-amz-content-sha256"),
		Bytes:               int(w.written),
	}
	s.traces = append(s.traces, trace)
	if len(s.traces) > maxTraces {
		s.traces = s.traces[len(s.traces)-maxTraces:]
	}
}

func (s *Server) secretInRequestLocked(r *http.Request) bool {
	secrets := []string{s.control.SecretAccessKey, s.control.SessionToken}
	// The Authorization header and the session-token header are the documented
	// carriers of this material; every other location must not contain it.
	haystacks := []string{r.URL.Path, r.URL.RawQuery}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-amz-security-token" {
			continue
		}
		haystacks = append(haystacks, values...)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, haystack := range haystacks {
			if strings.Contains(haystack, secret) {
				return true
			}
		}
	}
	return false
}

func redactAuthorization(header string) (scheme, redacted string) {
	if header == "" {
		return "", ""
	}
	scheme, _, _ = strings.Cut(header, " ")
	if scheme == "" {
		scheme = "unknown"
	}
	return scheme, scheme + " <redacted>"
}

func signedHeadersFrom(header string) string {
	fields := strings.Split(header, ",")
	for _, field := range fields {
		name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if ok && name == "SignedHeaders" {
			return value
		}
	}
	return ""
}

func regionFrom(header string) string {
	fields := strings.Split(header, ",")
	for _, field := range fields {
		name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || name != "Credential" {
			continue
		}
		parts := strings.Split(value, "/")
		if len(parts) == 5 {
			return parts[2]
		}
	}
	return ""
}

// canonicalHeadersBlock renders the signed headers in the order the client
// listed them. SigV4 requires them sorted; the client's list is used verbatim
// so a mis-ordered list would fail verification rather than be normalized.
func canonicalHeadersBlock(r *http.Request, names []string) string {
	var builder strings.Builder
	for _, name := range names {
		name = strings.ToLower(name)
		var value string
		if name == "host" {
			value = r.Host
		} else {
			value = strings.Join(r.Header.Values(name), ",")
		}
		builder.WriteString(name)
		builder.WriteString(":")
		builder.WriteString(collapseSpaces(value))
		builder.WriteString("\n")
	}
	return builder.String()
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// awsCanonicalQuery encodes and sorts query parameters independently of the
// adapter's implementation.
func awsCanonicalQuery(values url.Values) string {
	type pair struct{ name, value string }
	pairs := make([]pair, 0, len(values))
	for name, entries := range values {
		for _, entry := range entries {
			// url.Values has already decoded '+'; re-encode a space as %20.
			pairs = append(pairs, pair{name: awsURIEncode(name, true), value: awsURIEncode(entry, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	parts := make([]string, len(pairs))
	for index, entry := range pairs {
		parts[index] = entry.name + "=" + entry.value
	}
	return strings.Join(parts, "&")
}

func awsURIEncode(value string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		b := value[index]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			builder.WriteByte(b)
		case b == '/' && !encodeSlash:
			builder.WriteByte(b)
		default:
			builder.WriteByte('%')
			builder.WriteByte(hexDigits[b>>4])
			builder.WriteByte(hexDigits[b&0x0f])
		}
	}
	return builder.String()
}

func signingKey(secret, dateStamp, region, serviceName string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	key = hmacSHA256(key, []byte(region))
	key = hmacSHA256(key, []byte(serviceName))
	return hmacSHA256(key, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeError writes an S3-style XML error. HEAD responses carry no body, which
// net/http drops automatically.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>%s</Code><Message>%s</Message></Error>",
		code, message)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	written, err := r.ResponseWriter.Write(data)
	r.written += int64(written)
	return written, err
}

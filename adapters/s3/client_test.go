package s3

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "bare host gets HTTPS", raw: "s3.example.com", want: "https://s3.example.com"},
		{name: "scheme and host lowercased", raw: "HTTP://S3.Example.COM:9000", want: "http://s3.example.com:9000"},
		{name: "base path kept and trimmed", raw: "https://example.com/edge/s3/", want: "https://example.com/edge/s3"},
		{name: "root path removed", raw: "https://example.com/", want: "https://example.com"},
		{name: "credentials refused", raw: "https://key:secret@example.com", wantErr: true},
		{name: "query refused", raw: "https://example.com?bucket=x", wantErr: true},
		{name: "fragment refused", raw: "https://example.com#x", wantErr: true},
		{name: "non-http scheme refused", raw: "ftp://example.com", wantErr: true},
		{name: "empty refused", raw: "   ", wantErr: true},
		{name: "no host refused", raw: "https:///path", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(test.raw)
			if test.wantErr {
				if !errors.Is(err, appsync.ErrInvalidEndpoint) {
					t.Fatalf("error = %v, want ErrInvalidEndpoint", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEndpoint: %v", err)
			}
			if got != test.want {
				t.Fatalf("endpoint = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "", want: ""},
		{raw: "   ", want: ""},
		{raw: "/team/vault/", want: "team/vault"},
		{raw: "team//vault", want: "team/vault"},
		{raw: "./team/./vault", want: "team/vault"},
		{raw: "team/vault-1", want: "team/vault-1"},
		{raw: "a/../b", wantErr: true},
		{raw: "a/\x00b", wantErr: true},
	}
	for _, test := range tests {
		got, err := NormalizePrefix(test.raw)
		if test.wantErr {
			if !errors.Is(err, appsync.ErrInvalidEndpoint) {
				t.Errorf("NormalizePrefix(%q) error = %v, want ErrInvalidEndpoint", test.raw, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePrefix(%q): %v", test.raw, err)
			continue
		}
		if got != test.want {
			t.Errorf("NormalizePrefix(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestObjectKeyUsesTheNormalizedPrefix(t *testing.T) {
	if got := ObjectKey(""); got != domainsync.SyncFileName {
		t.Fatalf("empty prefix key = %q", got)
	}
	if got := ObjectKey("team/vault"); got != "team/vault/"+domainsync.SyncFileName {
		t.Fatalf("prefixed key = %q", got)
	}
}

func TestPrepareNormalizesAndKeepsSecrets(t *testing.T) {
	client := New(Options{})
	prepared, insecure, err := client.Prepare(appsync.S3Settings{
		Endpoint:        " HTTP://Example.COM:9000/edge/ ",
		Region:          " us-east-1 ",
		Bucket:          " natcatty ",
		Prefix:          " /team//vault/ ",
		AccessKeyID:     " access-key ",
		SecretAccessKey: "secret-value",
		SessionToken:    " session-token ",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !insecure {
		t.Fatal("a plain-HTTP endpoint must be reported as insecure")
	}
	if prepared.Endpoint != "http://example.com:9000/edge" {
		t.Fatalf("endpoint = %q", prepared.Endpoint)
	}
	if prepared.Region != "us-east-1" || prepared.Bucket != "natcatty" || prepared.Prefix != "team/vault" {
		t.Fatalf("normalized settings = %+v", prepared)
	}
	if prepared.AccessKeyID != "access-key" || prepared.SecretAccessKey != "secret-value" || prepared.SessionToken != "session-token" {
		t.Fatalf("signing material was not preserved: %+v", prepared)
	}

	https, insecure, err := client.Prepare(appsync.S3Settings{
		Endpoint: "https://example.com", Region: "us-east-1", Bucket: "b",
		AccessKeyID: "a", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatalf("Prepare HTTPS: %v", err)
	}
	if insecure {
		t.Fatal("an HTTPS endpoint must not be reported as insecure")
	}
	_ = https
}

func TestPrepareRequiresEachField(t *testing.T) {
	valid := appsync.S3Settings{
		Endpoint: "https://example.com", Region: "us-east-1", Bucket: "bucket",
		AccessKeyID: "access", SecretAccessKey: "secret",
	}
	client := New(Options{})
	if _, _, err := client.Prepare(valid); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	broken := []appsync.S3Settings{}
	one := valid
	one.Endpoint = ""
	broken = append(broken, one)
	one = valid
	one.Region = ""
	broken = append(broken, one)
	one = valid
	one.Region = "us east 1"
	broken = append(broken, one)
	one = valid
	one.Bucket = ""
	broken = append(broken, one)
	one = valid
	one.Bucket = "a/b"
	broken = append(broken, one)
	one = valid
	one.AccessKeyID = ""
	broken = append(broken, one)
	one = valid
	one.SecretAccessKey = ""
	broken = append(broken, one)
	for index, settings := range broken {
		if _, _, err := client.Prepare(settings); !errors.Is(err, appsync.ErrInvalidEndpoint) {
			t.Errorf("broken settings %d: error = %v, want ErrInvalidEndpoint", index, err)
		}
	}
}

func TestObjectAddressHonorsSchemeHostPortBasePathAndEncodesTheKey(t *testing.T) {
	credential := &domainsync.S3Credential{
		Endpoint: "http://127.0.0.1:9000/edge/s3/",
		Bucket:   "natcatty",
		Prefix:   "team/$ vault",
	}
	address, key, err := objectAddress(credential)
	if err != nil {
		t.Fatalf("objectAddress: %v", err)
	}
	wantKey := "team/$ vault/" + domainsync.SyncFileName
	if key != wantKey {
		t.Fatalf("key = %q, want %q", key, wantKey)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse address: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Host != "127.0.0.1:9000" {
		t.Fatalf("scheme/host = %s/%s", parsed.Scheme, parsed.Host)
	}
	wantPath := "/edge/s3/natcatty/team/$ vault/" + domainsync.SyncFileName
	if parsed.Path != wantPath {
		t.Fatalf("decoded path = %q, want %q", parsed.Path, wantPath)
	}
	if !strings.Contains(parsed.EscapedPath(), "/edge/s3/natcatty/team/%24%20vault/") {
		t.Fatalf("escaped path = %q", parsed.EscapedPath())
	}
}

func TestGrantIdentityIncludesBucketAndPrefix(t *testing.T) {
	first, err := grantFor(&domainsync.S3Credential{
		Endpoint: "https://s3.example.com", Bucket: "bucket", Prefix: "one",
	})
	if err != nil {
		t.Fatalf("grantFor: %v", err)
	}
	second, err := grantFor(&domainsync.S3Credential{
		Endpoint: "https://s3.example.com", Bucket: "bucket", Prefix: "two",
	})
	if err != nil {
		t.Fatalf("grantFor: %v", err)
	}
	if first.Identity() == second.Identity() {
		t.Fatal("two prefixes in one bucket share a merge anchor")
	}
	if !strings.Contains(first.Login, "bucket/one@") {
		t.Fatalf("login = %q", first.Login)
	}
	if strings.Contains(first.ID, "access") {
		t.Fatalf("identity leaked a credential: %q", first.ID)
	}
}

func TestMapStatusUsesActionableSentinels(t *testing.T) {
	client := New(Options{})
	errorBody := func(code string) []byte {
		return []byte(`<?xml version="1.0"?><Error><Code>` + code + `</Code><Message>x</Message></Error>`)
	}
	tests := []struct {
		name   string
		status int
		code   string
		region string
		want   error
	}{
		{name: "401", status: http.StatusUnauthorized, code: "InvalidAccessKeyId", want: appsync.ErrCredentialsRejected},
		{name: "403 access denied", status: http.StatusForbidden, code: "AccessDenied", want: appsync.ErrForbidden},
		{name: "403 signature", status: http.StatusForbidden, code: "SignatureDoesNotMatch", want: appsync.ErrCredentialsRejected},
		{name: "403 token", status: http.StatusForbidden, code: "InvalidToken", want: appsync.ErrCredentialsRejected},
		{name: "403 unknown access key", status: http.StatusForbidden, code: "InvalidAccessKeyId", want: appsync.ErrCredentialsRejected},
		{name: "404 missing key", status: http.StatusNotFound, code: "NoSuchKey", want: errNotFound},
		{name: "404 missing bucket", status: http.StatusNotFound, code: "NoSuchBucket", want: appsync.ErrInvalidEndpoint},
		{name: "412", status: http.StatusPreconditionFailed, code: "PreconditionFailed", want: appsync.ErrConflict},
		{name: "429", status: http.StatusTooManyRequests, code: "", want: appsync.ErrRateLimited},
		{name: "503 slow down", status: http.StatusServiceUnavailable, code: "SlowDown", want: appsync.ErrRateLimited},
		{name: "500", status: http.StatusInternalServerError, code: "InternalError", want: appsync.ErrNetwork},
		{name: "301 wrong region", status: http.StatusMovedPermanently, code: "PermanentRedirect", region: "eu-west-1", want: appsync.ErrWrongRegion},
		{name: "400 malformed auth region", status: http.StatusBadRequest, code: "AuthorizationHeaderMalformed", want: appsync.ErrWrongRegion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.region != "" {
				header.Set("x-amz-bucket-region", test.region)
			}
			_, err := client.mapStatus(&response{status: test.status, header: header, body: errorBody(test.code)})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if err := s3ErrorCode(errorBody("NoSuchKey")); err != "NoSuchKey" {
		t.Fatalf("s3ErrorCode = %q", err)
	}
	if got := s3ErrorCode([]byte("not xml")); got != "" {
		t.Fatalf("s3ErrorCode on invalid XML = %q", got)
	}
}

func TestCredentialForRequiresAnS3Credential(t *testing.T) {
	if _, err := credentialFor(domainsync.Token{}); !errors.Is(err, appsync.ErrNotConfigured) {
		t.Fatalf("empty token error = %v", err)
	}
	token := domainsync.Token{S3: &domainsync.S3Credential{Endpoint: "https://example.com"}}
	if _, err := credentialFor(token); err != nil {
		t.Fatalf("credentialFor: %v", err)
	}
}

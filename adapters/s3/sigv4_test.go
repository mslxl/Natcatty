package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The four vectors below are Amazon's published S3 Signature Version 4
// examples. The access key id and secret are the documented example values,
// not real credentials. They pin canonical URI encoding (including $), query
// sorting, canonical header selection, the scope, and the HMAC key ladder to
// an implementation this repository did not write.
func TestSignMatchesThePublishedS3Vectors(t *testing.T) {
	credentials := Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	emptyHash := PayloadSHA256(nil)

	tests := []struct {
		name            string
		method          string
		host            string
		uri             string
		query           string
		headers         map[string]string
		payloadHash     string
		wantSigned      string
		wantSignature   string
		wantCanonicalUR string
	}{
		{
			name:   "GET object",
			method: http.MethodGet,
			host:   "examplebucket.s3.amazonaws.com",
			uri:    "/test.txt",
			headers: map[string]string{
				"Range": "bytes=0-9",
			},
			payloadHash:   emptyHash,
			wantSigned:    "host;range;x-amz-content-sha256;x-amz-date",
			wantSignature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:   "PUT object",
			method: http.MethodPut,
			host:   "examplebucket.s3.amazonaws.com",
			uri:    "/test$file.text",
			headers: map[string]string{
				"Date":                "Fri, 24 May 2013 00:00:00 GMT",
				"x-amz-storage-class": "REDUCED_REDUNDANCY",
			},
			payloadHash:     "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			wantSigned:      "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			wantSignature:   "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
			wantCanonicalUR: "/test%24file.text",
		},
		{
			name:          "GET bucket lifecycle",
			method:        http.MethodGet,
			host:          "examplebucket.s3.amazonaws.com",
			uri:           "/",
			query:         "lifecycle",
			payloadHash:   emptyHash,
			wantSigned:    "host;x-amz-content-sha256;x-amz-date",
			wantSignature: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:          "GET bucket list with a query",
			method:        http.MethodGet,
			host:          "examplebucket.s3.amazonaws.com",
			uri:           "/",
			query:         "max-keys=2&prefix=J",
			payloadHash:   emptyHash,
			wantSigned:    "host;x-amz-content-sha256;x-amz-date",
			wantSignature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := SignInput{
				Method:      test.method,
				Host:        test.host,
				URI:         test.uri,
				Query:       test.query,
				Headers:     http.Header{},
				PayloadHash: test.payloadHash,
				Region:      "us-east-1",
				Service:     "s3",
				Time:        at,
			}
			for name, value := range test.headers {
				input.Headers.Set(name, value)
			}
			signed, err := Sign(input, credentials)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if signed.Signature != test.wantSignature {
				t.Fatalf("signature = %s, want %s\ncanonical request:\n%s\nto sign:\n%s",
					signed.Signature, test.wantSignature, signed.CanonicalRequest, signed.StringToSign)
			}
			if signed.SignedHeaders != test.wantSigned {
				t.Fatalf("signed headers = %q, want %q", signed.SignedHeaders, test.wantSigned)
			}
			if !strings.HasPrefix(signed.Authorization, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,") {
				t.Fatalf("authorization = %q", signed.Authorization)
			}
			if !strings.Contains(signed.Authorization, "Signature="+test.wantSignature) {
				t.Fatalf("authorization does not carry the signature: %q", signed.Authorization)
			}
			if test.wantCanonicalUR != "" && !strings.Contains(signed.CanonicalRequest, "\n"+test.wantCanonicalUR+"\n") {
				t.Fatalf("canonical request does not carry %q:\n%s", test.wantCanonicalUR, signed.CanonicalRequest)
			}
			if !strings.Contains(signed.CanonicalRequest, test.payloadHash) {
				t.Fatalf("canonical request does not carry the payload hash:\n%s", signed.CanonicalRequest)
			}
		})
	}
}

func TestSignIncludesTheSessionTokenWhenPresent(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	base := SignInput{
		Method:      http.MethodGet,
		Host:        "127.0.0.1:9000",
		URI:         "/bucket/netcatty-vault.json",
		PayloadHash: PayloadSHA256(nil),
		Region:      "us-east-1",
		Service:     "s3",
		Time:        at,
	}
	withoutToken, err := Sign(base, Credentials{AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatalf("Sign without token: %v", err)
	}
	if strings.Contains(withoutToken.SignedHeaders, "x-amz-security-token") {
		t.Fatalf("signed headers include a token that was not set: %q", withoutToken.SignedHeaders)
	}
	withToken, err := Sign(base, Credentials{
		AccessKeyID: "key", SecretAccessKey: "secret", SessionToken: "token-value",
	})
	if err != nil {
		t.Fatalf("Sign with token: %v", err)
	}
	if !strings.Contains(withToken.SignedHeaders, "x-amz-security-token") {
		t.Fatalf("signed headers omit the session token: %q", withToken.SignedHeaders)
	}
	if !strings.Contains(withToken.CanonicalRequest, "x-amz-security-token:token-value\n") {
		t.Fatalf("canonical request omits the session token value:\n%s", withToken.CanonicalRequest)
	}
	if withToken.Signature == withoutToken.Signature {
		t.Fatal("the session token did not change the signature")
	}
}

func TestSignRequiresItsInputs(t *testing.T) {
	valid := SignInput{
		Method: http.MethodGet, Host: "h", URI: "/", PayloadHash: PayloadSHA256(nil),
		Region: "us-east-1", Service: "s3", Time: time.Unix(0, 0),
	}
	validCredentials := Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}
	if _, err := Sign(valid, Credentials{}); err == nil {
		t.Fatal("Sign accepted empty credentials")
	}
	broken := valid
	broken.Host = ""
	if _, err := Sign(broken, validCredentials); err == nil {
		t.Fatal("Sign accepted a request without a host")
	}
	broken = valid
	broken.Region = ""
	if _, err := Sign(broken, validCredentials); err == nil {
		t.Fatal("Sign accepted an empty region")
	}
	broken = valid
	broken.PayloadHash = ""
	if _, err := Sign(broken, validCredentials); err == nil {
		t.Fatal("Sign accepted an empty payload hash")
	}
}

func TestURIEncodingLeavesOnlyUnreservedBytesAndSlashes(t *testing.T) {
	tests := map[string]string{
		"test$file.text": "test%24file.text",
		"a b":            "a%20b",
		"a+b":            "a%2Bb",
		"a%b":            "a%25b",
		"~_-.":           "~_-.",
		"a/b":            "a/b",
		"café":           "caf%C3%A9",
		"a/../b":         "a/../b",
		"a//b":           "a//b",
		"a&b=c":          "a%26b%3Dc",
	}
	for raw, want := range tests {
		if got := uriEncode(raw, false); got != want {
			t.Errorf("uriEncode(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("uriEncode with slashes encoded = %q", got)
	}
}

func TestCanonicalQueryIsEncodedAndSorted(t *testing.T) {
	got := canonicalQuery("prefix=J&max-keys=2&empty=&space=a+b&dup=2&dup=1")
	want := "dup=1&dup=2&empty=&max-keys=2&prefix=J&space=a%20b"
	if got != want {
		t.Fatalf("canonical query = %q, want %q", got, want)
	}
	if got := canonicalQuery(""); got != "" {
		t.Fatalf("empty query = %q", got)
	}
}

func TestPayloadSHA256MatchesTheDocumentedEmptyHash(t *testing.T) {
	if got := PayloadSHA256(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty payload hash = %s", got)
	}
	if got := PayloadSHA256([]byte("hello")); got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("hello payload hash = %s", got)
	}
}

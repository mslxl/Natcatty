package remote

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func capabilityDocumentWith(changes func(map[string]any)) map[string]any {
	document := validCapabilityDocument()
	if changes != nil {
		changes(document)
	}
	return document
}

func TestCapabilitiesParsesPinnedDocument(t *testing.T) {
	document := capabilityDocumentWith(func(document map[string]any) {
		capabilities := document["capabilities"].(map[string]any)
		capabilities["sh.atuin.server/records.packfile"] = map[string]any{
			"version": 1, "record_count": 500,
		}
	})
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, document)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})

	capabilities, err := remote.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if capabilities.ServerVersion != historysync.PinnedAtuinVersion ||
		capabilities.Major != 18 || capabilities.Minor != 24 || capabilities.Patch != 5 {
		t.Fatalf("server version = %+v", capabilities)
	}
	if capabilities.PageSize != 100 {
		t.Fatalf("page size = %d, want 100", capabilities.PageSize)
	}
	if capabilities.Values[historysync.CapabilityServer] != 1 ||
		capabilities.Values[historysync.CapabilityPageSize] != 1 {
		t.Fatalf("capability values = %v", capabilities.Values)
	}
	if !capabilities.Supports("sh.atuin.server/records.packfile") {
		t.Fatal("advertised packfile capability was not recorded")
	}
	if err := capabilities.Check(); err != nil {
		t.Fatalf("advertised but unenforced packfile: %v", err)
	}

	requests := recorder.all()
	if len(requests) != 1 || requests[0].Path != "/api/v0/capabilities" {
		t.Fatalf("requests = %+v", requests)
	}
	if got := requests[0].Header.Get("Authorization"); got != "Token session-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestCapabilitiesFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		document any
	}{
		{
			name:     "server major is newer",
			version:  "19.0.0",
			document: validCapabilityDocument(),
		},
		{
			name:     "server major is older",
			version:  "17.9.9",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version is not semver",
			version:  "eighteen",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version is missing",
			version:  "",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version has no minor or patch",
			version:  "18",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version has no patch",
			version:  "18.24",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version has four components",
			version:  "18.24.5.1",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version has a non-numeric component",
			version:  "x.24.5",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version has a negative component",
			version:  "18.-1.5",
			document: validCapabilityDocument(),
		},
		{
			name:     "server version starts with a plus",
			version:  "+18.24.5",
			document: validCapabilityDocument(),
		},
		{
			name:    "capabilities capability is missing",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				delete(document["capabilities"].(map[string]any), historysync.CapabilityServer)
			}),
		},
		{
			name:    "page size capability is missing",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				delete(document["capabilities"].(map[string]any), historysync.CapabilityPageSize)
			}),
		},
		{
			name:    "page size is zero",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityPageSize] =
					map[string]any{"version": 1, "page_size": 0}
			}),
		},
		{
			name:    "page size is absent",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityPageSize] =
					map[string]any{"version": 1}
			}),
		},
		{
			name:    "capabilities protocol version is unknown",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityServer] =
					map[string]any{"version": 2}
			}),
		},
		{
			name:    "page size protocol version is unknown",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityPageSize] =
					map[string]any{"version": 2, "page_size": 100}
			}),
		},
		{
			name:    "required capability value is malformed",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityServer] = true
			}),
		},
		{
			name:    "server enforces an unknown capability",
			version: historysync.PinnedAtuinVersion,
			document: capabilityDocumentWith(func(document map[string]any) {
				document["enforced"] = []string{"sh.atuin.server/records.packfile"}
			}),
		},
		{
			name:     "capabilities field is missing",
			version:  historysync.PinnedAtuinVersion,
			document: map[string]any{"version": "token"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t, test.version, map[string]http.HandlerFunc{
				"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
					writeJSON(t, writer, http.StatusOK, test.document)
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
			})
			_, err := remote.Capabilities(context.Background())
			if !errors.Is(err, historysync.ErrIncompatibleServer) {
				t.Fatalf("Capabilities = %v, want ErrIncompatibleServer", err)
			}
		})
	}
}

func TestCapabilitiesRejectsMalformedJSONAsRemoteFailure(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"capabilities": {`))
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Capabilities = %v, want ErrRemote", err)
	}
}

func TestCapabilitiesAcceptsPrereleaseAndBuildVersions(t *testing.T) {
	for _, version := range []string{"18.24.5-rc.1", "18.0.0+build.7", " 18.24.5 "} {
		t.Run(version, func(t *testing.T) {
			server, _ := newTestServer(t, version, map[string]http.HandlerFunc{
				"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
					writeJSON(t, writer, http.StatusOK, validCapabilityDocument())
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
			})
			capabilities, err := remote.Capabilities(context.Background())
			if err != nil {
				t.Fatalf("Capabilities: %v", err)
			}
			if capabilities.ServerVersion != strings.TrimSpace(version) || capabilities.Major != 18 {
				t.Fatalf("capabilities = %+v, want the pinned major from %q", capabilities, version)
			}
		})
	}
}

func TestRecordStatusFiltersOtherTagsAndSorts(t *testing.T) {
	body := `{"hosts":{
		"0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee1":{"history":4,"kv":2,"packfile":0},
		"0190AAAA-BBBB-7CCC-8DDD-EEEEEEEEEEE0":{"history":0}
	},"unknown":"field"}`
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(body))
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	statuses, err := remote.RecordStatus(context.Background())
	if err != nil {
		t.Fatalf("RecordStatus: %v", err)
	}
	want := []historysync.SeriesStatus{
		{Series: historysync.Series{SeriesHost: testWriterID, Tag: "history"}, MaxIndex: 0},
		{Series: historysync.Series{SeriesHost: testOtherWriter, Tag: "history"}, MaxIndex: 4},
	}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %+v, want %+v", statuses, want)
	}
	if len(recorder.all()) != 1 {
		t.Fatalf("requests = %d", len(recorder.all()))
	}
}

func TestRecordStatusRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"hosts is missing", `{}`},
		{"hosts is null", `{"hosts":null}`},
		{"host is not a UUID", `{"hosts":{"not-a-uuid":{"history":1}}}`},
		{"index is negative", `{"hosts":{"` + testWriterID + `":{"history":-1}}}`},
		{"index is null", `{"hosts":{"` + testWriterID + `":{"history":null}}}`},
		{"index is a string", `{"hosts":{"` + testWriterID + `":{"history":"one"}}}`},
		{"hosts is not an object", `{"hosts":[]}`},
		{"trailing data", `{"hosts":{}} {"hosts":{}}`},
		{"not JSON", `nope`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
					_, _ = writer.Write([]byte(test.body))
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
			})
			_, err := remote.RecordStatus(context.Background())
			if !errors.Is(err, historysync.ErrRemote) {
				t.Fatalf("RecordStatus = %v, want ErrRemote", err)
			}
		})
	}
}

func TestRecordStatusIgnoresUnknownTagValues(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"hosts":{"` + testWriterID + `":{"history":3,"kv":null,"packfile":"later"}}}`))
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	statuses, err := remote.RecordStatus(context.Background())
	if err != nil {
		t.Fatalf("RecordStatus: %v", err)
	}
	want := []historysync.SeriesStatus{
		{Series: historysync.Series{SeriesHost: testWriterID, Tag: "history"}, MaxIndex: 3},
	}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %+v, want %+v", statuses, want)
	}
}

func TestRecordStatusAcceptsAnEmptyAccount(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"hosts":{}}`))
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	statuses, err := remote.RecordStatus(context.Background())
	if err != nil {
		t.Fatalf("RecordStatus: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want none", statuses)
	}
}

func TestCapabilitiesPreservesBasePath(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/atuin/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, validCapabilityDocument())
		},
	})
	remote := openTestRemote(t, server.URL+"/atuin", historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	if _, err := remote.Capabilities(context.Background()); err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	requests := recorder.all()
	if len(requests) != 1 || requests[0].Path != "/atuin/api/v0/capabilities" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestOpenRejectsUnsafeAddresses(t *testing.T) {
	base := historysync.Config{AuthMode: atuinmodel.AuthModeSession, Token: "session-token"}
	tests := []string{
		"",
		"ftp://example.com",
		"https://user" + ":" + "secret" + "@example.com",
		"https://example.com/?token=x",
		"https://example.com/#fragment",
		"http://example.com",
	}
	for _, address := range tests {
		t.Run(strings.ReplaceAll(address, "/", "_"), func(t *testing.T) {
			config := base
			config.BaseURL = address
			if _, err := NewFactory(Options{}).Open(context.Background(), config); err == nil {
				t.Fatalf("Open(%q) succeeded, want an error", address)
			}
		})
	}
}

func TestCapabilitiesCopiesValues(t *testing.T) {
	document := maps.Clone(validCapabilityDocument())
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, document)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	first, err := remote.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	first.Values[historysync.CapabilityServer] = 99
	second, err := remote.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if second.Values[historysync.CapabilityServer] != 1 {
		t.Fatalf("capability values leaked between calls: %v", second.Values)
	}
}

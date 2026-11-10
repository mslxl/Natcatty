// Package stub exposes the one service-test stub definition shared by the CLI
// fake host and the reference Rust SDK's in-language mock. The definition
// lives in sdk/idl/stub.json and is validated against the IDL, so a stub can
// never name a service or method the wire ABI does not declare.
package stub

import (
	"encoding/json"

	"github.com/mslxl/natcatty/sdk/idl"
)

// Definition returns the parsed shared stub definition.
func Definition() (idl.Stub, error) { return idl.StubDefinition() }

// Source returns the raw JSON shared stub definition.
func Source() []byte { return idl.StubSource() }

// Response returns the stub payload for one service method by name.
func Response(serviceName, methodName string) ([]byte, error) {
	definition, err := Definition()
	if err != nil {
		return nil, err
	}
	return definition.Response(serviceName, methodName)
}

// ResponseByID returns the stub payload for one service method by wire id.
func ResponseByID(serviceName string, methodID uint32) ([]byte, error) {
	definition, err := Definition()
	if err != nil {
		return nil, err
	}
	return definition.ResponseByID(serviceName, methodID)
}

// NotFoundResponse renders the transport-OK envelope the fake host returns
// when the shared stub has no response for a service call. The generated Rust
// absent_response emits the same bytes for the in-language mock, so one shared
// stub definition cannot produce divergent behavior.
func NotFoundResponse(err error) []byte {
	payload, _ := json.Marshal(map[string]string{
		"code":    "not-found",
		"message": err.Error(),
	})
	return payload
}

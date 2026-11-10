package idl_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/sdk/idl"
)

// TestGeneratedRustCoversEveryServiceMethod proves the typed Rust wrappers
// exist for every service and method the IDL declares, and that the wrapper
// uses the same stable numeric id. This is the generation-side half of the
// host/IDL consistency contract.
func TestGeneratedRustCoversEveryServiceMethod(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rust := string(definition.GenerateRust())
	for _, service := range definition.Services {
		marker := "    pub mod " + service.Name + " {\n"
		if !strings.Contains(rust, marker) {
			t.Fatalf("generated Rust is missing service module %q", service.Name)
		}
		for _, method := range service.Methods {
			constant := fmt.Sprintf("METHOD_%s: u32 = %d;",
				strings.ToUpper(method.Name), method.ID)
			if !strings.Contains(rust, constant) {
				t.Errorf("service %s is missing %q", service.Name, constant)
			}
			wrapperName := method.Name
			if wrapperName == "call" {
				wrapperName = "call_method_named"
			}
			wrapper := fmt.Sprintf("pub fn %s<'a>(request: &[u8], response: &'a mut [u8])",
				wrapperName)
			if !strings.Contains(rust, wrapper) {
				t.Errorf("service %s is missing typed wrapper for %s", service.Name, method.Name)
			}
		}
	}
	for _, want := range []string{
		"pub struct ServiceReply<'a>",
		"pub const fn transport(self) -> Option<ServiceStatus>",
		"pub const fn from_code(code: i32) -> Option<Self>",
		"pub enum UIStatus",
		"pub enum UIOpKind",
		"pub trait Host: Send",
		"fn service_call(&mut self, service: &str, method_id: u32,",
		"pub fn service_response_len() -> u32",
		// The wasm extern and the typed wrapper are both called `call` in the
		// same module; the extern gets a distinct Rust-side name so the target
		// compiles, and link_name binds it to the exported wasm field.
		"fn host_call(method_id: u32, req_ptr: u32, req_len: u32, resp_ptr: u32, resp_cap: u32) -> i32;",
		"host_call(method_id, request.as_ptr() as u32, request.len() as u32,",
	} {
		if !strings.Contains(rust, want) {
			t.Errorf("generated Rust is missing %q", want)
		}
	}
}

// TestGeneratedRustBindsServiceExternsToTheCallExport proves every generated
// service module binds its Rust-side host_call alias to the `call` wasm import
// field the host exports and the structural allowlist accepts. Rust otherwise
// uses the extern identifier as the import field, which made every plugin that
// calls a service import `<module>.host_call` and fail install-time validation.
func TestGeneratedRustBindsServiceExternsToTheCallExport(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rust := string(definition.GenerateRust())
	if !strings.Contains(rust, "pub const CALL: &str = \"call\";") {
		t.Fatal("generated Rust service::CALL is not \"call\"")
	}
	for _, service := range definition.Services {
		block := fmt.Sprintf(
			"        #[link(wasm_import_module = %q)]\n"+
				"        extern \"C\" {\n"+
				"            #[link_name = \"call\"]\n"+
				"            fn host_call(method_id: u32, req_ptr: u32, req_len: u32, resp_ptr: u32, resp_cap: u32) -> i32;\n",
			service.Module())
		if !strings.Contains(rust, block) {
			t.Errorf("service %s does not bind host_call to the call wasm import field", service.Name)
		}
	}
	// A bare extern would make Rust import the Rust-side alias instead.
	if strings.Contains(rust, "extern \"C\" {\n            fn host_call(") {
		t.Error("generated Rust has a service extern without #[link_name = \"call\"]")
	}
}

// TestGeneratedGoCoversEveryServiceMethod proves the host-side Go types expose
// every service module and method id, and that the response-length import is
// in the allowlist. The adapter consistency test consumes the same data.
func TestGeneratedGoCoversEveryServiceMethod(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	goSource := string(definition.GenerateGo())
	for _, service := range definition.Services {
		moduleConstant := "Service" + goCamelForTest(service.Name) + "Module"
		if !strings.Contains(goSource, moduleConstant) ||
			!strings.Contains(goSource, fmt.Sprintf("%q", service.Module())) {
			t.Errorf("generated Go is missing module %q", service.Module())
		}
		for _, method := range service.Methods {
			want := fmt.Sprintf("uint32 = %d", method.ID)
			if !strings.Contains(goSource, want) {
				t.Errorf("generated Go is missing id %d for %s.%s", method.ID, service.Name, method.Name)
			}
		}
	}
	if !strings.Contains(goSource, "HostFunctionServiceResponseLen") {
		t.Fatal("generated Go is missing the service response length import")
	}
	if _, ok := definition.HostModule().FunctionByName("service_response_len"); !ok {
		t.Fatal("IDL host module is missing service_response_len")
	}
}

// TestSharedStubCoversEveryService proves the one shared stub definition names
// only live IDL methods and that every service has at least one scripted
// response, so the fake host and the Rust mock can exercise the whole surface.
func TestSharedStubCoversEveryService(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stub, err := idl.StubDefinition()
	if err != nil {
		t.Fatalf("StubDefinition: %v", err)
	}
	covered := map[string]bool{}
	for _, key := range stub.Keys() {
		serviceName, methodName, ok := strings.Cut(key, ".")
		if !ok {
			t.Fatalf("stub key %q is not service.method", key)
		}
		service, found := serviceByNameForTest(definition, serviceName)
		if !found {
			t.Fatalf("stub names unknown service %q", serviceName)
		}
		var methodID uint32
		found = false
		for _, method := range service.Methods {
			if method.Name == methodName {
				methodID = method.ID
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("stub names unknown method %s.%s", serviceName, methodName)
		}
		byID, err := stub.ResponseByID(serviceName, methodID)
		if err != nil {
			t.Fatalf("ResponseByID(%s, %d): %v", serviceName, methodID, err)
		}
		byName, err := stub.Response(serviceName, methodName)
		if err != nil {
			t.Fatalf("Response(%s, %s): %v", serviceName, methodName, err)
		}
		if string(byID) != string(byName) {
			t.Fatalf("stub lookup by id and name differ for %s", key)
		}
		covered[serviceName] = true
	}
	for _, service := range definition.Services {
		if service.Name == "ui" {
			// UI responses are binary and session-state dependent; the real
			// session serves them in the fake host and the Rust mock has a
			// typed UI simulator.
			continue
		}
		if !covered[service.Name] {
			t.Errorf("shared stub has no response for service %q", service.Name)
		}
	}
	rustStub, err := definition.GenerateRustStub(stub)
	if err != nil {
		t.Fatalf("GenerateRustStub: %v", err)
	}
	for _, service := range definition.Services {
		if service.Name == "ui" {
			continue
		}
		if !strings.Contains(string(rustStub), fmt.Sprintf("(%q, ", service.Name)) {
			t.Errorf("generated Rust stub has no entry for service %q", service.Name)
		}
	}
}

func serviceByNameForTest(definition *idl.Definition, name string) (idl.Service, bool) {
	for _, service := range definition.Services {
		if service.Name == name {
			return service, true
		}
	}
	return idl.Service{}, false
}

func goCamelForTest(name string) string {
	var out strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

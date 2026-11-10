package idl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/sdk/idl"
)

func TestParseCurrentIDL(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if definition.Name != "natcatty" || definition.Version != "1.15.0" || definition.ABI != 1 {
		t.Fatalf("identity = %q %q abi %d", definition.Name, definition.Version, definition.ABI)
	}
	host := definition.HostModule()
	if host.Name != "natcatty_host_v1" {
		t.Fatalf("host module = %q", host.Name)
	}
	wantHost := []string{
		"abi_version", "log", "now_ns", "random_get", "output_write",
		"language_respond", "language_cancelled",
		"mcp_tool_result", "mcp_tool_progress",
		"service_respond", "service_response_len",
		"provider_respond", "provider_cancelled",
		"provider_input_read", "provider_input_credit",
		"provider_output_write", "provider_output_credit",
		"data_provider_respond", "data_provider_cancelled",
	}
	if len(host.Functions) != len(wantHost) {
		t.Fatalf("host functions = %d, want %d", len(host.Functions), len(wantHost))
	}
	for i, want := range wantHost {
		if host.Functions[i].Name != want {
			t.Fatalf("host function %d = %q, want %q", i, host.Functions[i].Name, want)
		}
	}
	guest := definition.GuestModule()
	if len(guest.Exports) != 12 {
		t.Fatalf("guest exports = %d, want 12", len(guest.Exports))
	}
	if got := len(guest.RequiredExports()); got != 5 {
		t.Fatalf("required exports = %d, want 5", got)
	}
	if got := guest.Exports[0].Wire; got != "natcatty_abi_version" {
		t.Fatalf("first export = %q", got)
	}
	wantEnums := []string{
		"LogLevel", "LanguageKind", "ServiceStatus", "ProviderMethod",
		"DataProviderMethod",
		"NodeKind", "PropID", "PropValueKind", "UIStatus", "UIOpKind",
		"EventKind", "Key", "Role", "Token", "Layout",
		"CanvasShape", "PlanStatus", "Ease",
	}
	if len(definition.Enums) != len(wantEnums) {
		t.Fatalf("enums = %d, want %d", len(definition.Enums), len(wantEnums))
	}
	for i, want := range wantEnums {
		if definition.Enums[i].Name != want {
			t.Fatalf("enum %d = %q, want %q", i, definition.Enums[i].Name, want)
		}
	}
	if got := len(definition.Enums[1].Values); got != 8 {
		t.Fatalf("language kinds = %d, want 8", got)
	}
	providerMethods := definition.Enums[3]
	if len(providerMethods.Values) != 8 || providerMethods.Values[7].Name != "pump" ||
		providerMethods.Values[7].Value != 8 {
		t.Fatalf("provider methods = %#v", providerMethods.Values)
	}
	dataProviderMethods := definition.Enums[4]
	if len(dataProviderMethods.Values) != 13 ||
		dataProviderMethods.Values[0].Name != "open" ||
		dataProviderMethods.Values[12].Name != "plan" ||
		dataProviderMethods.Values[12].Value != 13 {
		t.Fatalf("data provider methods = %#v", dataProviderMethods.Values)
	}
	nodeKinds := definition.Enums[5]
	if len(nodeKinds.Values) != 35 || nodeKinds.Values[34].Name != "webview" || nodeKinds.Values[34].Value != 34 {
		t.Fatalf("node kinds = %#v", nodeKinds.Values)
	}
	propIDs := definition.Enums[6]
	if len(propIDs.Values) != 67 || propIDs.Values[66].Name != "transition_ease" || propIDs.Values[66].Value != 67 {
		t.Fatalf("prop ids = %#v", propIDs.Values)
	}
	uiStatuses := definition.Enums[8]
	if len(uiStatuses.Values) != 7 || uiStatuses.Values[3].Name != "denied" ||
		uiStatuses.Values[3].Value != 3 || uiStatuses.Values[6].Name != "closed" ||
		uiStatuses.Values[6].Value != 6 {
		t.Fatalf("ui statuses = %#v", uiStatuses.Values)
	}
	uiOps := definition.Enums[9]
	if len(uiOps.Values) != 7 || uiOps.Values[4].Name != "set_prop" ||
		uiOps.Values[4].Value != 4 || uiOps.Values[6].Name != "set_children" ||
		uiOps.Values[6].Value != 6 {
		t.Fatalf("ui op kinds = %#v", uiOps.Values)
	}
	canvasShapes := definition.Enums[15]
	if len(canvasShapes.Values) != 5 || canvasShapes.Values[4].Name != "image" {
		t.Fatalf("canvas shapes = %#v", canvasShapes.Values)
	}
	planStatus := definition.Enums[16]
	if len(planStatus.Values) != 5 || planStatus.Values[4].Name != "skipped" {
		t.Fatalf("plan statuses = %#v", planStatus.Values)
	}
	easing := definition.Enums[17]
	if len(easing.Values) != 4 || easing.Values[3].Name != "ease_in_out" || easing.Values[3].Value != 3 {
		t.Fatalf("ease values = %#v", easing.Values)
	}
	if len(definition.Records) != 1 || definition.Records[0].Name != "GuestRange" {
		t.Fatalf("records = %#v", definition.Records)
	}
	wantHandles := []string{"Handle", "NodeHandle", "ListenerHandle", "ImageHandle", "FontHandle", "WebviewHandle"}
	if len(definition.Handles) != len(wantHandles) {
		t.Fatalf("handles = %#v", definition.Handles)
	}
	for i, want := range wantHandles {
		if definition.Handles[i].Name != want || definition.Handles[i].Base != "u64" {
			t.Fatalf("handle %d = %#v, want %s u64", i, definition.Handles[i], want)
		}
	}
	wantServices := []string{
		"storage", "clipboard", "notifications", "events", "settings",
		"systempanel", "i18n", "ui", "files", "hosts", "sessions", "terminal", "services",
		"network", "auth", "trust", "vault",
	}
	if len(definition.Services) != len(wantServices) {
		t.Fatalf("services = %#v", definition.Services)
	}
	for i, want := range wantServices {
		service := definition.Services[i]
		if service.Name != want || service.Module() != "natcatty:svc/"+want+"_v1" {
			t.Fatalf("service %d = %#v", i, service)
		}
	}
	storage := definition.Services[0]
	if len(storage.Methods) != 4 || storage.Methods[0].Name != "get" || storage.Methods[0].ID != 1 {
		t.Fatalf("storage methods = %#v", storage.Methods)
	}
	ui := definition.Services[7]
	wantUIMethods := []struct {
		name string
		id   uint32
	}{
		{"open_view", 1}, {"close", 2}, {"focus", 3}, {"restore", 4},
		{"dialog", 5}, {"theme", 6}, {"locale", 7},
		{"apply", 8}, {"listen", 9}, {"unlisten", 10}, {"poll", 11},
		{"webview_respond", 12},
	}
	if len(ui.Methods) != len(wantUIMethods) {
		t.Fatalf("ui methods = %#v", ui.Methods)
	}
	for i, want := range wantUIMethods {
		if ui.Methods[i].Name != want.name || ui.Methods[i].ID != want.id {
			t.Fatalf("ui method %d = %#v, want %s=%d", i, ui.Methods[i], want.name, want.id)
		}
	}
	files := definition.Services[8]
	if len(files.Methods) != 20 || files.Methods[19].Name != "remote_mkdir" ||
		files.Methods[19].ID != 20 {
		t.Fatalf("files methods = %#v", files.Methods)
	}
	terminal := definition.Services[11]
	if len(terminal.Methods) != 5 || terminal.Methods[4].Name != "info" ||
		terminal.Methods[4].ID != 5 {
		t.Fatalf("terminal methods = %#v", terminal.Methods)
	}
	wantExternal := []struct {
		index   int
		name    string
		methods []string
	}{
		{13, "network", []string{"dial", "read", "write", "shutdown", "close", "tls_upgrade"}},
		{14, "auth", []string{"begin", "respond", "cancel"}},
		{15, "trust", []string{"verify"}},
		{16, "vault", []string{"metadata", "fields", "lease_issue", "lease_read", "lease_release"}},
	}
	for _, want := range wantExternal {
		service := definition.Services[want.index]
		if service.Name != want.name || len(service.Methods) != len(want.methods) {
			t.Fatalf("service %d = %#v, want %s with %d methods",
				want.index, service, want.name, len(want.methods))
		}
		for i, method := range want.methods {
			if service.Methods[i].Name != method || service.Methods[i].ID != uint32(i+1) {
				t.Fatalf("%s method %d = %#v, want %s=%d",
					want.name, i, service.Methods[i], method, i+1)
			}
		}
	}
}

func TestRejectsInvalidServiceDeclarations(t *testing.T) {
	_, err := idl.ParseString(`idl "x" {
  version "1.0.0"
  abi 1
  service "Storage" {
    method get = 1
  }
  host "h" {
    f() -> i32
  }
  guest "g" {
    export init "natcatty_init" () -> i32
  }
}`)
	if err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("ParseString error = %v, want lowercase service name", err)
	}
}

func TestRejectsDuplicateServiceMethodIDs(t *testing.T) {
	_, err := idl.ParseString(`idl "x" {
  version "1.0.0"
  abi 1
  service "storage" {
    method get = 1
    method set = 1
  }
  host "h" {
    f() -> i32
  }
  guest "g" {
    export init "natcatty_init" () -> i32
  }
}`)
	if err == nil || !strings.Contains(err.Error(), "repeats method id") {
		t.Fatalf("ParseString error = %v, want duplicate method id", err)
	}
}

// TestCommittedBindingsAreCurrent is the CI generation check: the computed
// output must match the checked-in files byte for byte.
func TestCommittedBindingsAreCurrent(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	root := testutil.ModuleRoot(t)
	stub, err := idl.StubDefinition()
	if err != nil {
		t.Fatalf("StubDefinition: %v", err)
	}
	rustStub, err := definition.GenerateRustStub(stub)
	if err != nil {
		t.Fatalf("GenerateRustStub: %v", err)
	}
	for _, output := range []struct {
		path    string
		content []byte
	}{
		{idl.GoPath, definition.GenerateGo()},
		{idl.RustPath, definition.GenerateRust()},
		{idl.RustStubPath, rustStub},
	} {
		committed, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(output.path)))
		if err != nil {
			t.Fatalf("read %s: %v", output.path, err)
		}
		if string(committed) != string(output.content) {
			t.Fatalf("%s is stale; run go run ./sdk/idl/gen", output.path)
		}
	}
}

// TestRustBindingsCarryTypedABIRejection proves the generated guest code can
// fail init with a typed error instead of a status code alone.
func TestRustBindingsCarryTypedABIRejection(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rust := string(definition.GenerateRust())
	for _, want := range []string{
		"pub enum AbiMismatch",
		"pub fn ensure_compatible_abi() -> Result<(), AbiMismatch>",
		"#[no_mangle]",
		"pub extern \"C\" fn natcatty_abi_version() -> i32",
		"#[link(wasm_import_module = \"natcatty_host_v1\")]",
		"pub fn now_ns() -> i64",
		"pub fn random_get(buffer: &mut [u8]) -> i32",
		"pub mod service {",
		"#[link(wasm_import_module = \"natcatty:svc/storage_v1\")]",
		"pub const METHOD_GET: u32 = 1;",
		"pub fn call_method(method_id: u32, request: &[u8], response: &mut [u8]) -> i32",
	} {
		if !strings.Contains(rust, want) {
			t.Errorf("generated Rust is missing %q", want)
		}
	}
}

func TestRejectsUnsupportedTypes(t *testing.T) {
	_, err := idl.ParseString(`idl "x" {
  version "1.0.0"
  abi 1
  host "h" {
    f(value: f128)
  }
  guest "g" {
    export init "natcatty_init" () -> i32
  }
}`)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("ParseString error = %v, want unknown type", err)
	}
}

func TestRejectsDuplicateDeclarations(t *testing.T) {
	_, err := idl.ParseString(`idl "x" {
  version "1.0.0"
  abi 1
  enum LogLevel : i32 {
    debug = 0
    debug = 1
  }
  host "h" {
    f() -> i32
  }
  guest "g" {
    export init "natcatty_init" () -> i32
  }
}`)
	if err == nil || !strings.Contains(err.Error(), "repeats name") {
		t.Fatalf("ParseString error = %v, want duplicate name", err)
	}
}

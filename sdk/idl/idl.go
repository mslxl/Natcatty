// Package idl parses the Natcatty plugin IDL and generates the checked-in
// host Go types and Rust bindings.
//
// The IDL is versioned with the wire ABI major. The checked-in outputs are
// regenerated with:
//
//	go run ./sdk/idl/gen
//
// Tests compare the generated output with the committed files, so a change to
// the IDL that is not regenerated fails CI.
package idl

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

//go:embed natcatty_v1.idl
var source string

// Source returns the current IDL text.
func Source() string { return source }

// Definition is a parsed IDL document.
type Definition struct {
	Name     string
	Version  string
	ABI      int
	Enums    []Enum
	Records  []Record
	Handles  []Handle
	Hosts    []Host
	Guests   []Guest
	Services []Service
}

// Enum is an integer enumeration on the wire.
type Enum struct {
	Name   string
	Base   string
	Values []EnumValue
}

// EnumValue is one named enumeration value.
type EnumValue struct {
	Name  string
	Value int
}

// Record is a fixed set of fields. Records are lowered field by field; they
// never cross the boundary as a pointer.
type Record struct {
	Name   string
	Fields []Field
}

// Field is one record field.
type Field struct {
	Name string
	Type string
}

// Handle is an opaque generation-tagged resource reference.
type Handle struct {
	Name string
	Base string
}

// Host is an imported module.
type Host struct {
	Name      string
	Functions []Function
}

// Guest is the guest export surface.
type Guest struct {
	Name    string
	Exports []Export
}

// Service is one host-service module. Every service module exports exactly
// one function, call(method_id, req_ptr, req_len, resp_ptr, resp_cap) -> i32.
// Method IDs are stable within the module's major version: adding a method
// does not change an existing import.
type Service struct {
	Name    string
	Methods []ServiceMethod
}

// ServiceMethod is one stable method id within a service module.
type ServiceMethod struct {
	Name string
	ID   uint32
}

// Module returns the WebAssembly import module name for the service.
func (s Service) Module() string { return "natcatty:svc/" + s.Name + "_v1" }

// Function is one host import or guest export signature.
type Function struct {
	Name   string
	Params []Param
	Result string
}

// Param is one function parameter.
type Param struct {
	Name string
	Type string
}

// Export is one guest export. Optional exports are accepted but not required.
type Export struct {
	// Name is the symbolic IDL name, for example "abi_version".
	Name string
	// Wire is the WebAssembly export name, for example "natcatty_abi_version".
	Wire string
	// Params and Result describe the function signature.
	Params []Param
	Result string
	// Optional reports that the host accepts a module without this export.
	Optional bool
}

// Parse parses the embedded IDL.
func Parse() (*Definition, error) { return ParseString(source) }

// ParseString parses one IDL document.
func ParseString(text string) (*Definition, error) {
	tokens, err := lex(text)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	definition, err := p.parseDefinition()
	if err != nil {
		return nil, err
	}
	if err := definition.validate(); err != nil {
		return nil, err
	}
	return definition, nil
}

// HostModule returns the single imported host module.
func (d *Definition) HostModule() Host { return d.Hosts[0] }

// GuestModule returns the single guest export surface.
func (d *Definition) GuestModule() Guest { return d.Guests[0] }

// FunctionByName returns one host import by its wire name.
func (h Host) FunctionByName(name string) (Function, bool) {
	for _, function := range h.Functions {
		if function.Name == name {
			return function, true
		}
	}
	return Function{}, false
}

// RequiredExports returns the guest exports a reactor must provide.
func (g Guest) RequiredExports() []Export {
	var required []Export
	for _, export := range g.Exports {
		if !export.Optional {
			required = append(required, export)
		}
	}
	return required
}

func (d *Definition) validate() error {
	if d.Name == "" {
		return fmt.Errorf("idl: name is required")
	}
	if d.Version == "" {
		return fmt.Errorf("idl: version is required")
	}
	if d.ABI <= 0 {
		return fmt.Errorf("idl: abi must be positive")
	}
	if len(d.Hosts) != 1 {
		return fmt.Errorf("idl: exactly one host module is required, got %d", len(d.Hosts))
	}
	if len(d.Guests) != 1 {
		return fmt.Errorf("idl: exactly one guest module is required, got %d", len(d.Guests))
	}
	types := map[string]bool{
		"i32": true, "u32": true, "i64": true, "u64": true,
	}
	seenTypes := map[string]bool{}
	recordTypes := map[string]bool{}
	for _, enum := range d.Enums {
		if seenTypes[enum.Name] {
			return fmt.Errorf("idl: duplicate type %q", enum.Name)
		}
		seenTypes[enum.Name] = true
		if enum.Base != "i32" && enum.Base != "i64" {
			return fmt.Errorf("idl: enum %s must use i32 or i64", enum.Name)
		}
		types[enum.Name] = true
		values := map[int]bool{}
		names := map[string]bool{}
		for _, value := range enum.Values {
			if values[value.Value] {
				return fmt.Errorf("idl: enum %s repeats value %d", enum.Name, value.Value)
			}
			if names[value.Name] {
				return fmt.Errorf("idl: enum %s repeats name %q", enum.Name, value.Name)
			}
			values[value.Value] = true
			names[value.Name] = true
		}
	}
	for _, record := range d.Records {
		if seenTypes[record.Name] {
			return fmt.Errorf("idl: duplicate type %q", record.Name)
		}
		seenTypes[record.Name] = true
		fieldNames := map[string]bool{}
		for _, field := range record.Fields {
			if fieldNames[field.Name] {
				return fmt.Errorf("idl: record %s repeats field %q", record.Name, field.Name)
			}
			fieldNames[field.Name] = true
			if !types[field.Type] {
				return fmt.Errorf("idl: record %s field %s has unknown type %q",
					record.Name, field.Name, field.Type)
			}
		}
		types[record.Name] = true
		recordTypes[record.Name] = true
	}
	for _, handle := range d.Handles {
		if seenTypes[handle.Name] {
			return fmt.Errorf("idl: duplicate type %q", handle.Name)
		}
		seenTypes[handle.Name] = true
		if handle.Base != "u64" {
			return fmt.Errorf("idl: handle %s must use u64", handle.Name)
		}
		types[handle.Name] = true
	}
	checkFunction := func(where string, fn Function) error {
		for _, param := range fn.Params {
			if !types[param.Type] {
				return fmt.Errorf("idl: %s parameter %s has unknown type %q",
					where, param.Name, param.Type)
			}
			if recordTypes[param.Type] {
				return fmt.Errorf("idl: %s parameter %s uses record %s by value; pass its fields",
					where, param.Name, param.Type)
			}
		}
		if fn.Result != "" && !types[fn.Result] {
			return fmt.Errorf("idl: %s has unknown result type %q", where, fn.Result)
		}
		if recordTypes[fn.Result] {
			return fmt.Errorf("idl: %s returns record %s by value; return its fields", where, fn.Result)
		}
		return nil
	}
	for _, fn := range d.Hosts[0].Functions {
		if err := checkFunction("host function "+fn.Name, fn); err != nil {
			return err
		}
	}
	seenExports := map[string]bool{}
	for _, export := range d.Guests[0].Exports {
		if seenExports[export.Wire] {
			return fmt.Errorf("idl: duplicate guest export %q", export.Wire)
		}
		seenExports[export.Wire] = true
		if err := checkFunction("guest export "+export.Name, Function{
			Name:   export.Name,
			Params: export.Params,
			Result: export.Result,
		}); err != nil {
			return err
		}
	}
	serviceNames := map[string]bool{}
	moduleNames := map[string]bool{}
	for _, service := range d.Services {
		if service.Name == "" {
			return fmt.Errorf("idl: service name is required")
		}
		for index, r := range service.Name {
			lower := r >= 'a' && r <= 'z'
			digit := index > 0 && r >= '0' && r <= '9'
			if !lower && !digit {
				return fmt.Errorf("idl: service name %q must be lowercase letters and digits, starting with a letter", service.Name)
			}
		}
		if serviceNames[service.Name] {
			return fmt.Errorf("idl: duplicate service %q", service.Name)
		}
		serviceNames[service.Name] = true
		if moduleNames[service.Module()] {
			return fmt.Errorf("idl: duplicate service module %q", service.Module())
		}
		moduleNames[service.Module()] = true
		if len(service.Methods) == 0 {
			return fmt.Errorf("idl: service %s declares no methods", service.Name)
		}
		seenMethods := map[string]bool{}
		seenIDs := map[uint32]bool{}
		for _, method := range service.Methods {
			if method.Name == "" {
				return fmt.Errorf("idl: service %s has an unnamed method", service.Name)
			}
			if seenMethods[method.Name] {
				return fmt.Errorf("idl: service %s repeats method %q", service.Name, method.Name)
			}
			if seenIDs[method.ID] {
				return fmt.Errorf("idl: service %s repeats method id %d", service.Name, method.ID)
			}
			if method.ID == 0 {
				return fmt.Errorf("idl: service %s method %s uses reserved id 0", service.Name, method.Name)
			}
			seenMethods[method.Name] = true
			seenIDs[method.ID] = true
		}
	}
	return nil
}

// --- lexer ---

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenIdent
	tokenNumber
	tokenString
	tokenLBrace
	tokenRBrace
	tokenLParen
	tokenRParen
	tokenColon
	tokenComma
	tokenEquals
	tokenArrow
)

type token struct {
	kind tokenKind
	text string
	line int
}

func lex(text string) ([]token, error) {
	var tokens []token
	line := 1
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '{':
			tokens = append(tokens, token{tokenLBrace, "{", line})
			i++
		case c == '}':
			tokens = append(tokens, token{tokenRBrace, "}", line})
			i++
		case c == '(':
			tokens = append(tokens, token{tokenLParen, "(", line})
			i++
		case c == ')':
			tokens = append(tokens, token{tokenRParen, ")", line})
			i++
		case c == ':':
			tokens = append(tokens, token{tokenColon, ":", line})
			i++
		case c == ',':
			tokens = append(tokens, token{tokenComma, ",", line})
			i++
		case c == '=':
			tokens = append(tokens, token{tokenEquals, "=", line})
			i++
		case c == '-' && i+1 < len(text) && text[i+1] == '>':
			tokens = append(tokens, token{tokenArrow, "->", line})
			i += 2
		case c == '"':
			i++
			var value strings.Builder
			closed := false
			for i < len(text) {
				ch := text[i]
				if ch == '"' {
					closed = true
					i++
					break
				}
				if ch == '\n' {
					return nil, fmt.Errorf("idl: unterminated string at line %d", line)
				}
				if ch == '\\' && i+1 < len(text) {
					i++
					switch text[i] {
					case '"', '\\':
						value.WriteByte(text[i])
					case 'n':
						value.WriteByte('\n')
					case 't':
						value.WriteByte('\t')
					default:
						return nil, fmt.Errorf("idl: unsupported escape at line %d", line)
					}
					i++
					continue
				}
				value.WriteByte(ch)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("idl: unterminated string at line %d", line)
			}
			tokens = append(tokens, token{tokenString, value.String(), line})
		case isIdentStart(c):
			start := i
			for i < len(text) && isIdentPart(text[i]) {
				i++
			}
			tokens = append(tokens, token{tokenIdent, text[start:i], line})
		case c >= '0' && c <= '9' || (c == '-' && i+1 < len(text) && text[i+1] >= '0' && text[i+1] <= '9'):
			start := i
			i++
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				i++
			}
			tokens = append(tokens, token{tokenNumber, text[start:i], line})
		default:
			return nil, fmt.Errorf("idl: unexpected character %q at line %d", c, line)
		}
	}
	tokens = append(tokens, token{tokenEOF, "", line})
	return tokens, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9'
}

// --- parser ---

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token { return p.tokens[p.pos] }

func (p *parser) next() token {
	token := p.tokens[p.pos]
	if token.kind != tokenEOF {
		p.pos++
	}
	return token
}

func (p *parser) expect(kind tokenKind, what string) (token, error) {
	token := p.next()
	if token.kind != kind {
		return token, fmt.Errorf("idl: line %d: expected %s, got %q", token.line, what, token.text)
	}
	return token, nil
}

func (p *parser) expectIdent(what string) (token, error) {
	return p.expect(tokenIdent, what)
}

func (p *parser) expectKeyword(keyword string) error {
	token, err := p.expectIdent("keyword " + keyword)
	if err != nil {
		return err
	}
	if token.text != keyword {
		return fmt.Errorf("idl: line %d: expected %q, got %q", token.line, keyword, token.text)
	}
	return nil
}

func (p *parser) parseDefinition() (*Definition, error) {
	if err := p.expectKeyword("idl"); err != nil {
		return nil, err
	}
	name, err := p.expect(tokenString, "IDL name")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return nil, err
	}
	definition := &Definition{Name: name.text}
	for p.peek().kind != tokenRBrace {
		if p.peek().kind == tokenEOF {
			return nil, fmt.Errorf("idl: unexpected end of file")
		}
		keyword, err := p.expectIdent("declaration")
		if err != nil {
			return nil, err
		}
		switch keyword.text {
		case "version":
			if err := p.parseVersion(definition); err != nil {
				return nil, err
			}
		case "abi":
			if err := p.parseABI(definition); err != nil {
				return nil, err
			}
		case "enum":
			value, err := p.parseEnum()
			if err != nil {
				return nil, err
			}
			definition.Enums = append(definition.Enums, value)
		case "record":
			value, err := p.parseRecord()
			if err != nil {
				return nil, err
			}
			definition.Records = append(definition.Records, value)
		case "handle":
			value, err := p.parseHandle()
			if err != nil {
				return nil, err
			}
			definition.Handles = append(definition.Handles, value)
		case "host":
			value, err := p.parseHost()
			if err != nil {
				return nil, err
			}
			definition.Hosts = append(definition.Hosts, value)
		case "guest":
			value, err := p.parseGuest()
			if err != nil {
				return nil, err
			}
			definition.Guests = append(definition.Guests, value)
		case "service":
			value, err := p.parseService()
			if err != nil {
				return nil, err
			}
			definition.Services = append(definition.Services, value)
		default:
			return nil, fmt.Errorf("idl: line %d: unknown declaration %q", keyword.line, keyword.text)
		}
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return nil, err
	}
	if p.peek().kind != tokenEOF {
		return nil, fmt.Errorf("idl: line %d: trailing tokens", p.peek().line)
	}
	return definition, nil
}

func (p *parser) parseVersion(definition *Definition) error {
	value, err := p.expect(tokenString, "version string")
	if err != nil {
		return err
	}
	definition.Version = value.text
	return nil
}

func (p *parser) parseABI(definition *Definition) error {
	value, err := p.expect(tokenNumber, "ABI number")
	if err != nil {
		return err
	}
	abi, err := strconv.Atoi(value.text)
	if err != nil {
		return fmt.Errorf("idl: line %d: invalid ABI %q", value.line, value.text)
	}
	definition.ABI = abi
	return nil
}

func (p *parser) parseEnum() (Enum, error) {
	name, err := p.expectIdent("enum name")
	if err != nil {
		return Enum{}, err
	}
	if _, err := p.expect(tokenColon, ":"); err != nil {
		return Enum{}, err
	}
	base, err := p.expectIdent("enum base type")
	if err != nil {
		return Enum{}, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return Enum{}, err
	}
	enum := Enum{Name: name.text, Base: base.text}
	for p.peek().kind != tokenRBrace {
		value, err := p.expectIdent("enum value")
		if err != nil {
			return Enum{}, err
		}
		if _, err := p.expect(tokenEquals, "="); err != nil {
			return Enum{}, err
		}
		number, err := p.expect(tokenNumber, "enum value number")
		if err != nil {
			return Enum{}, err
		}
		parsed, err := strconv.Atoi(number.text)
		if err != nil {
			return Enum{}, fmt.Errorf("idl: line %d: invalid enum value %q", number.line, number.text)
		}
		enum.Values = append(enum.Values, EnumValue{Name: value.text, Value: parsed})
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return Enum{}, err
	}
	return enum, nil
}

func (p *parser) parseRecord() (Record, error) {
	name, err := p.expectIdent("record name")
	if err != nil {
		return Record{}, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return Record{}, err
	}
	record := Record{Name: name.text}
	for p.peek().kind != tokenRBrace {
		field, err := p.expectIdent("record field name")
		if err != nil {
			return Record{}, err
		}
		if _, err := p.expect(tokenColon, ":"); err != nil {
			return Record{}, err
		}
		typ, err := p.expectIdent("record field type")
		if err != nil {
			return Record{}, err
		}
		record.Fields = append(record.Fields, Field{Name: field.text, Type: typ.text})
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (p *parser) parseHandle() (Handle, error) {
	name, err := p.expectIdent("handle name")
	if err != nil {
		return Handle{}, err
	}
	if _, err := p.expect(tokenColon, ":"); err != nil {
		return Handle{}, err
	}
	base, err := p.expectIdent("handle base type")
	if err != nil {
		return Handle{}, err
	}
	return Handle{Name: name.text, Base: base.text}, nil
}

func (p *parser) parseHost() (Host, error) {
	name, err := p.expect(tokenString, "host module name")
	if err != nil {
		return Host{}, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return Host{}, err
	}
	host := Host{Name: name.text}
	for p.peek().kind != tokenRBrace {
		function, err := p.parseFunction()
		if err != nil {
			return Host{}, err
		}
		host.Functions = append(host.Functions, function)
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return Host{}, err
	}
	return host, nil
}

func (p *parser) parseGuest() (Guest, error) {
	name, err := p.expect(tokenString, "guest module name")
	if err != nil {
		return Guest{}, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return Guest{}, err
	}
	guest := Guest{Name: name.text}
	for p.peek().kind != tokenRBrace {
		optional := false
		if p.peek().kind == tokenIdent && p.peek().text == "optional" {
			p.next()
			optional = true
		}
		if err := p.expectKeyword("export"); err != nil {
			return Guest{}, err
		}
		symbolic, err := p.expectIdent("export name")
		if err != nil {
			return Guest{}, err
		}
		wire, err := p.expect(tokenString, "export string")
		if err != nil {
			return Guest{}, err
		}
		function, err := p.parseSignature(symbolic.text)
		if err != nil {
			return Guest{}, err
		}
		guest.Exports = append(guest.Exports, Export{
			Name:     symbolic.text,
			Wire:     wire.text,
			Params:   function.Params,
			Result:   function.Result,
			Optional: optional,
		})
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return Guest{}, err
	}
	return guest, nil
}

func (p *parser) parseService() (Service, error) {
	name, err := p.expect(tokenString, "service name")
	if err != nil {
		return Service{}, err
	}
	if _, err := p.expect(tokenLBrace, "{"); err != nil {
		return Service{}, err
	}
	service := Service{Name: name.text}
	for p.peek().kind != tokenRBrace {
		if err := p.expectKeyword("method"); err != nil {
			return Service{}, err
		}
		method, err := p.expectIdent("method name")
		if err != nil {
			return Service{}, err
		}
		if _, err := p.expect(tokenEquals, "="); err != nil {
			return Service{}, err
		}
		number, err := p.expect(tokenNumber, "method id")
		if err != nil {
			return Service{}, err
		}
		id, parseErr := strconv.ParseUint(number.text, 10, 32)
		if parseErr != nil {
			return Service{}, fmt.Errorf("idl: line %d: invalid method id %q", number.line, number.text)
		}
		service.Methods = append(service.Methods, ServiceMethod{Name: method.text, ID: uint32(id)})
	}
	if _, err := p.expect(tokenRBrace, "}"); err != nil {
		return Service{}, err
	}
	return service, nil
}

func (p *parser) parseFunction() (Function, error) {
	name, err := p.expectIdent("function name")
	if err != nil {
		return Function{}, err
	}
	return p.parseSignature(name.text)
}

func (p *parser) parseSignature(name string) (Function, error) {
	if _, err := p.expect(tokenLParen, "("); err != nil {
		return Function{}, err
	}
	function := Function{Name: name}
	for p.peek().kind != tokenRParen {
		if len(function.Params) > 0 {
			if _, err := p.expect(tokenComma, ","); err != nil {
				return Function{}, err
			}
		}
		param, err := p.expectIdent("parameter name")
		if err != nil {
			return Function{}, err
		}
		if _, err := p.expect(tokenColon, ":"); err != nil {
			return Function{}, err
		}
		typ, err := p.expectIdent("parameter type")
		if err != nil {
			return Function{}, err
		}
		function.Params = append(function.Params, Param{Name: param.text, Type: typ.text})
	}
	if _, err := p.expect(tokenRParen, ")"); err != nil {
		return Function{}, err
	}
	if p.peek().kind == tokenArrow {
		p.next()
		result, err := p.expectIdent("result type")
		if err != nil {
			return Function{}, err
		}
		function.Result = result.text
	}
	return function, nil
}

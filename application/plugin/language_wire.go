package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// The language wire protocol is UTF-8 JSON in both directions. The guest
// receives one full snapshot and answers with a closed envelope:
//
//	{"code":"ok","version":3,"data":{...}}
//	{"code":"invalid","message":"...","version":3}
//
// Data is capability-specific and strictly data-only: text, numbers, ranges,
// and lists. HTML is never accepted.

type languageRequestPayload struct {
	RequestID  uint64                          `json:"requestId"`
	DocumentID string                          `json:"documentId"`
	Version    uint64                          `json:"version"`
	LanguageID string                          `json:"languageId"`
	Content    string                          `json:"content"`
	Cursor     LanguagePosition                `json:"cursor"`
	Selection  *LanguageSelection              `json:"selection,omitempty"`
	Context    LanguageContext                 `json:"context"`
	Capability domainplugin.LanguageCapability `json:"capability"`
}

type languageResponseEnvelope struct {
	Code    string          `json:"code"`
	Message string          `json:"message,omitempty"`
	Version uint64          `json:"version"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type languageHighlightData struct {
	Highlights []LanguageHighlight `json:"highlights"`
}

type languageCompletionData struct {
	Items []LanguageCompletionItem `json:"items"`
}

type languageDiagnosticsData struct {
	Diagnostics []LanguageDiagnostic `json:"diagnostics"`
}

type languageFormattingData struct {
	Edits []LanguageTextEdit `json:"edits"`
}

type languageHoverData struct {
	Hover *LanguageHover `json:"hover"`
}

type languageSignatureData struct {
	Signatures      []LanguageSignature `json:"signatures"`
	ActiveSignature int                 `json:"activeSignature"`
}

type languageNavigationData struct {
	Locations []LanguageLocation `json:"locations"`
}

type languageCodeActionsData struct {
	Actions []LanguageCodeAction `json:"actions"`
}

// closed highlight token set. A provider cannot name a token kind the host
// does not style.
var languageHighlightKinds = map[string]bool{
	"keyword": true, "string": true, "number": true, "comment": true,
	"identifier": true, "type": true, "function": true, "operator": true,
	"punctuation": true, "variable": true, "constant": true,
}

// callProvider runs one provider request and validates its response. A
// failure is returned separately from the decoded data so the router can drop
// only that provider's contribution.
func (h *LanguageHost) callProvider(ctx context.Context, state *languageDocumentState,
	snapshot LanguageDocument, version uint64, provider *languageProvider,
	capability domainplugin.LanguageCapability) (LanguageDocumentResults, *LanguageProviderFailure) {
	requestID := h.nextRequest.Add(1)
	request := languageRequestPayload{
		RequestID: requestID, DocumentID: snapshot.ID, Version: version,
		LanguageID: snapshot.LanguageID, Content: snapshot.Content,
		Cursor: snapshot.Cursor, Selection: snapshot.Selection,
		Context: snapshot.Context, Capability: capability,
	}
	payload, canceled, err := h.invokeProvider(ctx, state, provider, requestID, capability, request)
	if canceled {
		// A superseded request is not a provider fault even when the provider
		// honors the cancel by returning without a response. The router drops
		// the result and retries on the newer snapshot.
		failure := canceledLanguageFailure(provider, capability)
		h.record(provider.ref, string(capability), failure.Code)
		return LanguageDocumentResults{}, &failure
	}
	if err != nil {
		failure := h.providerFailure(provider, capability, err)
		return LanguageDocumentResults{}, &failure
	}
	response, err := decodeLanguageResponse(payload)
	if err != nil {
		failure := h.providerFailure(provider, capability, err)
		return LanguageDocumentResults{}, &failure
	}
	if response.Code != ServiceCodeOK {
		message, _ := sanitizeLanguageText(response.Message, MaxLanguageMessageBytes)
		failure := LanguageProviderFailure{
			PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
			Fiber: provider.fiber, Capability: capability,
			Code: closedLanguageCode(response.Code), Message: message,
		}
		h.record(provider.ref, string(capability), failure.Code)
		return LanguageDocumentResults{}, &failure
	}
	if response.Version != version {
		failure := LanguageProviderFailure{
			PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
			Fiber: provider.fiber, Capability: capability,
			Code: "stale", Message: "response names a stale document version",
		}
		h.record(provider.ref, string(capability), "stale")
		return LanguageDocumentResults{}, &failure
	}
	if len(response.Data) > MaxLanguageResponseBytes {
		failure := LanguageProviderFailure{
			PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
			Fiber: provider.fiber, Capability: capability,
			Code: "limit", Message: "response exceeds the language result cap",
		}
		h.record(provider.ref, string(capability), "limit")
		return LanguageDocumentResults{}, &failure
	}
	result, truncated, err := decodeLanguageData(snapshot, version, capability, response.Data)
	if err != nil {
		failure := h.providerFailure(provider, capability, err)
		return LanguageDocumentResults{}, &failure
	}
	result.Truncated = truncated
	result.Providers = 1
	h.record(provider.ref, string(capability), ServiceCodeOK)
	return result, nil
}

// callFormatting runs only the chosen formatting provider and validates its
// edit set against the formatting caps.
func (h *LanguageHost) callFormatting(ctx context.Context, state *languageDocumentState,
	snapshot LanguageDocument, version uint64,
	provider *languageProvider) ([]LanguageTextEdit, bool, *LanguageProviderFailure) {
	requestID := h.nextRequest.Add(1)
	request := languageRequestPayload{
		RequestID: requestID, DocumentID: snapshot.ID, Version: version,
		LanguageID: snapshot.LanguageID, Content: snapshot.Content,
		Cursor: snapshot.Cursor, Selection: snapshot.Selection,
		Context: snapshot.Context, Capability: domainplugin.LanguageFormatting,
	}
	payload, canceled, err := h.invokeProvider(ctx, state, provider, requestID,
		domainplugin.LanguageFormatting, request)
	if canceled {
		failure := canceledLanguageFailure(provider, domainplugin.LanguageFormatting)
		h.record(provider.ref, string(domainplugin.LanguageFormatting), failure.Code)
		return nil, false, &failure
	}
	if err != nil {
		failure := h.providerFailure(provider, domainplugin.LanguageFormatting, err)
		return nil, false, &failure
	}
	response, err := decodeLanguageResponse(payload)
	if err != nil {
		failure := h.providerFailure(provider, domainplugin.LanguageFormatting, err)
		return nil, false, &failure
	}
	if response.Code != ServiceCodeOK {
		message, _ := sanitizeLanguageText(response.Message, MaxLanguageMessageBytes)
		failure := LanguageProviderFailure{
			PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
			Fiber: provider.fiber, Capability: domainplugin.LanguageFormatting,
			Code: closedLanguageCode(response.Code), Message: message,
		}
		h.record(provider.ref, string(domainplugin.LanguageFormatting), failure.Code)
		return nil, false, &failure
	}
	if response.Version != version {
		failure := LanguageProviderFailure{
			PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
			Fiber: provider.fiber, Capability: domainplugin.LanguageFormatting,
			Code: "stale", Message: "response names a stale document version",
		}
		h.record(provider.ref, string(domainplugin.LanguageFormatting), "stale")
		return nil, false, &failure
	}
	var data languageFormattingData
	if err := decodeClosedLanguageData(response.Data, &data); err != nil {
		failure := h.providerFailure(provider, domainplugin.LanguageFormatting, err)
		return nil, false, &failure
	}
	edits, truncated, err := validateFormattingEdits(data.Edits)
	if err != nil {
		failure := h.providerFailure(provider, domainplugin.LanguageFormatting, err)
		return nil, false, &failure
	}
	h.record(provider.ref, string(domainplugin.LanguageFormatting), ServiceCodeOK)
	return edits, truncated, nil
}

// invokeProvider performs the guest call and registers the request as
// in-flight for the document so a newer snapshot can mark it canceled. The
// returned canceled flag is captured under the document lock before the
// cooperative-cancellation record is cleared; without that, a call returning
// after supersession would look like a provider failure and degrade a healthy
// provider.
func (h *LanguageHost) invokeProvider(ctx context.Context, state *languageDocumentState,
	provider *languageProvider, requestID uint64,
	capability domainplugin.LanguageCapability,
	request languageRequestPayload) (payload []byte, canceled bool, err error) {
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return nil, false, ErrClosed
	}
	state.inflight[requestID] = languageInflight{key: provider.key(), fiber: provider.fiber}
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		delete(state.inflight, requestID)
		canceled = state.superseded[requestID]
		delete(state.superseded, requestID)
		state.mu.Unlock()
		h.clearCanceled(requestID)
	}()
	payload, err = json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode language request: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, LanguageDeadline(capability))
	defer cancel()
	payload, err = provider.invoker.InvokeLanguage(callCtx, LanguageRequest{
		ID: requestID, Kind: capability, Payload: payload,
	})
	return payload, false, err
}

func (h *LanguageHost) clearCanceled(requestID uint64) {
	h.cancelMu.Lock()
	delete(h.canceled, requestID)
	h.cancelMu.Unlock()
}

// canceledLanguageFailure is the transient outcome for a request the host
// superseded while it was in flight. It is deliberately not a provider fault:
// failureDegrades exempts the canceled code, so rapid edits cannot degrade a
// healthy provider.
func canceledLanguageFailure(provider *languageProvider,
	capability domainplugin.LanguageCapability) LanguageProviderFailure {
	return LanguageProviderFailure{
		PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
		Fiber: provider.fiber, Capability: capability,
		Code: "canceled", Message: "request was superseded",
	}
}

// providerFailure classifies a real provider error against the closed failure
// vocabulary. Cancellation is handled before an error reaches this path.
func (h *LanguageHost) providerFailure(provider *languageProvider,
	capability domainplugin.LanguageCapability,
	err error) LanguageProviderFailure {
	failure := LanguageProviderFailure{
		PluginID: provider.ref.ID, ProviderID: provider.contract.ID,
		Fiber: provider.fiber, Capability: capability,
	}
	message, _ := sanitizeLanguageText(err.Error(), MaxLanguageMessageBytes)
	failure.Code = languageFailureCode(err)
	failure.Message = message
	h.record(provider.ref, string(capability), failure.Code)
	return failure
}

// languageFailureCode maps runtime and validation failures to the closed
// failure vocabulary exposed to the editor.
func languageFailureCode(err error) string {
	var deadline *DeadlineError
	var limit *LimitError
	var noResponse *LanguageNoResponseError
	var trap *TrapError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &deadline):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrCallInFlight):
		return "busy"
	case errors.Is(err, ErrNotActive), errors.Is(err, ErrClosed):
		return "unavailable"
	case errors.As(err, &limit), errors.Is(err, ErrWouldBlock):
		return "limit"
	case errors.As(err, &noResponse):
		return "invalid"
	case errors.As(err, &trap):
		return "fault"
	default:
		return "fault"
	}
}

func closedLanguageCode(code string) string {
	switch code {
	case ServiceCodeDenied, ServiceCodeNotFound, ServiceCodeConflict,
		ServiceCodeInvalid, ServiceCodeLimit, ServiceCodeFault:
		return code
	default:
		return ServiceCodeFault
	}
}

func decodeLanguageResponse(payload []byte) (languageResponseEnvelope, error) {
	if len(payload) > MaxLanguageResponseBytes {
		return languageResponseEnvelope{}, &LimitError{
			Resource: "language response bytes", Limit: MaxLanguageResponseBytes,
		}
	}
	var response languageResponseEnvelope
	if err := decodeClosedLanguageData(payload, &response); err != nil {
		return languageResponseEnvelope{}, err
	}
	return response, nil
}

// decodeClosedLanguageData rejects unknown fields so results stay inside the
// closed data shapes; extra data is never silently rendered.
func decodeClosedLanguageData(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("language response has no data")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode language response: %w", err)
	}
	if decoder.More() {
		return errors.New("language response has trailing data")
	}
	return nil
}

// decodeLanguageData validates one capability payload and applies the
// per-provider caps.
func decodeLanguageData(snapshot LanguageDocument, version uint64,
	capability domainplugin.LanguageCapability, raw json.RawMessage) (LanguageDocumentResults, bool, error) {
	result := LanguageDocumentResults{
		DocumentID: snapshot.ID, Version: version, Capability: capability,
	}
	truncated := false
	switch capability {
	case domainplugin.LanguageHighlight:
		var data languageHighlightData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		for _, token := range data.Highlights {
			if !languageHighlightKinds[token.Kind] {
				return result, false, fmt.Errorf("unknown highlight token kind %q", token.Kind)
			}
			if err := validateLanguageRange(token.Range); err != nil {
				return result, false, err
			}
			result.Highlights = append(result.Highlights, token)
			if len(result.Highlights) >= MaxLanguageHighlights {
				truncated = true
				break
			}
		}
	case domainplugin.LanguageCompletion:
		var data languageCompletionData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		for _, item := range data.Items {
			sanitized, itemTruncated := sanitizeCompletionItem(item)
			if sanitized.Label == "" {
				return result, false, errors.New("completion item has no label")
			}
			result.Completions = append(result.Completions, sanitized)
			truncated = truncated || itemTruncated
			if len(result.Completions) >= MaxLanguageCompletionItems {
				truncated = true
				break
			}
		}
	case domainplugin.LanguageDiagnostics:
		var data languageDiagnosticsData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		for _, diagnostic := range data.Diagnostics {
			if diagnostic.Severity < LanguageSeverityError ||
				diagnostic.Severity > LanguageSeverityHint {
				return result, false, fmt.Errorf("diagnostic severity %d is outside 1..4",
					diagnostic.Severity)
			}
			if err := validateLanguageRange(diagnostic.Range); err != nil {
				return result, false, err
			}
			var itemTruncated bool
			diagnostic.Message, itemTruncated = sanitizeLanguageText(
				diagnostic.Message, MaxLanguageMessageBytes)
			truncated = truncated || itemTruncated
			diagnostic.Code, itemTruncated = sanitizeLanguageText(
				diagnostic.Code, MaxLanguageProviderLabelBytes)
			truncated = truncated || itemTruncated
			diagnostic.Source, itemTruncated = sanitizeLanguageText(
				diagnostic.Source, MaxLanguageProviderLabelBytes)
			truncated = truncated || itemTruncated
			if diagnostic.Message == "" {
				return result, false, errors.New("diagnostic has no message")
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			if len(result.Diagnostics) >= MaxLanguageDiagnostics {
				truncated = true
				break
			}
		}
	case domainplugin.LanguageFormatting:
		var data languageFormattingData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		edits, editsTruncated, err := validateFormattingEdits(data.Edits)
		if err != nil {
			return result, false, err
		}
		result.Formatting = edits
		truncated = editsTruncated
	case domainplugin.LanguageHover:
		var data languageHoverData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		if data.Hover != nil {
			hover, hoverTruncated, err := sanitizeHover(*data.Hover)
			if err != nil {
				return result, false, err
			}
			result.Hover = hover
			truncated = hoverTruncated
		}
	case domainplugin.LanguageSignatureHelp:
		var data languageSignatureData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		help, helpTruncated, err := sanitizeSignatureHelp(data.Signatures, data.ActiveSignature)
		if err != nil {
			return result, false, err
		}
		result.Signature = help
		truncated = helpTruncated
	case domainplugin.LanguageNavigation:
		var data languageNavigationData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		for _, location := range data.Locations {
			// The navigation capability is same-document only. A cross-file
			// target is dropped, not rendered as a broken jump.
			if location.DocumentID != "" && location.DocumentID != snapshot.ID {
				continue
			}
			location.DocumentID = snapshot.ID
			if err := validateLanguageRange(location.Range); err != nil {
				return result, false, err
			}
			var itemTruncated bool
			location.Label, itemTruncated = sanitizeLanguageText(
				location.Label, MaxLanguageProviderLabelBytes)
			truncated = truncated || itemTruncated
			result.Navigation = append(result.Navigation, location)
			if len(result.Navigation) >= MaxLanguageLocations {
				truncated = true
				break
			}
		}
	case domainplugin.LanguageCodeActions:
		var data languageCodeActionsData
		if err := decodeClosedLanguageData(raw, &data); err != nil {
			return result, false, err
		}
		totalBytes := 0
		for _, action := range data.Actions {
			var itemTruncated bool
			action.Title, itemTruncated = sanitizeLanguageText(action.Title, MaxLanguageProviderLabelBytes)
			if action.Title == "" {
				return result, false, errors.New("code action has no title")
			}
			action.Kind, itemTruncated = sanitizeLanguageText(action.Kind, MaxLanguageProviderLabelBytes)
			truncated = truncated || itemTruncated
			edits, editsTruncated, err := validateFormattingEdits(action.Edits)
			if err != nil {
				return result, false, err
			}
			action.Edits = edits
			truncated = truncated || editsTruncated
			actionBytes := 0
			for _, edit := range edits {
				actionBytes += len(edit.NewText)
			}
			if totalBytes+actionBytes > MaxLanguageFormattingBytes {
				truncated = true
				break
			}
			totalBytes += actionBytes
			result.CodeActions = append(result.CodeActions, action)
			if len(result.CodeActions) >= MaxLanguageCodeActions {
				truncated = true
				break
			}
		}
	default:
		return result, false, fmt.Errorf("unknown language capability %q", capability)
	}
	return result, truncated, nil
}

func validateFormattingEdits(edits []LanguageTextEdit) ([]LanguageTextEdit, bool, error) {
	truncated := false
	total := 0
	out := make([]LanguageTextEdit, 0, len(edits))
	for _, edit := range edits {
		if err := validateLanguageRange(edit.Range); err != nil {
			return nil, false, err
		}
		text, textTruncated := sanitizeLanguageText(edit.NewText, MaxLanguageFormattingBytes)
		truncated = truncated || textTruncated
		edit.NewText = text
		total += len(text)
		if total > MaxLanguageFormattingBytes {
			truncated = true
			if len(out) == 0 {
				return nil, true, nil
			}
			break
		}
		out = append(out, edit)
		if len(out) >= MaxLanguageFormattingEdits {
			truncated = true
			break
		}
	}
	return out, truncated, nil
}

func sanitizeCompletionItem(item LanguageCompletionItem) (LanguageCompletionItem, bool) {
	truncated := false
	var itemTruncated bool
	item.Label, itemTruncated = sanitizeLanguageText(item.Label, MaxLanguageProviderLabelBytes)
	truncated = truncated || itemTruncated
	if item.InsertText == "" {
		item.InsertText = item.Label
	}
	item.InsertText, itemTruncated = sanitizeLanguageText(item.InsertText, MaxLanguageInsertTextBytes)
	truncated = truncated || itemTruncated
	item.Kind, itemTruncated = sanitizeLanguageText(item.Kind, MaxLanguageProviderLabelBytes)
	truncated = truncated || itemTruncated
	item.Detail, itemTruncated = sanitizeLanguageText(item.Detail, MaxLanguageDetailBytes)
	truncated = truncated || itemTruncated
	item.Documentation, itemTruncated = sanitizeLanguageText(item.Documentation, MaxLanguageDocumentationBytes)
	truncated = truncated || itemTruncated
	item.SortKey, itemTruncated = sanitizeLanguageText(item.SortKey, MaxLanguageProviderLabelBytes)
	truncated = truncated || itemTruncated
	item.FilterKey, itemTruncated = sanitizeLanguageText(item.FilterKey, MaxLanguageProviderLabelBytes)
	truncated = truncated || itemTruncated
	return item, truncated
}

func sanitizeHover(hover LanguageHover) (*LanguageHover, bool, error) {
	format := hover.Format
	if format == "" {
		format = "plain"
	}
	if format != "plain" && format != "markdown" {
		return nil, false, fmt.Errorf("hover format %q is not plain or markdown", hover.Format)
	}
	var truncated bool
	if format == "markdown" {
		hover.Contents = sanitizeRestrictedMarkdown(hover.Contents)
	}
	hover.Contents, truncated = sanitizeLanguageText(hover.Contents, MaxLanguageHoverBytes)
	hover.Format = format
	if hover.Contents == "" {
		return nil, truncated, nil
	}
	return &hover, truncated, nil
}

func sanitizeSignatureHelp(signatures []LanguageSignature,
	active int) (*LanguageSignatureHelp, bool, error) {
	if len(signatures) == 0 {
		return nil, false, nil
	}
	truncated := false
	out := make([]LanguageSignature, 0, len(signatures))
	for _, signature := range signatures {
		var itemTruncated bool
		signature.Label, itemTruncated = sanitizeLanguageText(signature.Label, MaxLanguageProviderLabelBytes)
		truncated = truncated || itemTruncated
		signature.Documentation, itemTruncated = sanitizeLanguageText(
			signature.Documentation, MaxLanguageDocumentationBytes)
		truncated = truncated || itemTruncated
		if signature.Label == "" {
			return nil, false, errors.New("signature has no label")
		}
		if len(signature.Parameters) > MaxLanguageSignatureParameters {
			signature.Parameters = signature.Parameters[:MaxLanguageSignatureParameters]
			truncated = true
		}
		parameters := make([]LanguageSignatureParameter, 0, len(signature.Parameters))
		for _, parameter := range signature.Parameters {
			parameter.Label, itemTruncated = sanitizeLanguageText(
				parameter.Label, MaxLanguageProviderLabelBytes)
			truncated = truncated || itemTruncated
			parameter.Documentation, itemTruncated = sanitizeLanguageText(
				parameter.Documentation, MaxLanguageDocumentationBytes)
			truncated = truncated || itemTruncated
			parameters = append(parameters, parameter)
		}
		signature.Parameters = parameters
		if signature.ActiveParameter < 0 {
			signature.ActiveParameter = 0
		}
		if signature.ActiveParameter >= len(signature.Parameters) {
			signature.ActiveParameter = len(signature.Parameters) - 1
			if signature.ActiveParameter < 0 {
				signature.ActiveParameter = 0
			}
		}
		out = append(out, signature)
		if len(out) >= MaxLanguageSignatures {
			truncated = true
			break
		}
	}
	if active < 0 || active >= len(out) {
		active = 0
	}
	return &LanguageSignatureHelp{Signatures: out, ActiveSignature: active}, truncated, nil
}

// sanitizeLanguageText bounds one rendered string and removes NUL and control
// characters the host renderer must never receive. It truncates on a rune
// boundary and reports whether it changed the text.
func sanitizeLanguageText(text string, limit int) (string, bool) {
	if limit <= 0 {
		return "", text != ""
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	var out strings.Builder
	out.Grow(min(len(text), limit))
	truncated := false
	written := 0
	for _, r := range text {
		if r == 0 || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			truncated = true
			continue
		}
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(utf8.RuneError))
		}
		if written+size > limit {
			truncated = true
			break
		}
		out.WriteRune(r)
		written += size
	}
	return out.String(), truncated
}

// sanitizeRestrictedMarkdown strips raw HTML tags and dangerous URI schemes
// from hover markdown. The host renders the remainder as markdown; anything
// that could be interpreted as markup is removed rather than trusted.
func sanitizeRestrictedMarkdown(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '<' {
			// Drop a tag-like sequence: <tag ...>, </tag>, <!-- ... -->.
			if i+1 < len(text) {
				next := text[i+1]
				if next == '!' || next == '/' || next == '?' ||
					next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' {
					end := strings.IndexByte(text[i:], '>')
					if end >= 0 {
						i += end + 1
						continue
					}
				}
			}
		}
		out.WriteByte(text[i])
		i++
	}
	sanitized := out.String()
	for _, scheme := range []string{"javascript:", "data:", "vbscript:"} {
		sanitized = removeFold(sanitized, scheme)
	}
	return sanitized
}

// removeFold removes every ASCII case-insensitive occurrence of the lowercase
// needle from text.
func removeFold(text, needle string) string {
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); {
		if i+len(needle) <= len(text) && equalFoldASCII(text[i:i+len(needle)], needle) {
			i += len(needle)
			continue
		}
		out = append(out, text[i])
		i++
	}
	return string(out)
}

func equalFoldASCII(candidate, lower string) bool {
	if len(candidate) != len(lower) {
		return false
	}
	for i := 0; i < len(candidate); i++ {
		c := candidate[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

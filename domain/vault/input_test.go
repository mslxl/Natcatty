package vault

import (
	"encoding/json"
	"testing"

	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
)

func TestNormalizeDraftKeepsSSHInputCompatibility(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label:           "web",
		Hostname:        "example.com",
		Protocol:        ProtocolSSH,
		NewlineMode:     modelterminal.NewlineCRLF,
		BackspaceMode:   modelterminal.BackspaceCtrlH,
		LineEditingMode: modelterminal.LineEditingByte,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.NewlineMode != modelterminal.NewlineCRLF ||
		draft.BackspaceMode != modelterminal.BackspaceCtrlH ||
		draft.LineEditingMode != modelterminal.LineEditingByte {
		t.Fatalf("draft = %+v, want the SSH input modes preserved", draft)
	}
}

func TestNormalizeDraftClearsInputCompatibilityOutsidePlainSSH(t *testing.T) {
	for _, test := range []struct {
		name      string
		protocol  Protocol
		transport Transport
	}{
		{"telnet", ProtocolTelnet, TransportPlain},
		{"vnc", ProtocolVNC, TransportPlain},
		{"mosh", ProtocolSSH, TransportMosh},
		{"et", ProtocolSSH, TransportET},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft, err := NormalizeDraft(Draft{
				Label:           "web",
				Hostname:        "example.com",
				Protocol:        test.protocol,
				Transport:       test.transport,
				NewlineMode:     modelterminal.NewlineLF,
				BackspaceMode:   modelterminal.BackspaceCtrlH,
				LineEditingMode: modelterminal.LineEditingByte,
			})
			if err != nil {
				t.Fatalf("NormalizeDraft: %v", err)
			}
			if got := draftInputCompatibility(draft); !got.IsDefault() {
				t.Fatalf("%s kept %+v, want the default", test.name, got)
			}
		})
	}
}

func TestNormalizeDraftFallsBackForUnknownInputModes(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label:           "web",
		Hostname:        "example.com",
		Protocol:        ProtocolSSH,
		NewlineMode:     modelterminal.NewlineMode("unknown"),
		BackspaceMode:   modelterminal.BackspaceMode("unknown"),
		LineEditingMode: modelterminal.LineEditingMode("unknown"),
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if got := draftInputCompatibility(draft); !got.IsDefault() {
		t.Fatalf("draft = %+v, want the default input modes", got)
	}
}

func TestHostInputCompatibilityRoundTripsThroughJSON(t *testing.T) {
	host := Host{
		ID:              "host-1",
		Label:           "web",
		Hostname:        "example.com",
		Protocol:        ProtocolSSH,
		NewlineMode:     modelterminal.NewlineLF,
		BackspaceMode:   modelterminal.BackspaceCtrlH,
		LineEditingMode: modelterminal.LineEditingByte,
	}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal fields: %v", err)
	}
	for key, want := range map[string]string{
		"newlineBehavior":     "lf",
		"backspaceBehavior":   "ctrl-h",
		"lineEditingBehavior": "byte",
	} {
		if got, _ := fields[key].(string); got != want {
			t.Errorf("JSON %s = %q, want %q", key, got, want)
		}
	}

	var decoded Host
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal Host: %v", err)
	}
	if got := decoded.InputCompatibility(); got != host.InputCompatibility() {
		t.Fatalf("decoded input modes = %+v, want %+v", got, host.InputCompatibility())
	}
	if got := decoded.InputCompatibility(); got != (modelterminal.InputCompatibility{
		Newline:     modelterminal.NewlineLF,
		Backspace:   modelterminal.BackspaceCtrlH,
		LineEditing: modelterminal.LineEditingByte,
	}) {
		t.Fatalf("InputCompatibility() = %+v", got)
	}
}

func TestHostInputCompatibilityIgnoresOtherProtocolsAndUnknownValues(t *testing.T) {
	if got := (Host{Protocol: ProtocolTelnet, NewlineMode: modelterminal.NewlineLF}).
		InputCompatibility(); !got.IsDefault() {
		t.Fatalf("Telnet Host compatibility = %+v, want the default", got)
	}
	if got := (Host{
		Protocol:      ProtocolSSH,
		NewlineMode:   modelterminal.NewlineMode("unknown"),
		BackspaceMode: modelterminal.BackspaceMode("unknown"),
	}).InputCompatibility(); !got.IsDefault() {
		t.Fatalf("unknown stored modes = %+v, want the default", got)
	}
}

// draftInputCompatibility reads the modes from a normalized draft through the
// same value the Session will receive.
func draftInputCompatibility(draft Draft) modelterminal.InputCompatibility {
	return Host{
		Protocol:        draft.Protocol,
		NewlineMode:     draft.NewlineMode,
		BackspaceMode:   draft.BackspaceMode,
		LineEditingMode: draft.LineEditingMode,
	}.InputCompatibility()
}

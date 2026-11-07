package et

import (
	"errors"
	"testing"
)

func TestConfigNormalizeAndValidate(t *testing.T) {
	cfg := Config{
		Hostname: " example.test ",
		Username: " alice ",
	}.Normalized()
	if cfg.Hostname != "example.test" || cfg.Username != "alice" {
		t.Fatalf("normalized endpoint = %+v", cfg)
	}
	if cfg.Port != DefaultPort || cfg.ETPort != DefaultETPort {
		t.Fatalf("ports = %d/%d", cfg.Port, cfg.ETPort)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := (Config{}).Normalized().Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty hostname = %v", err)
	}
	bad := Config{Hostname: "host", ETPort: 70000}.Normalized()
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad et port = %v", err)
	}
	bad = Config{Hostname: "host", Port: -1}.Normalized()
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad port = %v", err)
	}
}

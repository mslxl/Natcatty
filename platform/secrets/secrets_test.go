package secrets

import (
	"errors"
	"fmt"
	"testing"

	"github.com/egoist/mygo"
)

func TestMapErrorNamesTheStatesTheAdapterDistinguishes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"none", nil, nil},
		{"not found", fmt.Errorf("wrapped: %w", mygo.ErrSecretNotFound), ErrNotFound},
		{"unsupported", fmt.Errorf("wrapped: %w", errors.ErrUnsupported), ErrUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapError(test.err)
			if test.want == nil {
				if got != nil {
					t.Fatalf("mapError = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("mapError = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMapErrorKeepsAnUnknownFailure(t *testing.T) {
	original := errors.New("keychain locked")
	if got := mapError(original); !errors.Is(got, original) {
		t.Fatalf("mapError = %v, want the original error", got)
	}
}

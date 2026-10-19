package connection

import (
	"reflect"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func candidateIDs(plan AuthPlan) []string {
	ids := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func testKey(id string, label string) *KeyMaterial {
	return &KeyMaterial{ID: model.KeyID(id), Label: label, PrivateKey: "material"}
}

func TestBuildAuthPlanPasswordModeIsolatesPassword(t *testing.T) {
	t.Parallel()

	plan := BuildAuthPlan(AuthSpec{
		Username:    "alice",
		Mode:        AuthModePassword,
		Password:    "secret",
		SelectedKey: testKey("key-1", "work"),
		DefaultKeys: []KeyMaterial{*testKey("key-2", "home")},
		UseAgent:    true,
	})
	want := []string{CandidatePassword, CandidateKeyboardInteractive}
	if got := candidateIDs(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if plan.FailClosed {
		t.Error("password mode must not fail closed on a key")
	}
}

func TestBuildAuthPlanMFAOrdersKeyboardInteractiveFirstWhenPasswordMissing(t *testing.T) {
	t.Parallel()

	passwordPlan := BuildAuthPlan(AuthSpec{Mode: AuthModePassword, RequiresMFA: true})
	if got, want := candidateIDs(passwordPlan), []string{CandidateKeyboardInteractive}; !reflect.DeepEqual(got, want) {
		t.Fatalf("password-mode candidates = %v, want %v", got, want)
	}

	autoPlan := BuildAuthPlan(AuthSpec{
		Mode:        AuthModeAuto,
		RequiresMFA: true,
		UseAgent:    true,
		SelectedKey: testKey("key-1", "work"),
	})
	want := []string{CandidateKeyboardInteractive, CandidateAgent, "publickey-user"}
	if got := candidateIDs(autoPlan); !reflect.DeepEqual(got, want) {
		t.Fatalf("auto-mode candidates = %v, want %v", got, want)
	}
}

func TestBuildAuthPlanSelectedKeyFailsClosedAndHasNoAgentFallback(t *testing.T) {
	t.Parallel()

	plan := BuildAuthPlan(AuthSpec{
		Mode:        AuthModeKey,
		Password:    "secret",
		SelectedKey: testKey("key-1", "work"),
		DefaultKeys: []KeyMaterial{*testKey("key-2", "home")},
		UseAgent:    true,
	})
	want := []string{"publickey-user", CandidatePassword, CandidateKeyboardInteractive}
	if got := candidateIDs(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if !plan.FailClosed {
		t.Error("FailClosed = false, want true for a selected key")
	}
	if plan.Candidates[0].Key == nil || plan.Candidates[0].Key.ID != "key-1" {
		t.Errorf("first candidate key = %+v, want key-1", plan.Candidates[0].Key)
	}
	if !plan.Candidates[0].Primary {
		t.Error("selected key is not marked primary")
	}
}

func TestBuildAuthPlanAutoOrdersAgentKeyPasswordThenKeyboardInteractive(t *testing.T) {
	t.Parallel()

	plan := BuildAuthPlan(AuthSpec{
		Mode:        AuthModeAuto,
		Password:    "secret",
		SelectedKey: testKey("key-1", "work"),
		DefaultKeys: []KeyMaterial{*testKey("key-2", "home")},
		UseAgent:    true,
		RequiresMFA: true,
	})
	want := []string{CandidateAgent, "publickey-user", "publickey-default-home", CandidateKeyboardInteractive, CandidatePassword}
	if got := candidateIDs(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestPromoteCachedCandidateOnlyInAutoMode(t *testing.T) {
	t.Parallel()

	auto := BuildAuthPlan(AuthSpec{
		Mode:     AuthModeAuto,
		Password: "secret",
		UseAgent: true,
	})
	PromoteCachedCandidate(&auto, CandidatePassword, AuthModeAuto)
	if got, want := candidateIDs(auto), []string{CandidateAgent, CandidatePassword, CandidateKeyboardInteractive}; !reflect.DeepEqual(got, want) {
		t.Fatalf("password was promoted in auto mode: %v", got)
	}
	PromoteCachedCandidate(&auto, CandidateAgent, AuthModeAuto)
	if got, want := candidateIDs(auto), []string{CandidateAgent, CandidatePassword, CandidateKeyboardInteractive}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agent promotion changed order: %v", got)
	}

	withKey := BuildAuthPlan(AuthSpec{
		Mode:        AuthModeAuto,
		SelectedKey: testKey("key-1", "work"),
		UseAgent:    true,
	})
	PromoteCachedCandidate(&withKey, "publickey-user", AuthModeAuto)
	if got, want := candidateIDs(withKey), []string{"publickey-user", CandidateAgent, CandidateKeyboardInteractive}; !reflect.DeepEqual(got, want) {
		t.Fatalf("publickey promotion = %v, want %v", got, want)
	}

	keyMode := BuildAuthPlan(AuthSpec{Mode: AuthModeKey, SelectedKey: testKey("key-1", "work")})
	PromoteCachedCandidate(&keyMode, CandidateKeyboardInteractive, AuthModeKey)
	if got, want := candidateIDs(keyMode), []string{"publickey-user", CandidateKeyboardInteractive}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit key mode was reordered: %v", got)
	}
}

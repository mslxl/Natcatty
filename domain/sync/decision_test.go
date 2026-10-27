package sync

import "testing"

func TestDecideCoversEveryChangeCombination(t *testing.T) {
	tests := []struct {
		name          string
		localChanged  bool
		remoteChanged bool
		strategy      Strategy
		want          Action
	}{
		{"both unchanged", false, false, StrategySmartMerge, ActionNone},
		{"local only", true, false, StrategySmartMerge, ActionUpload},
		{"remote only", false, true, StrategySmartMerge, ActionDownload},
		{"both, smart merge", true, true, StrategySmartMerge, ActionMerge},
		{"both, smart merge preserve", true, true, StrategySmartMergePreserve, ActionMerge},
		{"both, prefer local", true, true, StrategyPreferLocal, ActionUpload},
		{"both, prefer cloud", true, true, StrategyPreferCloud, ActionDownload},
		{"legacy newest merges", true, true, StrategyNewest, ActionMerge},
		{"legacy local uploads", true, true, StrategyLocal, ActionUpload},
		{"legacy remote downloads", true, true, StrategyRemote, ActionDownload},
		{"unknown strategy merges", true, true, Strategy("bogus"), ActionMerge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Decide(test.localChanged, test.remoteChanged, test.strategy)
			if got != test.want {
				t.Fatalf("Decide = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeStrategy(t *testing.T) {
	for _, strategy := range []Strategy{
		StrategySmartMerge, StrategySmartMergePreserve, StrategyPreferCloud, StrategyPreferLocal,
	} {
		if got := NormalizeStrategy(strategy); got != strategy {
			t.Errorf("NormalizeStrategy(%q) = %q", strategy, got)
		}
	}
	legacy := map[Strategy]Strategy{
		StrategyNewest:   StrategySmartMerge,
		StrategyLocal:    StrategyPreferLocal,
		StrategyRemote:   StrategyPreferCloud,
		Strategy(""):     StrategySmartMerge,
		Strategy("junk"): StrategySmartMerge,
	}
	for value, want := range legacy {
		if got := NormalizeStrategy(value); got != want {
			t.Errorf("NormalizeStrategy(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestStrategyMergesAndPreserves(t *testing.T) {
	if !Merges(StrategySmartMerge) || !Merges(StrategySmartMergePreserve) {
		t.Fatal("both merge strategies must merge")
	}
	if Merges(StrategyPreferCloud) || Merges(StrategyPreferLocal) {
		t.Fatal("whole-side strategies must not merge")
	}
	if !IsPreserveDeletionsStrategy(StrategySmartMergePreserve) {
		t.Fatal("smartMergePreserve must preserve deletions")
	}
	if IsPreserveDeletionsStrategy(StrategySmartMerge) {
		t.Fatal("smartMerge propagates deletions")
	}
}

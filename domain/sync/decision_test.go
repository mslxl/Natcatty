package sync

import "testing"

func TestDecideCoversEveryChangeCombination(t *testing.T) {
	tests := []struct {
		name          string
		localChanged  bool
		remoteChanged bool
		strategy      Strategy
		localAt       int64
		remoteAt      int64
		want          Action
	}{
		{"both unchanged", false, false, StrategyNewest, 100, 200, ActionNone},
		{"local only", true, false, StrategyNewest, 100, 200, ActionUpload},
		{"remote only", false, true, StrategyNewest, 100, 200, ActionDownload},
		{"both, local newer", true, true, StrategyNewest, 300, 200, ActionUpload},
		{"both, remote newer", true, true, StrategyNewest, 100, 200, ActionDownload},
		{"both, tie goes remote", true, true, StrategyNewest, 200, 200, ActionDownload},
		{"both, pinned local", true, true, StrategyLocal, 100, 200, ActionUpload},
		{"both, pinned remote", true, true, StrategyRemote, 300, 200, ActionDownload},
		{"unknown strategy uses newest", true, true, Strategy("bogus"), 300, 200, ActionUpload},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Decide(test.localChanged, test.remoteChanged, test.strategy, test.localAt, test.remoteAt)
			if got != test.want {
				t.Fatalf("Decide = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeStrategy(t *testing.T) {
	for _, strategy := range []Strategy{StrategyNewest, StrategyLocal, StrategyRemote} {
		if got := NormalizeStrategy(strategy); got != strategy {
			t.Errorf("NormalizeStrategy(%q) = %q", strategy, got)
		}
	}
	if got := NormalizeStrategy(""); got != StrategyNewest {
		t.Errorf("empty strategy = %q", got)
	}
}

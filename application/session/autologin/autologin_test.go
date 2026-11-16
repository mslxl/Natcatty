package autologin

import "testing"

// recorder collects the lines the detector writes.
type recorder struct {
	writes []string
}

func (r *recorder) write(line []byte) { r.writes = append(r.writes, string(line)) }

func newTestEngine(options Options) (*Engine, *recorder) {
	recorder := &recorder{}
	options.Write = recorder.write
	return New(options), recorder
}

func TestAnswersTheLoginAndPasswordPrompts(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret", MaxAttempts: 3})

	engine.Feed("login: ")
	assertWrites(t, recorder, "admin\r")

	engine.Feed("\r\nPassword: ")
	assertWrites(t, recorder, "admin\r", "secret\r")

	engine.Feed("\r\n$ ")
	if !engine.Completed() || engine.Active() {
		t.Fatal("a command prompt after credentials should complete the detector")
	}
	assertWrites(t, recorder, "admin\r", "secret\r")
}

func TestAnswersAPromptStrippedOfANSIStyling(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.Feed("\x1b[32mlo\x1b[0mg" + "in: ")
	assertWrites(t, recorder, "admin\r")
}

func TestWakeOnlyEnterKeepsTheDetectorArmed(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})

	engine.UserInput([]byte("\r"))
	engine.UserInput([]byte("\n"))
	engine.UserInput([]byte("\r\n"))
	if !engine.Active() {
		t.Fatal("a wake-only Enter disarmed the detector before the first prompt")
	}

	engine.Feed("login: ")
	engine.Feed("\r\nPassword: ")
	assertWrites(t, recorder, "admin\r", "secret\r")
}

func TestWakeOnlyEnterAfterCredentialsIsATakeover(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})

	engine.Feed("login: ")
	engine.UserInput([]byte("\r"))
	engine.Feed("\r\nPassword: ")
	if engine.Active() {
		t.Fatal("the detector stayed armed after the user typed Enter at the login prompt")
	}
	assertWrites(t, recorder, "admin\r")
}

func TestOtherInputBeforeThePromptIsATakeover(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.UserInput([]byte("x"))
	engine.Feed("login: ")
	engine.Feed("\r\nPassword: ")
	if engine.Active() {
		t.Fatal("the detector stayed armed after ordinary typed input")
	}
	assertWrites(t, recorder)
}

func TestWrongCredentialsStopAtTheAttemptLimit(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Username: "admin", Password: "bad", MaxAttempts: 2,
	})

	engine.Feed("login: ")
	engine.Feed("\r\nPassword: ")
	engine.Feed("\r\nLogin incorrect\r\nlogin: ")
	engine.Feed("\r\nPassword: ")
	engine.Feed("\r\nLogin incorrect\r\nlogin: ")

	if engine.Active() {
		t.Fatal("the detector kept arming after the attempt limit")
	}
	assertWrites(t, recorder, "admin\r", "bad\r", "admin\r", "bad\r")
}

func TestRepeatedPasswordPromptCountsAgainstTheLimit(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Username: "admin", Password: "bad", MaxAttempts: 2,
	})

	engine.Feed("login: ")
	engine.Feed("\r\nPassword: ")
	engine.Feed("\r\nPassword: ")
	engine.Feed("\r\nPassword: ")

	if engine.Active() {
		t.Fatal("the detector kept arming after the attempt limit")
	}
	assertWrites(t, recorder, "admin\r", "bad\r", "bad\r")
}

func TestAnswersAContinueBannerOnceBeforeTheLoginPrompt(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})

	engine.Feed("Press RETURN to continue")
	engine.Feed("\r\nlogin: ")
	engine.Feed("\r\nPress RETURN to continue")
	engine.Feed("\r\nPassword: ")

	assertWrites(t, recorder, "\r", "admin\r", "secret\r")
}

func TestConfiguredExpectRepliesAnswerCustomPrompts(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Username: "admin",
		Password: "secret",
		Replies: []Reply{
			{Expect: "Are you sure", Respond: "yes"},
			{Expect: "Hostname:", Respond: "console"},
		},
	})

	engine.Feed("Are you sure? [y/n] ")
	engine.Feed("\r\nHostname: ")
	engine.Feed("\r\nlogin: ")

	if !engine.Active() {
		t.Fatal("the detector should still wait for the password prompt")
	}
	assertWrites(t, recorder, "yes\r", "console\r", "admin\r")
}

func TestAnswersBothPromptsFromOneChunk(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.Feed("Username: \r\nPassword: ")
	assertWrites(t, recorder, "admin\r", "secret\r")
}

func TestNoCredentialsLeavesTheEngineIdle(t *testing.T) {
	engine, recorder := newTestEngine(Options{})
	if engine.Active() {
		t.Fatal("an engine without credentials should not arm")
	}
	engine.Feed("login: \r\nPassword: ")
	assertWrites(t, recorder)
}

func TestCancelStopsWritingWithoutCompleting(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.Cancel()
	engine.Feed("login: \r\nPassword: ")
	if engine.Completed() || engine.Active() {
		t.Fatal("a cancelled detector changed state")
	}
	assertWrites(t, recorder)
}

func assertWrites(t *testing.T, recorder *recorder, want ...string) {
	t.Helper()
	if len(recorder.writes) != len(want) {
		t.Fatalf("writes = %q, want %q", recorder.writes, want)
	}
	for index := range want {
		if recorder.writes[index] != want[index] {
			t.Fatalf("writes = %q, want %q", recorder.writes, want)
		}
	}
}

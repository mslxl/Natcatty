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

// TestLastLoginBannerDoesNotConsumeAnAttempt covers a console that prints
// "Last login: user" before its real prompt. The banner ends in a word the
// username matcher would otherwise accept, and with a one-attempt limit it
// must not answer or disarm before "login:" arrives.
func TestLastLoginBannerDoesNotConsumeAnAttempt(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Username: "admin", Password: "secret", MaxAttempts: 1,
	})

	engine.Feed("Last login: user\r\n")
	if !engine.Active() {
		t.Fatal("the login banner disarmed the detector")
	}
	assertWrites(t, recorder)

	engine.Feed("login: ")
	engine.Feed("\r\nPassword: ")
	if !engine.Active() {
		t.Fatal("the one-attempt detector disarmed before the real prompt")
	}
	assertWrites(t, recorder, "admin\r", "secret\r")

	// A banner after the credentials must not pass for the shell prompt.
	engine.Feed("\r\nLast login: admin from 10.0.0.5\r\n")
	if engine.Completed() || !engine.Active() {
		t.Fatal("the banner completed the detector before a command prompt")
	}
	engine.Feed("$ ")
	if !engine.Completed() {
		t.Fatal("the command prompt did not complete the detector")
	}
}

// TestRepliesOnlyProfileArmsTheDetector makes a custom expect/reply rule a
// complete automation on its own, with no username or password saved.
func TestRepliesOnlyProfileArmsTheDetector(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Replies: []Reply{{Expect: "Site code:", Respond: "lab-1"}},
	})

	if !engine.Active() {
		t.Fatal("a replies-only profile did not arm the detector")
	}
	engine.Feed("Site code: ")
	assertWrites(t, recorder, "lab-1\r")
}

// TestTakeoverDisarmsWithoutWaitingForAWrite proves a takeover from a
// broadcast or an inserted command stops the detector immediately, even when
// it would otherwise treat the bytes as a harmless wake Enter.
func TestTakeoverDisarmsWithoutWaitingForAWrite(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})

	engine.Takeover()
	if engine.Active() {
		t.Fatal("a takeover left the detector armed")
	}
	engine.Feed("login: \r\nPassword: ")
	assertWrites(t, recorder)
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

// TestPasswordOnlyProfileSpendsOneAttemptPerPassword is the accounting
// contract for a console that asks for the password first, with no username.
// The first password send opens an attempt just as the first username does, so
// the number of sends never exceeds the configured limit. The old tests always
// fed "login: " first, which let the username branch hide this off-by-one.
func TestPasswordOnlyProfileSpendsOneAttemptPerPassword(t *testing.T) {
	const password = "MaxAttempts=1"
	cases := []struct {
		name        string
		maxAttempts int
		want        int
	}{
		{name: "one", maxAttempts: 1, want: 1},
		{name: "two", maxAttempts: 2, want: 2},
		{name: "default", maxAttempts: 0, want: DefaultAttempts},
		{name: "explicit default", maxAttempts: DefaultAttempts, want: DefaultAttempts},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			engine, recorder := newTestEngine(Options{
				Password: password, PasswordSet: true, MaxAttempts: testCase.maxAttempts,
			})

			engine.Feed("Password: ")
			for index := 1; index < testCase.want; index++ {
				engine.Feed("\r\nPassword: ")
			}
			// One prompt past the limit must not be answered.
			engine.Feed("\r\nPassword: ")

			want := make([]string, testCase.want)
			for index := range want {
				want[index] = password + "\r"
			}
			assertWrites(t, recorder, want...)
			if engine.attempts != testCase.want {
				t.Fatalf("attempts = %d, want %d", engine.attempts, testCase.want)
			}
			if engine.Active() {
				t.Fatal("the detector stayed armed past the attempt limit")
			}
		})
	}
}

// TestFirstPromptPasswordCountsItsAttempt covers a profile that also has a
// username: when the console asks for the password before the username, that
// first password still spends one of the configured attempts.
func TestFirstPromptPasswordCountsItsAttempt(t *testing.T) {
	const password = "MaxAttempts=1"
	engine, recorder := newTestEngine(Options{
		Username: "admin", Password: password, PasswordSet: true, MaxAttempts: 1,
	})

	engine.Feed("Password: ")
	assertWrites(t, recorder, password+"\r")
	if engine.attempts != 1 {
		t.Fatalf("attempts = %d, want 1 after the first password", engine.attempts)
	}

	engine.Feed("\r\nPassword: ")
	if engine.Active() {
		t.Fatal("a second password prompt stayed armed past a one-attempt limit")
	}
	assertWrites(t, recorder, password+"\r")
}

// TestPasswordBannerDoesNotSendTheSavedPassword covers a MOTD or security
// notice that merely ends in the word "password". Before the banner filter the
// detector answered it, spent the one allowed attempt, and then never answered
// the real prompt.
func TestPasswordBannerDoesNotSendTheSavedPassword(t *testing.T) {
	const password = "MaxAttempts=1"
	banners := []string{
		"Never share your password",
		"Do not reveal your password",
		"Never share your password:",
		"Your password expires in 30 days",
	}
	for _, banner := range banners {
		t.Run(banner, func(t *testing.T) {
			engine, recorder := newTestEngine(Options{
				Username: "admin", Password: password, PasswordSet: true, MaxAttempts: 1,
			})

			engine.Feed(banner + "\r\n")
			assertWrites(t, recorder)
			if !engine.Active() {
				t.Fatal("the banner disarmed the detector")
			}
			if engine.attempts != 0 {
				t.Fatalf("attempts = %d, want 0 after a banner", engine.attempts)
			}

			engine.Feed("login: ")
			engine.Feed("\r\nPassword: ")
			assertWrites(t, recorder, "admin\r", password+"\r")
			engine.Feed("\r\n$ ")
			if !engine.Completed() {
				t.Fatal("the real login did not complete")
			}
		})
	}
}

// TestPasswordPromptForms keeps the real forms answerable while the sentence
// filter rejects notices.
func TestPasswordPromptForms(t *testing.T) {
	const password = "MaxAttempts=1"
	cases := []struct {
		name  string
		feeds []string
	}{
		{name: "colon", feeds: []string{"Password: "}},
		{name: "lowercase", feeds: []string{"password:"}},
		{name: "for user", feeds: []string{"Password for user: "}},
		{name: "ansi", feeds: []string{"\x1b[1mPassword:\x1b[0m "}},
		{name: "split reads", feeds: []string{"Pass", "word: "}},
		{name: "bare word", feeds: []string{"Password"}},
		// The original detector's terminator is optional: vendor consoles
		// print the credential word at the end of an imperative sentence, with
		// no punctuation at all (Kylin V10 "Input Password", issue #1293).
		{name: "input password", feeds: []string{"Input Password"}},
		{name: "input password colon", feeds: []string{"Input Password: "}},
		{name: "chinese", feeds: []string{"输入密码"}},
		{name: "enter your password", feeds: []string{"Enter your password: "}},
		{name: "please enter your password", feeds: []string{"Please enter your password: "}},
		{name: "type your password", feeds: []string{"Type your password:"}},
		{name: "your password", feeds: []string{"Your password: "}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			engine, recorder := newTestEngine(Options{
				Password: password, PasswordSet: true, MaxAttempts: 1,
			})
			for _, feed := range testCase.feeds {
				engine.Feed(feed)
			}
			assertWrites(t, recorder, password+"\r")
		})
	}
}

// TestRepliesOnlyProfileCompletesAtACommandPrompt pins the firedAny branch of
// the completion check: a profile with no saved credentials still completes
// when its custom answer is followed by the console's shell prompt.
func TestRepliesOnlyProfileCompletesAtACommandPrompt(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Replies: []Reply{{Expect: "Site code:", Respond: "lab-1"}},
	})
	engine.Feed("Site code: ")
	assertWrites(t, recorder, "lab-1\r")
	if engine.Completed() {
		t.Fatal("the detector completed before a command prompt")
	}
	engine.Feed("\r\n$ ")
	if !engine.Completed() || engine.Active() {
		t.Fatal("a replied profile did not complete at the command prompt")
	}
}

// TestTakeoverDropsQueuedAnswers asserts the invariant Takeover documents:
// answers queued by advance never reach the transport after a takeover. Feed
// drains under the same lock, so the queue is seeded directly to make the
// removal of the clearing assignment observable.
func TestTakeoverDropsQueuedAnswers(t *testing.T) {
	engine, recorder := newTestEngine(Options{
		Username: "admin", Password: "secret", MaxAttempts: 1,
	})
	engine.mu.Lock()
	engine.pending = [][]byte{[]byte("admin\r"), []byte("secret\r")}
	engine.mu.Unlock()

	engine.Takeover()

	engine.mu.Lock()
	pending := engine.pending
	engine.mu.Unlock()
	if pending != nil {
		t.Fatalf("Takeover left %d queued writes", len(pending))
	}
	engine.Feed("login: \r\nPassword: ")
	assertWrites(t, recorder)
}

// TestWakeEnterAfterABannerFollowingCredentialsIsATakeover makes the
// !sentUser guard observable: after the username was sent, a banner is the last
// line, so no prompt condition would disarm a naked Enter. It must still be a
// takeover because credentials were already transmitted.
func TestWakeEnterAfterABannerFollowingCredentialsIsATakeover(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.Feed("login: ")
	engine.Feed("\r\nSession started\r\n")
	assertWrites(t, recorder, "admin\r")

	engine.UserInput([]byte("\r"))
	if engine.Active() {
		t.Fatal("a bare Enter after credentials was treated as a wake")
	}
}

// TestWakeEnterAfterABannerFollowingAPasswordIsATakeover is the password-only
// counterpart: sentPass alone must disarm the wake exemption.
func TestWakeEnterAfterABannerFollowingAPasswordIsATakeover(t *testing.T) {
	engine, recorder := newTestEngine(Options{Password: "secret", PasswordSet: true})
	engine.Feed("Password: ")
	engine.Feed("\r\nWelcome\r\n")
	assertWrites(t, recorder, "secret\r")

	engine.UserInput([]byte("\r"))
	if engine.Active() {
		t.Fatal("a bare Enter after a password was treated as a wake")
	}
}

// TestDefaultAttemptsIsThree pins the literal default. The behavior test above
// derives its expectation from DefaultAttempts, so moving the constant would
// move the expectation with it; this guard keeps the original limit observable.
func TestDefaultAttemptsIsThree(t *testing.T) {
	if DefaultAttempts != 3 {
		t.Fatalf("DefaultAttempts = %d, want the original limit 3", DefaultAttempts)
	}
}

// TestDisarmDuringEmitDropsTheRemainingAnswers covers the interleaving between
// Feed draining its queue and writing it: a takeover or cancel from inside the
// first write must stop the answers that were already drained. Without a
// takeover epoch the second queued line is still written after the disarm.
func TestDisarmDuringEmitDropsTheRemainingAnswers(t *testing.T) {
	cases := []struct {
		name   string
		disarm func(*Engine)
	}{
		{name: "takeover", disarm: (*Engine).Takeover},
		{name: "cancel", disarm: (*Engine).Cancel},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var engine *Engine
			var writes []string
			engine = New(Options{
				Username: "admin", Password: "secret", MaxAttempts: 1,
				Write: func(line []byte) {
					writes = append(writes, string(line))
					testCase.disarm(engine)
				},
			})

			engine.Feed("login: \r\nPassword: ")
			if len(writes) != 1 || writes[0] != "admin\r" {
				t.Fatalf("writes = %q, want only the first answer", writes)
			}
		})
	}
}

// TestCancelDropsQueuedAnswers is the Cancel counterpart of
// TestTakeoverDropsQueuedAnswers. Removing Cancel's clearing assignment leaves
// the seeded queue observable.
func TestCancelDropsQueuedAnswers(t *testing.T) {
	engine, recorder := newTestEngine(Options{Username: "admin", Password: "secret"})
	engine.mu.Lock()
	engine.pending = [][]byte{[]byte("admin\r"), []byte("secret\r")}
	engine.mu.Unlock()

	engine.Cancel()

	engine.mu.Lock()
	pending := engine.pending
	engine.mu.Unlock()
	if pending != nil {
		t.Fatalf("Cancel left %d queued writes", len(pending))
	}
	engine.Feed("login: \r\nPassword: ")
	assertWrites(t, recorder)
}

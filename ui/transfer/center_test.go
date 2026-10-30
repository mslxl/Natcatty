package transfer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// centerQueueRepo seeds the Transfer center with history rows and records the
// saves the queue performs, so a view test can render finished and
// restart-restored rows without running a copy.
type centerQueueRepo struct {
	tasks []domainsftp.Task
}

func (r *centerQueueRepo) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	out := make([]domainsftp.Task, len(r.tasks))
	copy(out, r.tasks)
	return out, nil
}

func (r *centerQueueRepo) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.tasks = append([]domainsftp.Task(nil), tasks...)
	return nil
}

func centerSeed(now time.Time) []domainsftp.Task {
	return []domainsftp.Task{
		{
			ID: "t1", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
			DisplayName: "alpha.txt", SourceLabel: "local", DestLabel: "Test",
			Sources: []string{"/alpha.txt"}, DestDir: "/remote",
			BytesDone: 512, BytesTotal: 2048, FilesTotal: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "t2", Kind: domainsftp.OpCopy, Status: domainsftp.TaskCompleted,
			DisplayName: "beta.bin", SourceLabel: "local", DestLabel: "local",
			BytesDone: 100, BytesTotal: 100, FilesDone: 1, FilesTotal: 1,
			CreatedAt: now, UpdatedAt: now,
		},
	}
}

// newCenterFixture builds the SFTP surface over a live queue whose history is
// restored from a restart, then opens nothing: the test drives the header
// button or the state directly.
func newCenterFixture(t *testing.T, locale string, width, height int, seed []domainsftp.Task,
	options ...transferstate.Option) (*transferstate.State, *ui.Tester) {
	t.Helper()
	local := t.TempDir()
	writeFile(t, local+"/alpha.txt", "alpha")
	service := newViewService(local, t.TempDir())
	queue := apptransfer.NewQueue(service, apptransfer.WithQueueRepository(&centerQueueRepo{tasks: seed}))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)

	stateOptions := append([]transferstate.Option{transferstate.WithQueue(queue)}, options...)
	state := transferstate.NewState(service, stateOptions...)
	t.Cleanup(state.Shutdown)

	tester := ui.NewTester(View(state), width, height)
	tester.SetLocale(locale)
	tester.Frame()
	frameUntil(t, tester, "the local listing", func() bool {
		tab := state.Tab(transferstate.Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	return state, tester
}

func insideWindow(t *testing.T, tester *ui.Tester, label string, width, height int) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(label)
	if !ok {
		t.Fatalf("%q was not laid out; texts %q", label, tester.Texts())
	}
	if rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("%q has an empty box: %+v", label, rect)
	}
	if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(width)+0.5 || rect.Y+rect.H > float32(height)+0.5 {
		t.Fatalf("%q is clipped at %dx%d: %+v", label, width, height, rect)
	}
	return rect
}

// centerBindingStore is the persisted binding document for the center's
// shortcut tests.
type centerBindingStore struct{ state appkeybindings.State }

func (s *centerBindingStore) Load(context.Context) (appkeybindings.State, error) {
	return s.state, nil
}
func (s *centerBindingStore) Save(context.Context, appkeybindings.State) error { return nil }

func newCenterBindings(t *testing.T) *appkeybindings.Service {
	t.Helper()
	service := appkeybindings.NewService(&centerBindingStore{}, appkeybindings.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return service
}

// TestTransferCenterBackdropClosesAndPanelDoesNot drives real pointer input:
// a click inside the panel leaves the center open, and a click on the
// backdrop closes it.
func TestTransferCenterBackdropClosesAndPanelDoesNot(t *testing.T) {
	const width, height = 1100, 720
	state, tester := newCenterFixture(t, "en", width, height, centerSeed(time.Now()))
	state.OpenCenter()
	tester.Frame()
	panel, ok := tester.Find("Transfer center")
	if !ok {
		t.Fatalf("the center is not shown: %q", tester.Texts())
	}
	tester.ClickAt(panel.X+panel.W/2, panel.Y+panel.H/2)
	if !state.CenterOpen() {
		t.Fatal("a click inside the panel closed the center")
	}
	// The panel starts at centerTop and is centered, so the top-left corner
	// is backdrop.
	tester.ClickAt(12, 12)
	if state.CenterOpen() {
		t.Fatal("a click on the backdrop did not close the center")
	}
}

// TestTransferCenterConfiguredChordClosesIt proves the effective, rebindable
// transfer-center chord dismisses an open center even though a Modal overlay
// suppresses the window shortcut that opened it. Escape and Dismiss still
// work.
func TestTransferCenterConfiguredChordClosesIt(t *testing.T) {
	kb := newCenterBindings(t)
	state, tester := newCenterFixture(t, "en", 1100, 720, centerSeed(time.Now()),
		transferstate.WithKeybindings(kb))
	if chord := state.Shortcut("transferCenter"); chord != "Ctrl + Shift + T" {
		t.Fatalf("the configured chord = %q, want Ctrl + Shift + T", chord)
	}

	state.OpenCenter()
	tester.Frame()
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyT)
	if state.CenterOpen() {
		t.Fatal("the configured chord did not close the center")
	}

	state.OpenCenter()
	tester.Frame()
	tester.Key(0, ui.KeyEscape)
	if state.CenterOpen() {
		t.Fatal("Escape did not close the center")
	}

	state.OpenCenter()
	tester.Frame()
	if err := tester.Click("Dismiss"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if state.CenterOpen() {
		t.Fatal("Dismiss did not close the center")
	}

	// A rebinding moves the chord; the old default must not keep working.
	if err := kb.ReplaceBinding("transfer-center", domainkeybindings.SchemePC,
		"Alt + Shift + T", nil); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	state.OpenCenter()
	tester.Frame()
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyT)
	if !state.CenterOpen() {
		t.Fatal("the old default chord still closed the center after rebinding")
	}
	tester.Key(ui.Alt|ui.Shift, ui.KeyT)
	if state.CenterOpen() {
		t.Fatal("the rebound chord did not close the center")
	}
}

// TestTransferCenterShowsReconnectOnAFailedRow proves a row whose transport
// was lost explains how to recover instead of showing only a generic error.
func TestTransferCenterShowsReconnectOnAFailedRow(t *testing.T) {
	now := time.Now()
	seed := []domainsftp.Task{{
		ID: "conn", Kind: domainsftp.OpUpload, Status: domainsftp.TaskFailed,
		DisplayName: "alpha.txt", SourceLabel: "local", DestLabel: "Test",
		Sources: []string{"/alpha.txt"}, DestDir: "/remote",
		BytesDone: 0, BytesTotal: 2048, FilesTotal: 1,
		Error: "connection", ReconnectRequired: true,
		CreatedAt: now, UpdatedAt: now,
	}}
	state, tester := newCenterFixture(t, "en", 1100, 720, seed)
	state.OpenCenter()
	tester.Frame()
	for _, want := range []string{"Failed", "Connection failed.", "Reconnect to this Host"} {
		if !tester.HasText(want) {
			t.Errorf("the failed row does not show %q; texts %q", want, tester.Texts())
		}
	}
}

// intrinsicTextSize measures one line of text at an unconstrained width, so a
// test can prove a hint was neither ellipsized nor cut off.
func intrinsicTextSize(t *testing.T, text string, fontSize float32) (float32, float32) {
	t.Helper()
	var width, height float32
	tester := ui.NewTester(func(c *ui.Context) {
		width, height = c.MeasureText(0, ui.Span{Text: text, Size: fontSize})
	}, 200, 100)
	tester.Frame()
	if width <= 0 || height <= 0 {
		t.Fatalf("the reference text %q measured %vx%v", text, width, height)
	}
	return width, height
}

// TestTransferCenterShowsTheFullReconnectHintAtEverySize proves the English
// reconnect hint is not ellipsized at either window size. The hint needs its
// own line: the detail line also carries the panes, progress, and file count,
// which left too little room for the sentence in both English widths.
func TestTransferCenterShowsTheFullReconnectHintAtEverySize(t *testing.T) {
	now := time.Now()
	for _, tag := range []string{"en", "zh-CN"} {
		m := locales.In(tag)
		hint := m.SftpTransferReconnectRequired()
		refWidth, _ := intrinsicTextSize(t, hint, 10)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			seed := []domainsftp.Task{{
				ID: "conn", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
				DisplayName: "alpha.txt", SourceLabel: "local", DestLabel: "Test",
				Sources: []string{"/alpha.txt"}, DestDir: "/remote",
				BytesDone: 512, BytesTotal: 2048, FilesDone: 1, FilesTotal: 2,
				ReconnectRequired: true, CreatedAt: now, UpdatedAt: now,
			}}
			state, tester := newCenterFixture(t, tag, size[0], size[1], seed)
			state.OpenCenter()
			tester.Frame()

			rect, ok := tester.Find(hint)
			if !ok {
				t.Fatalf("%s at %dx%d: the full hint %q is not one text; texts %q",
					tag, size[0], size[1], hint, tester.Texts())
			}
			if rect.X < 0 || rect.Y < 0 ||
				rect.X+rect.W > float32(size[0])+0.5 || rect.Y+rect.H > float32(size[1])+0.5 {
				t.Fatalf("%s at %dx%d: the hint is outside the window: %+v", tag, size[0], size[1], rect)
			}
			if rect.H <= 0 {
				t.Fatalf("%s at %dx%d: the hint has no height: %+v", tag, size[0], size[1], rect)
			}
			// A box at least as wide as the sentence's own line renders all of
			// it; anything narrower would end in an ellipsis.
			if rect.W+0.5 < refWidth {
				t.Fatalf("%s at %dx%d: %q needs %.1fpx on one line but its box is only %.1fpx wide, so it is truncated",
					tag, size[0], size[1], hint, refWidth, rect.W)
			}
		}
	}
}

func TestTransferCenterShowsProgressAndControls(t *testing.T) {
	const width, height = 1100, 720
	state, tester := newCenterFixture(t, "en", width, height, centerSeed(time.Now()))
	if !tester.HasText("Transfers") {
		t.Fatalf("the queue strip is missing; texts %q", tester.Texts())
	}
	if err := tester.Click("Open the Transfer center"); err != nil {
		t.Fatalf("open the center: %v", err)
	}
	tester.Frame()
	if !state.CenterOpen() {
		t.Fatal("the strip's button did not open the center")
	}
	for _, want := range []string{
		"Transfer center", "alpha.txt", "Interrupted", "Retry", "Resume",
		"Completed", "Local → Test", "512 B / 2.0 KB · 25%",
	} {
		if !tester.HasText(want) {
			t.Errorf("the center does not show %q; texts %q", want, tester.Texts())
		}
	}
	for _, label := range []string{"Transfer center", "alpha.txt", "Completed", "Retry", "Resume"} {
		insideWindow(t, tester, label, width, height)
	}
}

func TestTransferCenterFitsTheSmallWindowInChinese(t *testing.T) {
	const width, height = 720, 480
	state, tester := newCenterFixture(t, "zh-CN", width, height, centerSeed(time.Now()))
	state.OpenCenter()
	tester.Frame()
	for _, want := range []string{"传输中心", "alpha.txt", "已中断", "重试", "继续", "已完成"} {
		if !tester.HasText(want) {
			t.Errorf("the center does not show %q; texts %q", want, tester.Texts())
		}
	}
	for _, label := range []string{"传输中心", "alpha.txt", "已中断", "重试", "继续", "已完成"} {
		insideWindow(t, tester, label, width, height)
	}
}

func TestTransferCenterRetryReportsAReconnect(t *testing.T) {
	state, tester := newCenterFixture(t, "en", 1100, 720, centerSeed(time.Now()))
	state.OpenCenter()
	tester.Frame()
	if err := tester.Click("Retry"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	tester.Frame()
	if got := state.CenterError(); got != "transfer-reconnect-required" {
		t.Errorf("center error = %q, want transfer-reconnect-required", got)
	}
	if !tester.HasText("Reconnect to this Host") {
		t.Errorf("the reconnect message is not shown; texts %q", tester.Texts())
	}
}

// TestTransferCenterLocalizesTheLocalPaneAndPerFileDetail proves the center
// translates the local-pane sentinel, the multi-file label, the current file,
// and a persisted failure code instead of rendering raw tokens.
func TestTransferCenterLocalizesTheLocalPaneAndPerFileDetail(t *testing.T) {
	now := time.Now()
	seed := []domainsftp.Task{
		{
			ID: "multi", Kind: domainsftp.OpCopy, Status: domainsftp.TaskFailed,
			Sources:     []string{"/a.bin", "/b.bin", "/c.bin"},
			SourceLabel: "local", DestLabel: "local",
			BytesDone: 512, BytesTotal: 2048, FilesDone: 1, FilesTotal: 3,
			CurrentFile: "/b.bin", Error: "exists",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "queued", Kind: domainsftp.OpCopy, Status: domainsftp.TaskQueued,
			Sources:     []string{"/a.bin", "/b.bin"},
			SourceLabel: "local", DestLabel: "local",
			CreatedAt: now.Add(time.Second), UpdatedAt: now,
		},
	}
	state, tester := newCenterFixture(t, "zh-CN", 1100, 720, seed)
	state.OpenCenter()
	tester.Frame()
	for _, want := range []string{
		"本地 → 本地", "1 / 3 个文件", "b.bin", "已存在同名项目。", "2 个项目",
	} {
		if !tester.HasText(want) {
			t.Errorf("the center does not show %q; texts %q", want, tester.Texts())
		}
	}
}

func TestTransferCenterEmptyState(t *testing.T) {
	state, tester := newCenterFixture(t, "en", 1100, 720, nil)
	state.OpenCenter()
	tester.Frame()
	if !tester.HasText("No transfers yet.") {
		t.Errorf("empty center texts %q", tester.Texts())
	}
	if !tester.HasText("6 transfers per resource") {
		t.Errorf("the default per-resource limit is not shown; texts %q", tester.Texts())
	}
}

func TestTransferCenterEtaIsHonest(t *testing.T) {
	en := locales.In("en")
	if got := etaText(en, 0); got != "" {
		t.Errorf("etaText(0) = %q, want empty", got)
	}
	if got := etaText(en, 9); got != "9 seconds left" {
		t.Errorf("etaText(9) = %q", got)
	}
	if got := etaText(en, 90); got != "2 minutes left" {
		t.Errorf("etaText(90) = %q", got)
	}
	if got := etaText(en, 3600); got != "1 hour left" {
		t.Errorf("etaText(3600) = %q", got)
	}

	task := domainsftp.Task{
		Status: domainsftp.TaskTransferring, SourceLabel: "local", DestLabel: "Test",
		BytesDone: 1024, BytesTotal: 10240, Speed: 1024, FilesTotal: 1,
	}
	if detail := transferDetail(en, task); !strings.Contains(detail, "9 seconds left") {
		t.Errorf("transferring detail = %q, want the ETA", detail)
	}
	task.Speed = 1023
	if detail := transferDetail(en, task); strings.Contains(detail, "left") {
		t.Errorf("detail without a speed = %q, want no ETA", detail)
	}
	task.Speed = 1024
	task.Status = domainsftp.TaskPaused
	if detail := transferDetail(en, task); strings.Contains(detail, "left") {
		t.Errorf("paused detail = %q, want no ETA", detail)
	}

	zh := locales.In("zh-CN")
	if !strings.Contains(zh.SftpTransferEtaSeconds(5), "还剩 5 秒") {
		t.Errorf("Chinese ETA = %q", zh.SftpTransferEtaSeconds(5))
	}
}

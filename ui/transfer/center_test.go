package transfer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
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
func newCenterFixture(t *testing.T, locale string, width, height int, seed []domainsftp.Task) (*transferstate.State, *ui.Tester) {
	t.Helper()
	local := t.TempDir()
	writeFile(t, local+"/alpha.txt", "alpha")
	service := newViewService(local, t.TempDir())
	queue := apptransfer.NewQueue(service, apptransfer.WithQueueRepository(&centerQueueRepo{tasks: seed}))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)

	state := transferstate.NewState(service, transferstate.WithQueue(queue))
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

func TestTransferCenterShowsProgressAndControls(t *testing.T) {
	const width, height = 1100, 720
	state, tester := newCenterFixture(t, "en", width, height, centerSeed(time.Now()))
	if !tester.HasText("Transfers") {
		t.Fatalf("the header has no Transfers button; texts %q", tester.Texts())
	}
	if err := tester.Click("Transfers"); err != nil {
		t.Fatalf("open the center: %v", err)
	}
	tester.Frame()
	if !state.CenterOpen() {
		t.Fatal("the header button did not open the center")
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

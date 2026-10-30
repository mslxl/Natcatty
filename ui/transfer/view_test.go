package transfer

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const frameTimeout = 5 * time.Second

func frameUntil(t *testing.T, tester *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; texts: %v", frameTimeout, what, tester.Texts())
		}
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
}

func TestDualPanesFitAtBothWindowSizes(t *testing.T) {
	for _, tag := range []string{"en", "zh-CN"} {
		m := locales.In(tag)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			width, height := float32(size[0]), float32(size[1])
			f := newViewFixture(t, size[0], size[1], tag)

			// The left pane's Tab, breadcrumb, and toolbar; the right pane's
			// Tab. Each label's first match is the left pane's.
			labels := []string{
				m.SftpLocal(),
				"Test",
				m.SftpGoHome(),
				m.SftpBookmarks(),
				m.SftpCopyPath(),
				m.SftpCompactList(),
				m.SftpFilter(),
				m.SftpNewFolder(),
				m.SftpNewFile(),
				m.SftpShowHiddenFiles(),
				m.SftpRefresh(),
				m.SftpMore(),
				m.SftpColumnName(),
			}
			rects := make([]ui.Rect, 0, len(labels))
			for _, label := range labels {
				rect, ok := f.tester.Find(label)
				if !ok {
					t.Fatalf("%s %dx%d does not show %q: %v", tag, size[0], size[1], label, f.tester.Texts())
				}
				if rect.X < -0.5 || rect.Y < -0.5 ||
					rect.X+rect.W > width+0.5 || rect.Y+rect.H > height+0.5 ||
					rect.W <= 0 || rect.H <= 0 {
					t.Fatalf("%s %dx%d clips %q: %+v", tag, size[0], size[1], label, rect)
				}
				rects = append(rects, rect)
			}
			for i := range rects {
				for j := i + 1; j < len(rects); j++ {
					if overlaps(rects[i], rects[j]) {
						t.Fatalf("%s %dx%d overlaps %q and %q: %+v, %+v",
							tag, size[0], size[1], labels[i], labels[j], rects[i], rects[j])
					}
				}
			}

			// The panes share the width and keep their own listings: the local
			// row is on the left half, the remote row on the right half.
			left, ok := f.tester.Find("alpha.txt")
			if !ok {
				t.Fatalf("%s %dx%d does not show the local file: %v", tag, size[0], size[1], f.tester.Texts())
			}
			right, ok := f.tester.Find("remote.txt")
			if !ok {
				t.Fatalf("%s %dx%d does not show the remote file: %v", tag, size[0], size[1], f.tester.Texts())
			}
			if left.X+left.W > width/2 {
				t.Fatalf("%s %dx%d: the local pane reaches into the right half: %+v", tag, size[0], size[1], left)
			}
			if right.X < width/2 {
				t.Fatalf("%s %dx%d: the remote pane reaches into the left half: %+v", tag, size[0], size[1], right)
			}
		}
	}
}

func overlaps(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func TestHiddenAndSortControlsAffectOnlyTheirPane(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")
	if f.tester.HasText(".secret") {
		t.Fatal("a dotfile shows by default")
	}
	if err := f.tester.Click(m.SftpShowHiddenFiles()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.state.Tab(transferstate.Left).ShowHidden {
		t.Fatal("the hidden toggle did not reach the left Tab")
	}
	if f.state.Tab(transferstate.Right).ShowHidden {
		t.Fatal("the hidden toggle reached the right Tab")
	}
	if !f.tester.HasText(".secret") {
		t.Fatalf("the hidden file did not render: %v", f.tester.Texts())
	}
	if err := f.tester.Click(m.SftpShowHiddenFiles()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if f.tester.HasText(".secret") {
		t.Fatal("the hidden file stayed visible after the second click")
	}

	// A click on a column header sorts by it; a second click reverses it.
	if err := f.tester.Click(m.SftpColumnSize()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if got := f.state.Tab(transferstate.Left).Sort.Field; got != domainsftp.SortSize {
		t.Fatalf("sort field is %q", got)
	}
	if got := f.state.Tab(transferstate.Right).Sort.Field; got != domainsftp.SortName {
		t.Fatalf("the sort choice leaked to the right pane: %q", got)
	}
	// Directories stay first, then the larger file.
	names := make([]string, 0, 4)
	for _, entry := range f.state.Tab(transferstate.Left).Visible() {
		names = append(names, entry.Name)
	}
	alpha, beta := indexOf(names, "alpha.txt"), indexOf(names, "beta.bin")
	if alpha < 0 || beta < 0 || alpha > beta {
		t.Fatalf("ascending size order is %v", names)
	}

	if err := f.tester.Click(m.SftpColumnSize()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	names = names[:0]
	for _, entry := range f.state.Tab(transferstate.Left).Visible() {
		names = append(names, entry.Name)
	}
	alpha, beta = indexOf(names, "alpha.txt"), indexOf(names, "beta.bin")
	if alpha < 0 || beta < 0 || beta > alpha {
		t.Fatalf("descending size order is %v", names)
	}
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func TestTabsBookmarksAndStarControls(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")

	if err := f.tester.Click(m.SftpAddTab()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if err := f.tester.Click(m.SftpLocalFilesystem()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the second local Tab", func() bool {
		pane := f.state.Pane(transferstate.Left)
		return len(pane.Tabs) == 2 && pane.Tab() != nil && pane.Tab().FS != nil && !pane.Tab().Loading
	})
	// The duplicate keeps the location; the new Tab is the active one.
	if got := f.state.Tab(transferstate.Left).Path; got != f.local {
		t.Fatalf("the new Tab started in %q", got)
	}

	if err := f.tester.Click(m.SftpMore()); err != nil {
		t.Fatal(err)
	}
	if err := f.tester.ChooseMenuItem(m.SftpDuplicateTab()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the duplicated Tab", func() bool {
		return len(f.state.Pane(transferstate.Left).Tabs) == 3 &&
			f.state.Tab(transferstate.Left).FS != nil && !f.state.Tab(transferstate.Left).Loading
	})
	if got := f.state.Tab(transferstate.Left).Path; got != f.local {
		t.Fatalf("the duplicated Tab is at %q", got)
	}

	if err := f.tester.Click(m.SftpMore()); err != nil {
		t.Fatal(err)
	}
	if err := f.tester.ChooseMenuItem(m.SftpCloseTab()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if got := len(f.state.Pane(transferstate.Left).Tabs); got != 2 {
		t.Fatalf("close left %d Tabs", got)
	}

	if err := f.tester.Click(m.SftpBookmarks()); err != nil {
		t.Fatal(err)
	}
	if err := f.tester.ChooseMenuItem(m.SftpBookmark()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.state.Bookmarked(transferstate.Left) {
		t.Fatal("the star did not bookmark the folder")
	}
	if f.state.Bookmarked(transferstate.Right) {
		t.Fatal("the bookmark leaked to the remote pane")
	}
}

func TestBookmarksMenuListsAndRemoves(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")

	if err := f.tester.Click(m.SftpBookmarks()); err != nil {
		t.Fatal(err)
	}
	menu := f.tester.Menu()
	if got := countText(menu, m.SftpBookmark()); got != 1 {
		t.Fatalf("the unbookmarked menu has %d add actions: %v", got, menu)
	}
	if got := countText(menu, m.SftpBookmarkRemove()); got != 0 {
		t.Fatalf("the unbookmarked menu offers a remove action: %v", menu)
	}
	if err := f.tester.ChooseMenuItem(m.SftpBookmark()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.state.Bookmarked(transferstate.Left) {
		t.Fatal("the add action did not bookmark the folder")
	}

	if err := f.tester.Click(m.SftpBookmarks()); err != nil {
		t.Fatal(err)
	}
	menu = f.tester.Menu()
	if got := countText(menu, m.SftpBookmark()); got != 0 {
		t.Fatalf("the bookmarked menu still offers an add action: %v", menu)
	}
	if got := countText(menu, m.SftpBookmarkRemove()); got != 1 {
		t.Fatalf("the bookmarked menu has %d remove actions: %v", got, menu)
	}
	if !containsText(menu, m.SftpBookmarksLocalHint()) {
		t.Fatalf("a local pane does not explain that its bookmarks are unsaved: %v", menu)
	}
	if err := f.tester.ChooseMenuItem(m.SftpBookmarkRemove()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if f.state.Bookmarked(transferstate.Left) {
		t.Fatal("the menu did not remove the bookmark")
	}
}

func countText(items []string, want string) int {
	count := 0
	for _, item := range items {
		if item == want {
			count++
		}
	}
	return count
}

func containsText(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestCopyRunsFromTheRowMenuAndReportsProgress(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")
	if err := f.tester.Click("alpha.txt"); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.state.Tab(transferstate.Left).Selection[filepath.Join(f.local, "alpha.txt")] {
		t.Fatalf("clicking the row selected %v", f.state.Tab(transferstate.Left).Selection)
	}

	if err := f.tester.RightClick("alpha.txt"); err != nil {
		t.Fatal(err)
	}
	if err := f.tester.ChooseMenuItem(m.SftpCopyToOtherPane()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the copy to finish", func() bool {
		return !f.state.Busy() && f.state.Snapshot() != nil
	})
	f.tester.Frame()
	if got := contentOf(t, filepath.Join(f.remote, "alpha.txt")); got != "alpha" {
		t.Fatalf("the copy wrote %q", got)
	}
	if !f.tester.HasText(m.SftpProgress("alpha.txt", 100)) {
		t.Fatalf("the operation bar does not show completion: %v", f.tester.Texts())
	}

	if err := f.tester.Click(m.SftpDismiss()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if f.state.Snapshot() != nil {
		t.Fatal("dismiss left the operation bar in place")
	}
	if !f.tester.HasText(m.SftpOperationDone()) {
		t.Fatalf("the status does not report the result: %v", f.tester.Texts())
	}
}

func TestMetadataOperationShowsCompletionInsteadOfZeroPercent(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")

	if err := f.tester.Click(m.SftpNewFolder()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	f.state.SetDialogDraft("created-by-test")
	if err := f.tester.Click(m.SftpCreate()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the folder operation to finish", func() bool {
		return !f.state.Busy() && f.state.Snapshot() != nil
	})
	f.tester.Frame()

	if f.tester.HasText(m.SftpProgress(m.SftpNewFolder(), 0)) {
		t.Fatalf("a completed metadata operation still shows 0%%: %v", f.tester.Texts())
	}
	if !f.tester.HasText(m.SftpProgressDone(m.SftpNewFolder())) {
		t.Fatalf("the completed operation has no completion label: %v", f.tester.Texts())
	}
}

func TestEveryMetadataOperationKindShowsCompletion(t *testing.T) {
	m := locales.In("en")
	for _, kind := range []domainsftp.OperationKind{
		domainsftp.OpMkdir,
		domainsftp.OpCreateFile,
		domainsftp.OpRename,
		domainsftp.OpChmod,
	} {
		finished := &transferstate.Operation{Kind: kind, Current: "thing"}
		if got, want := operationLabel(m, finished), m.SftpProgressDone("thing"); got != want {
			t.Errorf("%s label = %q, want %q", kind, got, want)
		}
		running := &transferstate.Operation{Kind: kind, Current: "thing", Running: true}
		if got := operationLabel(m, running); got == m.SftpProgressDone("thing") {
			t.Errorf("a running %s already looks finished: %q", kind, got)
		}
	}
}

func TestDoubleClickOpensAnUnselectedFolder(t *testing.T) {
	f := newViewFixture(t, 1100, 720, "en")
	rect, ok := f.tester.Find("folder")
	if !ok {
		t.Fatalf("the folder row is missing: %v", f.tester.Texts())
	}
	if f.state.Tab(transferstate.Left).Selection[filepath.Join(f.local, "folder")] {
		t.Fatal("the folder was selected before the double-click")
	}
	// A frame between the clicks is what a real double-click does; the second
	// click then arrives as a double-click with a single click count.
	f.tester.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	f.tester.Frame()
	f.tester.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	frameUntil(t, f.tester, "the double-click to open the folder", func() bool {
		tab := f.state.Tab(transferstate.Left)
		return tab.Path == filepath.Join(f.local, "folder") && !tab.Loading
	})
}

func TestDeleteAsksBeforeRemoving(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 1100, 720, "en")
	target := filepath.Join(f.local, "alpha.txt")
	if err := f.tester.Click("alpha.txt"); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()

	if err := f.tester.RightClick("alpha.txt"); err != nil {
		t.Fatal(err)
	}
	if err := f.tester.ChooseMenuItem(m.SftpDelete()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if dialog := f.state.Dialog(); dialog == nil || dialog.Kind != transferstate.DialogDelete {
		t.Fatalf("the row menu did not ask for confirmation: %+v", dialog)
	}
	if !f.tester.HasText(m.SftpDeleteTitle()) {
		t.Fatalf("the confirmation does not explain the deletion: %v", f.tester.Texts())
	}
	if !exists(target) {
		t.Fatal("the file was removed before confirmation")
	}

	if err := f.tester.Click(m.SftpCancel()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if f.state.Dialog() != nil {
		t.Fatal("cancel left the confirmation open")
	}
	if !exists(target) {
		t.Fatal("cancel removed the file")
	}
}

func TestNewFolderValidationIsShownWithoutCreating(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 720, 480, "en")
	if err := f.tester.Click(m.SftpNewFolder()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.tester.HasText(m.SftpNewFolderTitle()) {
		t.Fatalf("the new-folder prompt did not open: %v", f.tester.Texts())
	}
	f.state.SetDialogDraft("bad/name")
	if err := f.tester.Click(m.SftpCreate()); err != nil {
		t.Fatal(err)
	}
	f.tester.Frame()
	if !f.tester.HasText(m.SftpInvalidName()) {
		t.Fatalf("the invalid name is not explained: %v", f.tester.Texts())
	}
	if f.state.Dialog() == nil {
		t.Fatal("the prompt closed on an invalid name")
	}
	if exists(filepath.Join(f.local, "bad", "name")) {
		t.Fatal("an invalid name created a directory")
	}
}

func TestHostPickerListsAndOpensAHost(t *testing.T) {
	m := locales.In("en")
	f := newViewFixture(t, 720, 480, "en")
	if err := f.tester.Click(m.SftpAddTab()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the host list", func() bool {
		return f.state.HostTargets() != nil || !f.state.HostsLoading()
	})
	f.tester.Frame()
	if !f.tester.HasText("me@test.example:22") {
		t.Fatalf("the picker does not show the target: %v", f.tester.Texts())
	}
	if err := f.tester.Click("me@test.example:22"); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, f.tester, "the chosen Host to open", func() bool {
		tab := f.state.Tab(transferstate.Left)
		return tab != nil && tab.Kind == transferstate.KindRemote && tab.FS != nil && !tab.Loading
	})
	if f.state.Dialog() != nil {
		t.Fatal("choosing a Host left the picker open")
	}
}

func TestEmptyRightPaneOffersBothStarts(t *testing.T) {
	m := locales.In("en")
	local := t.TempDir()
	writeFile(t, filepath.Join(local, "alpha.txt"), "alpha")
	service := newViewService(local, local)
	state := transferstate.NewState(service)
	t.Cleanup(state.Shutdown)
	tester := ui.NewTester(View(state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	frameUntil(t, tester, "the local listing", func() bool {
		tab := state.Tab(transferstate.Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	if !tester.HasText(m.SftpSelectHostTitle()) {
		t.Fatalf("the empty pane does not say so: %v", tester.Texts())
	}
	if !tester.HasText(m.SftpSelectHost()) {
		t.Fatalf("the empty pane does not offer its starting actions: %v", tester.Texts())
	}
}

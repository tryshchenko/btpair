package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/engine"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/rsync"
	"github.com/tryshchenko/btpair/internal/steps"
	"github.com/tryshchenko/btpair/internal/volume"
)

// ── helpers ─────────────────────────────────────────────────────────────

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1_500_000: "1.5 MB", 2_000_000_000_000: "2.0 TB", -5: "0 B"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
	if clock(65*time.Second) != "1:05" || clock(3725*time.Second) != "1:02:05" {
		t.Error("clock formatting")
	}
}

func TestTruncMidKeepsBothEnds(t *testing.T) {
	s := "Photos/2024/Holiday in the mountains/IMG_2231.HEIC"
	for _, w := range []int{10, 20, 30} {
		got := truncMid(s, w)
		if ansi.StringWidth(got) > w {
			t.Errorf("width %d: %q too wide", w, got)
		}
		if !strings.HasSuffix(got, s[len(s)-3:]) || !strings.HasPrefix(got, "P") {
			t.Errorf("width %d: %q lost an end", w, got)
		}
	}
	if truncMid("short", 10) != "short" {
		t.Error("short string changed")
	}
}

func TestBarsHaveExactWidth(t *testing.T) {
	for _, w := range []int{1, 7, 20, 63} {
		for _, f := range []float64{-1, 0, 0.001, 0.333, 0.5, 0.999, 1, 1.5} {
			if got := ansi.StringWidth(gradientBar(w, f, 17)); got != w {
				t.Errorf("gradientBar(%d, %v) width %d", w, f, got)
			}
		}
		for _, c := range [][2]float64{{0, 0}, {0.2, 0.1}, {0.9, 0.5}, {1, 1}, {0.001, 0.001}} {
			if got := ansi.StringWidth(usageBar(w, c[0], c[1])); got != w {
				t.Errorf("usageBar(%d, %v) width %d", w, c, got)
			}
		}
	}
	if gradientBar(0, 0.5, 0) != "" || usageBar(0, 0.5, 0) != "" {
		t.Error("zero-width bars must be empty")
	}
}

func TestSpreadFitsWidth(t *testing.T) {
	for _, w := range []int{10, 30, 80} {
		got := spread("left side text", "right", w)
		if ansi.StringWidth(got) > w {
			t.Errorf("spread at %d: width %d", w, ansi.StringWidth(got))
		}
	}
}

// ── app fixture ─────────────────────────────────────────────────────────

type fixture struct {
	app  *App
	root string
}

// newApp builds an App over dev drives: a paired main/replica with some
// history, one pair with its replica unplugged, and two blank drives.
func newApp(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // keep step selections out of $HOME
	root := t.TempDir()
	for _, d := range []string{"WORK-MAIN", "WORK-REPLICA", "MEDIA-MAIN", "MEDIA-REPLICA", "BLANK-A", "BLANK-B"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "WORK-MAIN", "file.txt"), []byte("hello"), 0o644)

	vols := scanVols(t, root)
	byName := map[string]*volume.Volume{}
	for _, v := range vols {
		byName[v.Name] = v
	}
	must(t, engine.InitPair(byName["WORK-MAIN"], byName["WORK-REPLICA"], "work", "Work SSDs"))
	must(t, engine.InitPair(byName["MEDIA-MAIN"], byName["MEDIA-REPLICA"], "media", "Music & films"))
	os.Rename(filepath.Join(root, "MEDIA-REPLICA"), filepath.Join(t.TempDir(), "unplugged"))

	// Some history on the work pair.
	now := time.Now()
	for i, st := range []meta.Status{meta.StatusOK, meta.StatusPartial, meta.StatusFailed} {
		rec := meta.RunRecord{State: meta.State{
			Status: st, RunID: meta.NewRunID(now.Add(time.Duration(i) * time.Minute)), StartedAt: now, FinishedAt: now.Add(time.Minute),
			Message: strings.Repeat("a long message ", 20),
			Steps:   []meta.StepResult{{ID: "s", Title: "Step", Status: meta.StatusOK}},
			Pending: []meta.PendingItem{{Kind: "file", Path: strings.Repeat("deep/", 30) + "file", Reason: "rsync error"}},
		}, Errors: []string{strings.Repeat("error text ", 30)}, Log: []string{"line"}}
		for _, n := range []string{"WORK-MAIN", "WORK-REPLICA"} {
			meta.SaveRun(filepath.Join(root, n), rec)
			meta.SaveState(filepath.Join(root, n), rec.State)
		}
	}

	cfg := &config.Config{Pairs: map[string]config.PairConfig{}, Steps: map[string]config.StepConfig{
		"quick": {Type: "shell", Title: "A step with a rather long title that must truncate", Description: strings.Repeat("description ", 20), Run: "true", Default: true},
		"skip":  {Type: "shell", Title: "Up to date step", Run: "true", SkipIf: "echo fresh; exit 0"},
	}}
	a := New(Options{DevRoot: root, Config: cfg})
	f := &fixture{app: a, root: root}
	f.resize(120, 40)
	f.send(a.scan()())
	f.send(a.pollSpace(a.vols...)())
	// Realistic sizes give realistic label widths ("812.3 GB used"): tiny
	// test folders once hid a layout bug.
	for i, v := range a.vols {
		a.spaces[v.UUID] = volume.Space{Total: 2_000_000_000_000, Free: uint64(1_187_654_321_000 - i*99_000_000_000)}
	}
	return f
}

func scanVols(t *testing.T, root string) []*volume.Volume {
	t.Helper()
	a := New(Options{DevRoot: root})
	msg := a.scan()().(scanMsg)
	must(t, msg.err)
	return msg.vols
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) resize(w, h int) { f.app.Update(tea.WindowSizeMsg{Width: w, Height: h}) }

// send delivers a message and runs any immediate follow-up commands that are
// safe to run synchronously (plans, scans, space polls), but not ticks.
func (f *fixture) send(msg tea.Msg) {
	_, cmd := f.app.Update(msg)
	f.drain(cmd, 0)
}

func (f *fixture) drain(cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 3 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch m := msg.(type) {
		case tea.BatchMsg:
			for _, c := range m {
				f.drain(c, depth+1)
			}
		case tickMsg, nil, eventsMsg, runClosedMsg:
			// ticks are timers; run events are driven explicitly by tests
		default:
			_, next := f.app.Update(m)
			f.drain(next, depth+1)
		}
	case <-time.After(300 * time.Millisecond):
		// a timer (cursor blink) or a blocking wait for run events; ignore
	}
}

func (f *fixture) key(k string) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	f.send(msg)
}

func (f *fixture) view() string { return ansi.Strip(f.app.View()) }

func (f *fixture) workPair() *engine.Pair { return f.app.pairByID("work") }

// assertFits checks that every rendered line fits the terminal.
func assertFits(t *testing.T, name string, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("%s @%dx%d: %d lines, terminal has %d", name, w, h, len(lines), h)
	}
	// Report the widest line only: one overflow pads every other line.
	worst, worstW := "", 0
	for _, l := range lines {
		if lw := ansi.StringWidth(strings.TrimRight(l, " ")); lw > worstW {
			worst, worstW = l, lw
		}
	}
	if worstW > w {
		t.Errorf("%s @%dx%d: content is %d wide: %q", name, w, h, worstW, strings.TrimRight(worst, " "))
	}
}

// ── run screen fixture ──────────────────────────────────────────────────

// runScreen puts the app on the run screen with a synthetic mid-sync state.
func (f *fixture) runScreen(t *testing.T, finished bool) {
	t.Helper()
	p := f.workPair()
	list, err := steps.ForPair(f.app.cfg, "work")
	must(t, err)
	m := newRun(engine.Request{Pair: p, Config: f.app.cfg, Steps: list})
	m.started = time.Now().Add(-90 * time.Second)
	m.cancel = func() {}
	long := strings.Repeat("very/long/path/", 20) + "IMG_0001.HEIC"
	evs := []engine.Event{
		engine.PhaseStarted{ID: engine.PhasePreflight},
		engine.PhaseFinished{ID: engine.PhasePreflight, Status: engine.StatusDone, Note: "WORK-MAIN → WORK-REPLICA"},
		engine.PhaseStarted{ID: "step:quick"},
		engine.PhaseFinished{ID: "step:quick", Status: engine.StatusFailed, Note: strings.Repeat("failure reason ", 10)},
		engine.PhaseFinished{ID: "step:skip", Status: engine.StatusSkipped, Note: "fresh"},
		engine.PhaseFinished{ID: engine.PhaseProtect, Status: engine.StatusSkipped, Note: "dev drive"},
		engine.PhaseFinished{ID: engine.PhaseAnalyze, Status: engine.StatusDone, Note: "120 to copy · 18 to delete · 186.5 MB"},
		engine.Planned{Plan: &rsync.Plan{FilesToCopy: 1200, FilesToDelete: 18, BytesToCopy: 9_000_000_000, FilesTotal: 50_000, BytesTotal: 900_000_000_000}},
		engine.PhaseStarted{ID: engine.PhaseSync},
		engine.Progress{ID: engine.PhaseSync, Done: 3_000_000_000, Total: 9_000_000_000, Unit: "bytes", Speed: 150_000_000, Current: long, CurrentSize: 34_000_000, Files: 400, FilesTotal: 1200, Deleted: 18},
		engine.Log{At: time.Now(), Kind: engine.KindCopy, Text: long},
		engine.Log{At: time.Now(), Kind: engine.KindDelete, Text: long},
		engine.Log{At: time.Now(), Level: engine.LevelWarn, Text: strings.Repeat("warning ", 30)},
		engine.Log{At: time.Now(), Level: engine.LevelError, Text: strings.Repeat("rsync error ", 30)},
	}
	if finished {
		evs = append(evs,
			engine.PhaseFinished{ID: engine.PhaseSync, Status: engine.StatusWarn, Note: "1,200 copied · 18 deleted · 9.0 GB · 1 errors"},
			engine.Finished{Record: meta.RunRecord{State: meta.State{Status: meta.StatusPartial, FinishedAt: time.Now(),
				Pending: []meta.PendingItem{{Kind: "step", Path: "quick"}},
				Steps:   []meta.StepResult{{ID: "quick", Status: meta.StatusFailed}, {ID: "skip", Skipped: true}}}}},
		)
	}
	f.app.update(m, evs)
}

func (a *App) update(m *runModel, evs []engine.Event) {
	a.run = m
	a.screen = scrRun
	for _, ev := range evs {
		m.apply(ev)
	}
}

// ── tests ───────────────────────────────────────────────────────────────

var sizes = [][2]int{{80, 24}, {100, 30}, {120, 40}, {160, 50}, {200, 60}}

func TestEveryScreenFitsTheTerminal(t *testing.T) {
	screens := map[string]func(*testing.T, *fixture){
		"dashboard": func(*testing.T, *fixture) {},
		"picker": func(t *testing.T, f *fixture) {
			f.key("enter")
			if f.app.screen != scrPicker {
				t.Fatalf("enter did not open the picker (flash: %q)", f.app.flash)
			}
		},
		"run":      func(t *testing.T, f *fixture) { f.runScreen(t, false) },
		"run done": func(t *testing.T, f *fixture) { f.runScreen(t, true) },
		"run problems tab": func(t *testing.T, f *fixture) {
			f.runScreen(t, true)
			f.key("tab")
		},
		"run cancel prompt": func(t *testing.T, f *fixture) {
			f.runScreen(t, false)
			f.key("ctrl+c")
		},
		"history": func(t *testing.T, f *fixture) {
			f.key("h")
			if f.app.screen != scrHistory {
				t.Fatal("h did not open history")
			}
		},
		"new pair": func(t *testing.T, f *fixture) { f.key("n") },
		"new pair confirm": func(t *testing.T, f *fixture) {
			f.key("n")
			f.key("enter")
			f.key("enter")
			f.key("x")
			f.key("enter")
			f.key("enter")
		},
	}
	for name, open := range screens {
		for _, sz := range sizes {
			f := newApp(t)
			f.resize(sz[0], sz[1])
			open(t, f)
			assertFits(t, name, f.view(), sz[0], sz[1])
		}
	}
}

var barRun = regexp.MustCompile(`[━╍]+`)

// Regression: the replica's bar shrank when it carried a "+X incoming" note.
func TestDriveBarsAlign(t *testing.T) {
	for _, w := range []int{90, 100, 112, 120, 140, 200} {
		f := newApp(t)
		f.resize(w, 40)
		f.runScreen(t, false)
		var bars []int
		for _, l := range strings.Split(f.view(), "\n") {
			if strings.Contains(l, "WORK-MAIN ") || strings.Contains(l, "WORK-REPLICA ") {
				if strings.Contains(l, "→") {
					continue // the preflight note mentions both names
				}
				right := l[strings.LastIndex(l, "WORK-"):]
				if m := barRun.FindString(right); m != "" {
					bars = append(bars, len([]rune(m)))
				} else {
					t.Errorf("@%d: drive row without a bar: %q", w, l)
				}
			}
		}
		if len(bars) != 2 || bars[0] != bars[1] || bars[0] < 8 {
			t.Errorf("@%d: bar widths %v, want two equal bars of at least 8 cells", w, bars)
		}
	}
}

func TestDashboardShowsPairsAndDrives(t *testing.T) {
	f := newApp(t)
	v := f.view()
	for _, want := range []string{"work", "Work SSDs", "WORK-MAIN", "WORK-REPLICA", "media", "replica not connected", "BLANK-A", "UNPAIRED DRIVES", "free", "failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// Ready pairs are listed before incomplete ones.
	if strings.Index(v, "Work SSDs") > strings.Index(v, "Music & films") {
		t.Error("ready pair not listed first")
	}
}

func TestDashboardRefusesIncompletePair(t *testing.T) {
	f := newApp(t)
	f.key("down") // media: replica unplugged
	f.key("enter")
	if f.app.screen != scrDash || !strings.Contains(f.app.flash, "connect the replica") {
		t.Fatalf("screen=%v flash=%q", f.app.screen, f.app.flash)
	}
}

func TestPickerSelectionAndPlans(t *testing.T) {
	f := newApp(t)
	f.key("enter")
	pk := f.app.picker
	if len(pk.items) != 2 {
		t.Fatalf("%d steps offered", len(pk.items))
	}
	byID := map[string]*pickItem{}
	for i := range pk.items {
		byID[pk.items[i].step.ID()] = &pk.items[i]
	}
	if !byID["quick"].on || byID["skip"].on {
		t.Error("defaults not applied")
	}
	if p := byID["skip"].plan; p == nil || !p.UpToDate || p.Summary != "fresh" {
		t.Errorf("skip plan = %+v", p)
	}
	if !strings.Contains(f.view(), "fresh") {
		t.Error("plan summary not shown")
	}

	f.key("space") // toggle first row
	f.key("a")     // all on
	for _, it := range pk.items {
		if !it.on {
			t.Error("'a' did not select all")
		}
	}
	f.key("u") // only steps with work
	if byID["skip"].on || !byID["quick"].on {
		t.Error("'u' should select only steps with work to do")
	}
	// Options rows: dry run, checksum, allow deletions.
	for i := 0; i < len(pk.items); i++ {
		f.key("down")
	}
	f.key("space")
	f.key("down")
	f.key("space")
	f.key("down")
	f.key("space")
	if !pk.dryRun || !pk.checksum || !pk.allowDel {
		t.Errorf("options: dry=%v checksum=%v del=%v", pk.dryRun, pk.checksum, pk.allowDel)
	}
	f.key("esc")
	if f.app.screen != scrDash {
		t.Error("esc did not go back")
	}
}

func TestPickerRemembersSelection(t *testing.T) {
	f := newApp(t)
	f.key("enter")
	f.key("a") // quick+skip on (quick was on, so 'a' turns all on)
	chosen := map[string]bool{}
	for _, it := range f.app.picker.items {
		chosen[it.step.ID()] = it.on
	}
	saveSelection("work", chosen)
	f.key("esc")
	f.key("enter")
	for _, it := range f.app.picker.items {
		if it.on != chosen[it.step.ID()] {
			t.Errorf("%s selection not remembered", it.step.ID())
		}
	}
}

func TestPickerStartsRunWithChoices(t *testing.T) {
	f := newApp(t)
	f.key("enter")
	pk := f.app.picker
	for i := range pk.items {
		pk.items[i].on = pk.items[i].step.ID() == "skip"
	}
	pk.dryRun, pk.checksum = true, true
	f.key("enter")
	if f.app.screen != scrRun {
		t.Fatal("enter did not start the run")
	}
	req := f.app.run.req
	if !req.DryRun || !req.Checksum || len(req.Steps) != 1 || req.Steps[0].ID() != "skip" {
		t.Fatalf("request = dry:%v checksum:%v steps:%d", req.DryRun, req.Checksum, len(req.Steps))
	}
	// Let the (dry) run finish so the goroutine does not outlive the test.
	for ev := range f.app.run.ch {
		f.app.run.apply(ev)
	}
	if !f.app.run.done() || f.app.run.record.Status != meta.StatusDryRun {
		t.Fatalf("dry run ended with %+v", f.app.run.record)
	}
}

func TestCancelNeedsConfirmation(t *testing.T) {
	f := newApp(t)
	f.runScreen(t, false)
	cancelled := false
	f.app.run.cancel = func() { cancelled = true }

	f.key("ctrl+c")
	if !f.app.run.confirmCancel || cancelled {
		t.Fatal("ctrl+c must ask first")
	}
	if !strings.Contains(f.view(), "Cancel the run?") {
		t.Error("prompt not shown")
	}
	f.key("n")
	if f.app.run.confirmCancel || cancelled {
		t.Fatal("'n' must keep the run going")
	}
	f.key("ctrl+c")
	f.key("y")
	if !cancelled || !f.app.run.cancelling {
		t.Fatal("'y' must cancel")
	}
}

func TestRunScreenContent(t *testing.T) {
	f := newApp(t)
	f.resize(140, 45)
	f.runScreen(t, false)
	v := f.view()
	for _, want := range []string{"33.3%", "3.0 GB of 9.0 GB", "/s", "ETA", "files 400 / 1,200", "deleted 18", "IMG_0001.HEIC", "DRIVES", "+6.0 GB", "Problems 2"} {
		if !strings.Contains(v, want) {
			t.Errorf("run screen missing %q", want)
		}
	}
	f.key("tab")
	if v := f.view(); !strings.Contains(v, "rsync error") || strings.Contains(v, "+ very/long") {
		t.Error("problems tab should show only warnings and errors")
	}
}

func TestFinishedRunReturnsToDashboard(t *testing.T) {
	f := newApp(t)
	f.runScreen(t, true)
	if !strings.Contains(f.view(), "COMPLETED WITH ISSUES") {
		t.Error("result banner missing")
	}
	f.key("enter")
	if f.app.screen != scrDash {
		t.Fatal("enter after a finished run should return to the dashboard")
	}
}

func TestLogIsCappedAndScrolls(t *testing.T) {
	f := newApp(t)
	f.runScreen(t, false)
	m := f.app.run
	for i := 0; i < maxLogLines+500; i++ {
		m.apply(engine.Log{At: time.Now(), Text: "line"})
	}
	if len(m.logs) > maxLogLines {
		t.Fatalf("log grew to %d lines", len(m.logs))
	}
	f.key("up")
	if m.follow || !strings.Contains(f.view(), "G to follow") {
		t.Error("scrolling up should stop following")
	}
	f.key("G")
	if !m.follow {
		t.Error("G should resume following")
	}
}

func TestNewPairWizardCreatesPair(t *testing.T) {
	f := newApp(t)
	f.key("n")
	f.key("enter") // main: BLANK-A
	f.key("enter") // replica: BLANK-B
	f.key("enter") // empty name is rejected
	if f.app.np.err == "" {
		t.Error("empty name accepted")
	}
	for _, r := range "spare" {
		f.key(string(r))
	}
	f.key("enter")
	f.key("enter") // skip label
	f.key("enter") // confirm
	if f.app.screen != scrDash {
		t.Fatalf("wizard did not finish (err %q)", f.app.np.err)
	}
	if p := f.app.pairByID("spare"); p == nil || p.Main.Name != "BLANK-A" || p.Replica.Name != "BLANK-B" {
		t.Fatalf("pair not created: %+v", p)
	}
}

func TestHistoryListsRunsNewestFirst(t *testing.T) {
	f := newApp(t)
	f.key("h")
	h := f.app.hist
	if len(h.runs) != 3 || h.runs[0].Status != meta.StatusFailed {
		t.Fatalf("runs = %d, first %s", len(h.runs), h.runs[0].Status)
	}
	v := f.view()
	for _, want := range []string{"failed", "partial", "synced", "PENDING", "ERRORS"} {
		if !strings.Contains(v, want) {
			t.Errorf("history missing %q", want)
		}
	}
	f.key("down")
	if h.cursor != 1 {
		t.Error("cursor did not move")
	}
}

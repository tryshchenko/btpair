package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tryshchenko/backup-tool/internal/engine"
	"github.com/tryshchenko/backup-tool/internal/meta"
	"github.com/tryshchenko/backup-tool/internal/rsync"
)

type phaseState struct {
	engine.Phase
	status     engine.PhaseStatus
	note       string
	start, end time.Time
	prog       *engine.Progress
}

type logLine struct {
	at    time.Time
	level engine.Level
	kind  engine.LogKind
	text  string
}

type runModel struct {
	req     engine.Request
	phases  []*phaseState
	byID    map[string]*phaseState
	active  *phaseState
	ch      chan engine.Event
	cancel  context.CancelFunc
	started time.Time

	plan   *rsync.Plan
	record *meta.RunRecord

	logs, errs []logLine
	warnCount  int
	tab        int // 0 activity, 1 problems
	scroll     int // lines scrolled up from the bottom
	follow     bool

	confirmCancel bool
	cancelling    bool
}

type eventsMsg []engine.Event
type runClosedMsg struct{}

const maxLogLines = 5000

func newRun(req engine.Request) *runModel {
	m := &runModel{req: req, byID: map[string]*phaseState{}, follow: true}
	for _, p := range engine.Phases(req) {
		ps := &phaseState{Phase: p}
		m.phases = append(m.phases, ps)
		m.byID[p.ID] = ps
	}
	return m
}

func (m *runModel) start() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.ch = make(chan engine.Event, 4096)
	m.started = time.Now()
	go engine.Run(ctx, m.req, m.ch)
	return m.wait()
}

// wait collects every event that is ready, so a burst of thousands of
// rsync lines costs one render rather than thousands.
func (m *runModel) wait() tea.Cmd {
	ch := m.ch
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return runClosedMsg{}
		}
		batch := eventsMsg{ev}
		for len(batch) < 2000 {
			select {
			case ev, ok := <-ch:
				if !ok {
					return batch
				}
				batch = append(batch, ev)
			default:
				return batch
			}
		}
		return batch
	}
}

func (m *runModel) done() bool { return m.record != nil }

func (m *runModel) update(a *App, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case eventsMsg:
		for _, ev := range msg {
			m.apply(ev)
		}
		if m.done() {
			// Reload metadata so the dashboard reflects this run.
			if m.req.Pair.Main != nil {
				m.req.Pair.Main.LoadMeta()
			}
			if m.req.Pair.Replica != nil {
				m.req.Pair.Replica.LoadMeta()
			}
			return a.pollSpace(m.req.Pair.Main, m.req.Pair.Replica)
		}
		return m.wait()
	case runClosedMsg:
		return nil
	case tea.KeyMsg:
		return m.key(a, msg)
	}
	return nil
}

func (m *runModel) key(a *App, k tea.KeyMsg) tea.Cmd {
	if m.confirmCancel {
		switch k.String() {
		case "y", "Y", "ctrl+c":
			m.confirmCancel = false
			m.cancelling = true
			m.cancel()
		case "n", "N", "esc":
			m.confirmCancel = false
		}
		return nil
	}
	switch k.String() {
	case "ctrl+c":
		if m.done() {
			return tea.Quit
		}
		if !m.cancelling {
			m.confirmCancel = true
		}
	case "q":
		if m.done() {
			return tea.Quit
		}
	case "esc", "enter":
		if m.done() {
			a.screen = scrDash
			a.scanned = false
			return a.scan()
		}
	case "tab", "e":
		m.tab = 1 - m.tab
		m.scroll, m.follow = 0, true
	case "up", "k":
		m.scroll++
		m.follow = false
	case "down", "j":
		if m.scroll > 0 {
			m.scroll--
		}
		m.follow = m.scroll == 0
	case "pgup":
		m.scroll += 10
		m.follow = false
	case "pgdown":
		m.scroll = max(0, m.scroll-10)
		m.follow = m.scroll == 0
	case "G", "end":
		m.scroll, m.follow = 0, true
	}
	return nil
}

func (m *runModel) apply(ev engine.Event) {
	switch e := ev.(type) {
	case engine.PhaseStarted:
		if ps := m.byID[e.ID]; ps != nil {
			ps.status = engine.StatusRunning
			ps.start = time.Now()
			m.active = ps
		}
	case engine.PhaseFinished:
		if ps := m.byID[e.ID]; ps != nil {
			ps.status, ps.note = e.Status, e.Note
			ps.end = time.Now()
			if ps.start.IsZero() {
				ps.start = ps.end
			}
		}
	case engine.Progress:
		if ps := m.byID[e.ID]; ps != nil {
			p := e
			ps.prog = &p
		}
	case engine.Planned:
		m.plan = e.Plan
	case engine.Log:
		l := logLine{e.At, e.Level, e.Kind, e.Text}
		m.logs = appendCapped(m.logs, l)
		if e.Level != engine.LevelInfo {
			m.errs = appendCapped(m.errs, l)
			if e.Level == engine.LevelWarn {
				m.warnCount++
			}
		}
	case engine.Finished:
		r := e.Record
		m.record = &r
		m.active = nil
		// phases that never ran are shown as skipped
		for _, ps := range m.phases {
			if ps.status == engine.StatusPending {
				ps.status = engine.StatusSkipped
				if ps.note == "" {
					ps.note = "not reached"
				}
			}
		}
	}
}

func appendCapped(s []logLine, l logLine) []logLine {
	if len(s) >= maxLogLines {
		s = s[len(s)-maxLogLines+1:]
	}
	return append(s, l)
}

// ── view ────────────────────────────────────────────────────────────────

func (m *runModel) view(a *App) string {
	w := a.contentWidth()
	elapsed := time.Since(m.started)
	if m.record != nil {
		elapsed = m.record.FinishedAt.Sub(m.started)
	}

	right := sMuted.Render("⏱ ") + sSub.Render(clock(elapsed))
	if m.req.DryRun {
		right = badge("DRY RUN", cAccent2) + "  " + right
	}
	top := header(w, []string{m.req.Pair.ID, map[bool]string{true: "dry run", false: "backup"}[m.req.DryRun]}, right)

	var sections []string
	sections = append(sections, top, "")
	if m.record != nil {
		sections = append(sections, m.resultBanner(w), "")
	}

	// Phases on the left, live detail on the right; stacked when narrow.
	phasesW := 40
	var mid string
	if w >= 100 {
		detailW := w - phasesW - 3
		left := m.viewPhases(a, phasesW)
		rightCol := m.viewDetail(a, detailW)
		hgt := max(lipgloss.Height(left), lipgloss.Height(rightCol))
		sep := strings.TrimRight(strings.Repeat(sFaint.Render("│")+"\n", hgt), "\n")
		mid = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(phasesW).Render(left), " ", sep, " ", rightCol)
	} else {
		mid = m.viewPhases(a, w) + "\n\n" + m.viewDetail(a, w)
	}
	sections = append(sections, mid, "", rule(w))

	body := strings.Join(sections, "\n")
	footer := a.footer(m.hints())
	if m.confirmCancel {
		keys := sKey.Render("y") + sMuted.Render(" cancel  ") + sKey.Render("n") + sMuted.Render(" keep going")
		q := sWarn.Render("Cancel the run?") + "  "
		explain := sSub.Render("The transfer stops and the run is recorded as cancelled.") + "   "
		if lipgloss.Width(q+explain+keys) > w {
			explain = ""
		}
		footer = rule(w) + "\n" + truncEnd(q+explain+keys, w)
	}

	// The log takes whatever height remains.
	logH := a.h - 1 - lipgloss.Height(body) - lipgloss.Height(footer) - 2
	logView := m.viewLog(w, max(3, logH))
	return body + "\n" + logView + "\n" + footer
}

func (m *runModel) hints() string {
	switch {
	case m.done():
		return helpBar("enter", "back to pairs", "tab", "activity/problems", "↑↓", "scroll", "q", "quit")
	case m.cancelling:
		return spinner(0) + sWarn.Render(" cancelling — cleaning up…")
	}
	return helpBar("tab", "activity/problems", "↑↓", "scroll", "G", "follow", "ctrl+c", "cancel")
}

func (m *runModel) resultBanner(w int) string {
	r := m.record
	var b, msg string
	switch r.Status {
	case meta.StatusOK:
		b, msg = badge("✓ COMPLETE", cOK), "The replica is an exact mirror of the main drive."
	case meta.StatusDryRun:
		b, msg = badge("○ DRY RUN COMPLETE", cAccent2), "Nothing was written. Review the plan below."
	case meta.StatusPartial:
		b, msg = badge("! COMPLETED WITH ISSUES", cWarn), fmt.Sprintf("%d item(s) are pending and will be retried next run.", len(r.Pending))
	case meta.StatusCancelled:
		b, msg = badge("◼ CANCELLED", cWarn), "Stopped before finishing. The next run picks up where this left off."
	default:
		b, msg = badge("✗ FAILED", cErr), r.Message
	}
	return b + "  " + sSub.Render(truncEnd(msg, w-lipgloss.Width(b)-2))
}

func (m *runModel) viewPhases(a *App, w int) string {
	lines := []string{label("Phases")}
	for _, ps := range m.phases {
		var icon string
		switch ps.status {
		case engine.StatusPending:
			icon = sFaint.Render("○")
		case engine.StatusRunning:
			icon = spinner(a.tick)
		case engine.StatusDone:
			icon = sOK.Render("✓")
		case engine.StatusSkipped:
			icon = sMuted.Render("–")
		case engine.StatusWarn:
			icon = sWarn.Render("!")
		case engine.StatusFailed:
			icon = sErr.Render("✗")
		}
		title := ps.Title
		if ps.Kind == engine.KindStep {
			title = "+ " + title
		}
		var tstyle = sText
		switch ps.status {
		case engine.StatusPending, engine.StatusSkipped:
			tstyle = sMuted
		case engine.StatusRunning:
			tstyle = sBold
		}
		dur := ""
		switch {
		case ps.status == engine.StatusRunning:
			dur = sAccent.Render(clock(time.Since(ps.start)))
		case !ps.end.IsZero() && ps.status != engine.StatusSkipped:
			dur = sMuted.Render(shortDur(ps.end.Sub(ps.start)))
		}
		lines = append(lines, spread(icon+" "+tstyle.Render(truncEnd(title, w-12)), dur, w))
		if ps.note != "" && ps.status != engine.StatusRunning {
			st := sMuted
			switch ps.status {
			case engine.StatusFailed:
				st = sErr
			case engine.StatusWarn:
				st = sWarn
			}
			lines = append(lines, "  "+st.Render(truncEnd(ps.note, w-2)))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *runModel) viewDetail(a *App, w int) string {
	var out []string
	ps := m.active
	switch {
	case m.record != nil:
		out = append(out, m.viewSummary(w)...)
	case ps == nil:
		out = append(out, label("Starting"), spinner(a.tick)+sSub.Render(" preparing…"))
	default:
		out = append(out, label(ps.Title))
		out = append(out, m.viewProgress(a, ps, w)...)
	}

	out = append(out, "", label("Drives"))
	pair := m.req.Pair
	var incoming int64
	if m.plan != nil && !m.req.DryRun && m.record == nil {
		incoming = m.plan.BytesToCopy
		if sp := m.byID[engine.PhaseSync].prog; sp != nil {
			incoming -= sp.Done
		}
	} else if m.plan != nil && m.req.DryRun {
		incoming = m.plan.BytesToCopy
	}
	out = append(out, a.driveRow("main", pair.Main, 0, w))
	out = append(out, a.driveRow("replica", pair.Replica, max(0, incoming), w))
	return strings.Join(out, "\n")
}

func (m *runModel) viewProgress(a *App, ps *phaseState, w int) []string {
	p := ps.prog
	barW := w - 7
	if p == nil || p.Total <= 0 {
		cur := "working…"
		if p != nil && p.Current != "" {
			cur = p.Current
		}
		return []string{
			gradientBar(barW, -1, a.tick),
			sSub.Render(truncMid(cur, w)),
			"",
		}
	}
	frac := float64(p.Done) / float64(p.Total)
	pct := lipgloss.NewStyle().Foreground(cText).Bold(true).Render(fmt.Sprintf("%5.1f%%", frac*100))
	lines := []string{gradientBar(barW, frac, a.tick) + " " + pct}

	if p.Unit == "bytes" {
		stats := sText.Render(humanBytes(p.Done)) + sMuted.Render(" of ") + sText.Render(humanBytes(p.Total))
		if p.Speed > 0 {
			eta := time.Duration(float64(p.Total-p.Done) / p.Speed * float64(time.Second))
			stats += sFaint.Render("   ·   ") + sAccent2.Render(humanBytes(int64(p.Speed))+"/s") +
				sFaint.Render("   ·   ") + sMuted.Render("ETA ") + sText.Render(clock(eta))
		}
		lines = append(lines, stats)
	} else {
		lines = append(lines, sText.Render(humanCount(p.Done))+sMuted.Render(" of ")+sText.Render(humanCount(p.Total)))
	}
	if p.FilesTotal > 0 || p.Deleted > 0 {
		lines = append(lines, sMuted.Render("files ")+sText.Render(humanCount(p.Files)+" / "+humanCount(p.FilesTotal))+
			sFaint.Render("   ·   ")+sMuted.Render("deleted ")+sText.Render(humanCount(p.Deleted))+
			sFaint.Render("   ·   ")+problemsCount(len(m.errs)-m.warnCount, m.warnCount))
	}
	if p.Current != "" {
		size := ""
		if p.Unit == "bytes" {
			size = "  " + sMuted.Render(humanBytes(p.CurrentSize))
		}
		lines = append(lines, sAccent.Render("↳ ")+sSub.Render(truncMid(p.Current, w-14))+size)
	}
	return lines
}

func problemsCount(errs, warns int) string {
	switch {
	case errs > 0:
		return sErr.Render(fmt.Sprintf("%d error%s", errs, plural(errs)))
	case warns > 0:
		return sWarn.Render(fmt.Sprintf("%d warning%s", warns, plural(warns)))
	}
	return sOK.Render("no errors")
}

func (m *runModel) viewSummary(w int) []string {
	r := m.record
	out := []string{label("Summary")}
	kv := func(k, v string) string { return sMuted.Render(padRight(k, 14)) + v }
	if m.plan != nil {
		if r.DryRun {
			out = append(out,
				kv("would copy", sText.Render(humanCount(m.plan.FilesToCopy)+" files")+sMuted.Render("  ·  ")+sText.Render(humanBytes(m.plan.BytesToCopy))),
				kv("would delete", sText.Render(humanCount(m.plan.FilesToDelete)+" entries")+sMuted.Render("  (moved to replica trash)")),
			)
		} else {
			out = append(out,
				kv("copied", sText.Render(humanCount(r.Stats.FilesTransferred)+" files")+sMuted.Render("  ·  ")+sText.Render(humanBytes(r.Stats.BytesTransferred))),
				kv("deleted", sText.Render(humanCount(r.Stats.FilesDeleted)+" entries")+sMuted.Render("  (kept in replica trash)")),
			)
		}
		out = append(out, kv("main holds", sText.Render(humanCount(m.plan.FilesTotal)+" entries")+sMuted.Render("  ·  ")+sText.Render(humanBytes(m.plan.BytesTotal))))
	}
	ok, failed, upToDate := 0, 0, 0
	for _, s := range r.Steps {
		switch {
		case s.Skipped:
			upToDate++
		case s.Status == meta.StatusFailed:
			failed++
		default:
			ok++
		}
	}
	if len(r.Steps) > 0 {
		okWord := "ok"
		if r.DryRun {
			okWord = "would run"
		}
		parts := []string{sOK.Render(fmt.Sprintf("%d %s", ok, okWord))}
		if failed > 0 {
			parts = append(parts, sErr.Render(fmt.Sprintf("%d failed", failed)))
		}
		if upToDate > 0 {
			parts = append(parts, sMuted.Render(fmt.Sprintf("%d up to date", upToDate)))
		}
		out = append(out, kv("extra steps", strings.Join(parts, sMuted.Render("  ·  "))))
	}
	out = append(out, kv("problems", problemsCount(len(m.errs)-m.warnCount, m.warnCount)))
	if len(r.Pending) > 0 {
		out = append(out, kv("pending", sWarn.Render(fmt.Sprintf("%d item(s) to retry", len(r.Pending)))))
	}
	return out
}

func (m *runModel) viewLog(w, h int) string {
	tabs := []string{"Activity", fmt.Sprintf("Problems %d", len(m.errs))}
	var tabLine []string
	for i, t := range tabs {
		if i == m.tab {
			tabLine = append(tabLine, lipgloss.NewStyle().Foreground(cAccent).Bold(true).Underline(true).Render(t))
		} else {
			st := sMuted
			if i == 1 && len(m.errs) > 0 {
				st = sWarn
			}
			tabLine = append(tabLine, st.Render(t))
		}
	}
	src := m.logs
	if m.tab == 1 {
		src = m.errs
	}
	rows := h - 1
	end := len(src) - m.scroll
	if end < 0 {
		end, m.scroll = 0, len(src)
	}
	start := max(0, end-rows)
	scrollInfo := ""
	if !m.follow {
		scrollInfo = sMuted.Render(fmt.Sprintf("↑ %d more  ·  G to follow", m.scroll))
	}
	lines := []string{spread(strings.Join(tabLine, "    "), scrollInfo, w)}
	for _, l := range src[start:end] {
		ts := sFaint.Render(l.at.Format("15:04:05")) + "  "
		var txt string
		switch l.level {
		case engine.LevelWarn:
			txt = sWarn.Render("! " + truncEnd(l.text, w-14))
		case engine.LevelError:
			txt = sErr.Render("✗ " + truncEnd(l.text, w-14))
		default:
			switch l.kind {
			case engine.KindCopy:
				txt = sAccent2.Render("+ ") + sSub.Render(truncMid(l.text, w-14))
			case engine.KindDelete:
				txt = sWarn.Render("− ") + sMuted.Render(truncMid(l.text, w-23)) + sFaint.Render("  → trash")
			default:
				txt = sText.Render(truncEnd(l.text, w-12))
			}
		}
		lines = append(lines, ts+txt)
	}
	if len(src) == 0 {
		empty := "nothing yet"
		if m.tab == 1 {
			empty = "no problems so far"
		}
		lines = append(lines, sMuted.Render(empty))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

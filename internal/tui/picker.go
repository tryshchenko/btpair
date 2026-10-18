package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tryshchenko/btpair/internal/engine"
	"github.com/tryshchenko/btpair/internal/fsutil"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/steps"
)

type pickItem struct {
	step     steps.Step
	on       bool
	plan     *steps.Plan
	planErr  error
	planning bool
}

type pickerModel struct {
	pair     *engine.Pair
	items    []pickItem
	cursor   int
	dryRun   bool
	checksum bool
	allowDel bool
}

type planMsg struct {
	idx  int
	plan steps.Plan
	err  error
}

const optRows = 3 // dry run, checksum, allow large deletions

func newPicker(a *App, p *engine.Pair, dryRun bool) (*pickerModel, error) {
	list, err := steps.ForPair(a.cfg, p.ID)
	if err != nil {
		return nil, err
	}
	saved, hasSaved := loadSelection(p.ID)
	m := &pickerModel{pair: p, dryRun: dryRun}
	for _, s := range list {
		on := s.DefaultSelected()
		if hasSaved {
			on = saved[s.ID()]
		}
		m.items = append(m.items, pickItem{step: s, on: on, planning: true})
	}
	return m, nil
}

// init plans every step concurrently so the picker can show what each one
// would do before the user commits.
func (m *pickerModel) init() tea.Cmd {
	var cmds []tea.Cmd
	for i, it := range m.items {
		s := it.step
		env := steps.Env{
			PairID:   m.pair.ID,
			Main:     m.pair.Main.Path,
			StateDir: filepath.Join(meta.StepsDir(m.pair.Main.Path), s.ID()),
		}
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			p, err := s.Plan(ctx, env)
			return planMsg{i, p, err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *pickerModel) rows() int { return len(m.items) + optRows }

func (m *pickerModel) update(a *App, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case planMsg:
		it := &m.items[msg.idx]
		it.planning = false
		if msg.err != nil {
			it.planErr = msg.err
		} else {
			it.plan = &msg.plan
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			a.screen = scrDash
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j", "tab":
			if m.cursor < m.rows()-1 {
				m.cursor++
			}
		case " ", "x":
			m.toggle(m.cursor)
		case "a":
			all := true
			for _, it := range m.items {
				all = all && it.on
			}
			for i := range m.items {
				m.items[i].on = !all
			}
		case "u":
			// select only steps that report work to do
			for i := range m.items {
				it := &m.items[i]
				it.on = it.plan != nil && !it.plan.UpToDate
			}
		case "enter":
			return m.start(a)
		}
	}
	return nil
}

func (m *pickerModel) toggle(row int) {
	switch {
	case row < len(m.items):
		m.items[row].on = !m.items[row].on
	case row == len(m.items):
		m.dryRun = !m.dryRun
	case row == len(m.items)+1:
		m.checksum = !m.checksum
	case row == len(m.items)+2:
		m.allowDel = !m.allowDel
	}
}

func (m *pickerModel) start(a *App) tea.Cmd {
	var sel []steps.Step
	chosen := map[string]bool{}
	for _, it := range m.items {
		chosen[it.step.ID()] = it.on
		if it.on {
			sel = append(sel, it.step)
		}
	}
	saveSelection(m.pair.ID, chosen)
	req := engine.Request{
		Pair:            m.pair,
		Config:          a.cfg,
		Steps:           sel,
		DryRun:          m.dryRun,
		AllowMassDelete: m.allowDel,
		Checksum:        m.checksum,
	}
	a.run = newRun(req)
	a.screen = scrRun
	return tea.Batch(a.run.start(), a.pollSpace(m.pair.Main, m.pair.Replica))
}

func (m *pickerModel) view(a *App) string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(header(w, []string{m.pair.ID, "plan backup"}, sMuted.Render(m.pair.Main.Name+"  →  "+m.pair.Replica.Name)))
	b.WriteString("\n\n")

	b.WriteString(spread(label("Extra steps"), sMuted.Render("run on the main drive before mirroring"), w) + "\n")
	if len(m.items) == 0 {
		b.WriteString(sMuted.Render("  No extra steps configured. Define them under [steps] in the config.") + "\n")
	}
	titleW := 22
	for _, it := range m.items {
		titleW = max(titleW, len([]rune(it.step.Title())))
	}
	titleW = min(titleW, 32)
	for i, it := range m.items {
		var status string
		switch {
		case it.planning:
			status = spinner(a.tick) + sMuted.Render(" checking…")
		case it.planErr != nil:
			status = sErr.Render("✗ " + it.planErr.Error())
		case it.plan.UpToDate:
			status = sOK.Render("✓ ") + sMuted.Render(it.plan.Summary)
		default:
			status = sAccent2.Render("● ") + sSub.Render(it.plan.Summary)
		}
		b.WriteString(m.row(i, check(it.on), it.step.Title(), titleW, it.step.Description(), status, w) + "\n")
	}

	b.WriteString("\n" + label("Mirror") + "\n")
	b.WriteString(m.row(-1, sMuted.Render("[■]"), "Mirror main → replica", titleW, "one-way rsync; replica changes go to trash", sMuted.Render("always"), w) + "\n")
	if s := engine.TrashSummary(a.trash[m.pair.Replica.UUID]); s != "" {
		b.WriteString(sMuted.Render(truncEnd("       trash on the replica: "+s+" — emptied oldest first if space runs short", w)) + "\n")
	}

	b.WriteString("\n" + label("Options") + "\n")
	n := len(m.items)
	b.WriteString(m.row(n, check(m.dryRun), "Dry run", titleW, "analyze only, write nothing to either drive", "", w) + "\n")
	limit := a.cfg.Pair(m.pair.ID).MaxDeletePercent
	b.WriteString(m.row(n+1, check(m.checksum), "Verify by checksum", titleW, "compare file contents, not just size & date (reads every file)", "", w) + "\n")
	b.WriteString(m.row(n+2, check(m.allowDel), "Allow large deletions", titleW, fmt.Sprintf("permit deleting more than %.0f%% of the replica's files or data", limit), "", w) + "\n")

	if st := m.pair.State(); st != nil && len(st.Pending) > 0 {
		b.WriteString("\n" + sWarn.Render(fmt.Sprintf("! %d item(s) pending from the last run will be retried", len(st.Pending))) + "\n")
	}

	action := "start backup"
	if m.dryRun {
		action = "start dry run"
	}
	hints := helpBar("↑↓", "move", "space", "toggle", "a", "all/none", "u", "only needed", "enter", action, "esc", "back")
	return layout(a, b.String(), a.footer(hints))
}

func (m *pickerModel) row(idx int, box, title string, titleW int, desc, status string, w int) string {
	cur := "  "
	t := padRight(sText.Render(truncEnd(title, titleW)), titleW)
	if idx == m.cursor {
		cur = sAccent.Render("▸ ")
		t = padRight(sBold.Render(truncEnd(title, titleW)), titleW)
	}
	left := cur + box + "  " + t + "   "
	statusW := min(lipgloss.Width(status), w/3)
	descW := w - lipgloss.Width(left) - statusW - 3
	d := ""
	if descW > 8 {
		d = sMuted.Render(truncEnd(desc, descW))
	}
	return spread(left+d, truncEnd(status, statusW), w)
}

func check(on bool) string {
	if on {
		return sAccent.Render("[✓]")
	}
	return sFaint.Render("[ ]")
}

// Step selections are remembered per pair on this machine.
func selectionPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "btpair", "selection.json")
}

func loadSelection(pair string) (map[string]bool, bool) {
	all := map[string]map[string]bool{}
	if err := fsutil.ReadJSON(selectionPath(), &all); err != nil {
		return nil, false
	}
	s, ok := all[pair]
	return s, ok
}

func saveSelection(pair string, sel map[string]bool) {
	all := map[string]map[string]bool{}
	_ = fsutil.ReadJSON(selectionPath(), &all)
	all[pair] = sel
	_ = fsutil.WriteJSON(selectionPath(), all)
}

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tryshchenko/backup-tool/internal/engine"
	"github.com/tryshchenko/backup-tool/internal/meta"
)

type historyModel struct {
	pair   *engine.Pair
	runs   []meta.RunRecord
	err    error
	cursor int
	detail int // scroll within detail pane
}

func newHistory(p *engine.Pair) *historyModel {
	m := &historyModel{pair: p}
	v := p.Main
	if v == nil {
		v = p.Replica
	}
	m.runs, m.err = meta.ListRuns(v.Path, 200)
	return m
}

func (m *historyModel) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q", "h":
		a.screen = scrDash
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.detail = 0
		}
	case "down", "j":
		if m.cursor < len(m.runs)-1 {
			m.cursor++
			m.detail = 0
		}
	case "pgdown", "J":
		m.detail += 5
	case "pgup", "K":
		m.detail = max(0, m.detail-5)
	}
	return nil
}

func (m *historyModel) view(a *App) string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(header(w, []string{m.pair.ID, "history"}, sMuted.Render(fmt.Sprintf("%d runs", len(m.runs)))))
	b.WriteString("\n\n")
	if m.err != nil {
		b.WriteString(sErr.Render("✗ " + m.err.Error()))
	}
	if len(m.runs) == 0 {
		b.WriteString(sMuted.Render("No runs recorded yet."))
		return layout(a, b.String(), a.footer(helpBar("esc", "back")))
	}

	listH := min(len(m.runs), max(5, (a.h-8)/2-2))
	cols := fmt.Sprintf("  %-18s  %-12s  %8s  %10s  %9s  %10s  %s", "STARTED", "RESULT", "TIME", "COPIED", "DELETED", "DATA", "STEPS")
	b.WriteString(sLabel.Render(truncEnd(cols, w)) + "\n")
	start := max(0, min(m.cursor-listH/2, len(m.runs)-listH))
	for i := start; i < start+listH && i < len(m.runs); i++ {
		r := m.runs[i]
		icon, st, word := statusLook(r.Status)
		dur := ""
		if !r.FinishedAt.IsZero() {
			dur = shortDur(r.FinishedAt.Sub(r.StartedAt))
		}
		stepsTxt := fmt.Sprintf("%d", len(r.Steps))
		row := fmt.Sprintf("%-18s  ", r.StartedAt.Local().Format("Mon 02 Jan 15:04")) +
			st.Render(padRight(icon+" "+word, 12)) +
			fmt.Sprintf("  %8s  %10s  %9s  %10s  %s", dur, humanCount(r.Stats.FilesTransferred), humanCount(r.Stats.FilesDeleted), humanBytes(r.Stats.BytesTransferred), stepsTxt)
		row = truncEnd(row, w-2)
		if i == m.cursor {
			b.WriteString(lipgloss.NewStyle().Background(cSelBg).Width(w).Render(sAccent.Render("▸ ") + row))
		} else {
			b.WriteString("  " + sSub.Render(row))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n" + rule(w) + "\n")
	detailH := a.h - 1 - lipgloss.Height(b.String()) - 3
	b.WriteString(m.viewDetail(m.runs[m.cursor], w, max(4, detailH)))

	hints := helpBar("↑↓", "select run", "J/K", "scroll details", "esc", "back")
	return layout(a, b.String(), a.footer(hints))
}

func (m *historyModel) viewDetail(r meta.RunRecord, w, h int) string {
	var lines []string
	icon, st, word := statusLook(r.Status)
	lines = append(lines, st.Render(icon+" "+strings.ToUpper(word))+sMuted.Render("   run "+r.RunID+"  ·  host "+r.Host))
	if r.Message != "" {
		lines = append(lines, sSub.Render(r.Message))
	}
	if len(r.Steps) > 0 {
		lines = append(lines, "", label("Extra steps"))
		for _, s := range r.Steps {
			i, sst, _ := statusLook(s.Status)
			if s.Skipped {
				i, sst = "–", sMuted
			}
			lines = append(lines, "  "+sst.Render(i)+" "+sText.Render(s.Title)+sMuted.Render("  "+shortDur(s.Duration)+"  "+s.Message))
		}
	}
	if len(r.Pending) > 0 {
		lines = append(lines, "", label("Pending"))
		for _, p := range r.Pending {
			lines = append(lines, "  "+sWarn.Render("• "+p.Kind+" ")+sText.Render(p.Path)+sMuted.Render("  "+p.Reason))
		}
	}
	if len(r.Errors) > 0 {
		lines = append(lines, "", label(fmt.Sprintf("Errors (%d)", len(r.Errors))))
		for _, e := range r.Errors {
			lines = append(lines, "  "+sErr.Render("✗ ")+sSub.Render(e))
		}
	}
	if len(r.Log) > 0 {
		lines = append(lines, "", label("Log"))
		for _, l := range r.Log {
			lines = append(lines, "  "+sMuted.Render(l))
		}
	}
	for i := range lines {
		lines[i] = truncEnd(lines[i], w)
	}
	if m.detail > len(lines)-h {
		m.detail = max(0, len(lines)-h)
	}
	end := min(len(lines), m.detail+h)
	return strings.Join(lines[m.detail:end], "\n")
}

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tryshchenko/backup-tool/internal/engine"
	"github.com/tryshchenko/backup-tool/internal/meta"
	"github.com/tryshchenko/backup-tool/internal/volume"
)

type dashModel struct {
	cursor int
}

func (d *dashModel) clamp(n int) {
	if d.cursor >= n {
		d.cursor = max(0, n-1)
	}
}

func (a *App) selectedPair() *engine.Pair {
	if len(a.pairs) == 0 {
		return nil
	}
	return a.pairs[a.dash.cursor]
}

func (a *App) updateDash(msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch km.String() {
	case "q", "esc":
		return tea.Quit
	case "up", "k":
		if a.dash.cursor > 0 {
			a.dash.cursor--
		}
	case "down", "j", "tab":
		if a.dash.cursor < len(a.pairs)-1 {
			a.dash.cursor++
		}
	case "r":
		a.scanned = false
		return a.scan()
	case "n":
		if len(a.unpaired) < 2 {
			a.setFlash(engine.LevelWarn, "connect two drives that are not yet paired to create a pair")
			return nil
		}
		a.np = newNewPair(a)
		a.screen = scrNewPair
		return a.np.init()
	case "h":
		p := a.selectedPair()
		if p == nil || (p.Main == nil && p.Replica == nil) {
			return nil
		}
		a.hist = newHistory(p)
		a.screen = scrHistory
	case "enter", "s", "d":
		p := a.selectedPair()
		if p == nil {
			return nil
		}
		if !p.Ready() {
			a.setFlash(engine.LevelWarn, pairBlocker(p))
			return nil
		}
		pk, err := newPicker(a, p, km.String() == "d")
		if err != nil {
			a.setFlash(engine.LevelError, err.Error())
			return nil
		}
		a.picker = pk
		a.screen = scrPicker
		return pk.init()
	}
	return nil
}

func pairBlocker(p *engine.Pair) string {
	switch {
	case len(p.Problems) > 0:
		return p.Problems[0]
	case p.Main == nil:
		return "connect the main drive of " + p.ID
	case p.Replica == nil:
		return "connect the replica drive of " + p.ID
	}
	return ""
}

func (a *App) viewDash() string {
	w := a.contentWidth()
	mode := ""
	if a.opts.DevRoot != "" {
		mode = sWarn.Render("dev mode") + sFaint.Render("  ·  ")
	}
	right := mode + sMuted.Render(fmt.Sprintf("%d drive%s", len(a.vols), plural(len(a.vols))))
	var b strings.Builder
	b.WriteString(header(w, []string{"backup pairs"}, right))
	b.WriteString("\n\n")

	switch {
	case !a.scanned:
		b.WriteString(spinner(a.tick) + sSub.Render("  looking for drives…"))
	case a.scanErr != nil:
		b.WriteString(sErr.Render("✗ " + a.scanErr.Error()))
	case len(a.pairs) == 0:
		b.WriteString(emptyState(w, len(a.unpaired)))
	default:
		for i, p := range a.pairs {
			b.WriteString(a.pairCard(p, i == a.dash.cursor, w))
			b.WriteString("\n")
		}
	}

	if len(a.unpaired) > 0 {
		b.WriteString("\n" + label("Unpaired drives") + "\n")
		for _, v := range a.unpaired {
			b.WriteString("  " + a.driveRow("", v, 0, w-2) + "\n")
		}
	}

	hints := helpBar("↑↓", "select", "enter", "back up", "d", "dry run", "h", "history", "n", "new pair", "r", "rescan", "q", "quit")
	return layout(a, b.String(), a.footer(hints))
}

func emptyState(w, unpaired int) string {
	msg := sBold.Render("No backup pairs connected") + "\n\n"
	if unpaired >= 2 {
		msg += sSub.Render("Two unpaired drives are connected. Press ") + sKey.Render("n") + sSub.Render(" to pair them as main and replica.")
	} else {
		msg += sSub.Render("Plug in both drives of a pair, or two blank drives to create a new pair, then press ") + sKey.Render("r") + sSub.Render(".")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(1, 3).Width(w - 2).Render(msg)
}

func (a *App) pairCard(p *engine.Pair, selected bool, w int) string {
	inner := w - 6
	st := p.State()

	// title line
	title := sBold.Render(p.ID)
	if p.Label != "" && p.Label != p.ID {
		title += "  " + sSub.Render(p.Label)
	}
	if selected {
		title = sAccent.Render("▸ ") + title
	} else {
		title = "  " + title
	}
	b := []string{spread(title, pairStatus(p, st), inner), ""}

	b = append(b, "  "+a.driveRowOrMissing("main", p.Main, inner-2))
	b = append(b, "  "+a.driveRowOrMissing("replica", p.Replica, inner-2))
	b = append(b, "")
	b = append(b, "  "+lastRunLine(st, inner-2))
	for _, pr := range p.Problems {
		b = append(b, "  "+sErr.Render("✗ "+truncEnd(pr, inner-4)))
	}

	border := cFaint
	if selected {
		border = cAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Width(w - 2).Render(strings.Join(b, "\n"))
}

func pairStatus(p *engine.Pair, st *meta.State) string {
	switch {
	case len(p.Problems) > 0:
		return pill("needs attention", cErr)
	case p.Main == nil && p.Replica == nil:
		return pill("offline", cMuted)
	case p.Main == nil:
		return pill("main drive not connected", cMuted)
	case p.Replica == nil:
		return pill("replica not connected", cMuted)
	case st != nil && st.Status == meta.StatusInProgress:
		return pill("interrupted — resume", cWarn)
	case st != nil && (st.Status == meta.StatusFailed || st.Status == meta.StatusCancelled):
		return pill("ready · last run "+string(st.Status), cWarn)
	case st != nil && len(st.Pending) > 0:
		return pill(fmt.Sprintf("ready · %d pending", len(st.Pending)), cWarn)
	}
	return pill("ready", cOK)
}

func lastRunLine(st *meta.State, w int) string {
	if st == nil || st.Status == meta.StatusNever {
		return sMuted.Render("never synchronized")
	}
	icon, style, word := statusLook(st.Status)
	line := style.Render(icon+" "+word) + sMuted.Render("  "+ago(st.StartedAt))
	if st.Status != meta.StatusInProgress {
		line += sFaint.Render("  ·  ") + sSub.Render(fmt.Sprintf("%s copied, %s deleted, %s",
			humanCount(st.Stats.FilesTransferred), humanCount(st.Stats.FilesDeleted), humanBytes(st.Stats.BytesTransferred)))
	}
	if st.Status != meta.StatusOK && !st.LastSuccessAt.IsZero() {
		line += sFaint.Render("  ·  ") + sMuted.Render("last good "+ago(st.LastSuccessAt))
	}
	return truncEnd(line, w)
}

func statusLook(s meta.Status) (string, lipgloss.Style, string) {
	switch s {
	case meta.StatusOK:
		return "✓", sOK, "synced"
	case meta.StatusPartial:
		return "!", sWarn, "partial"
	case meta.StatusFailed:
		return "✗", sErr, "failed"
	case meta.StatusCancelled:
		return "◼", sWarn, "cancelled"
	case meta.StatusInProgress:
		return "◌", sWarn, "interrupted"
	case meta.StatusDryRun:
		return "○", sSub, "dry run"
	}
	return "·", sMuted, string(s)
}

func (a *App) driveRowOrMissing(role string, v *volume.Volume, w int) string {
	if v == nil {
		return sLabel.Render(padRight(strings.ToUpper(role), 9)) + sMuted.Render("not connected")
	}
	return a.driveRow(role, v, 0, w)
}

// driveRow renders: ROLE  NAME  ━━━━━━━━──────  used · free
// incoming is bytes about to land on the drive (drawn as a dashed segment).
func (a *App) driveRow(role string, v *volume.Volume, incoming int64, w int) string {
	roleCol := ""
	if role != "" {
		roleCol = sLabel.Render(padRight(strings.ToUpper(role), 9))
	}
	nameW := min(22, max(10, w/6))
	name := padRight(sText.Render(truncMid(v.Name, nameW)), nameW+2)

	sp, ok := a.spaces[v.UUID]
	if !ok {
		return roleCol + name + sMuted.Render("measuring…")
	}
	stats := sSub.Render(humanBytes(int64(sp.Used()))+" used") +
		sMuted.Render(" · ") + sText.Render(humanBytes(int64(sp.Free))+" free")
	baseW := lipgloss.Width(stats)
	if incoming > 0 {
		stats += sAccent2.Render("  +" + humanBytes(incoming))
	}
	if v.ReadOnly {
		stats += "  " + sWarn.Render("read-only")
	}
	// Every row reserves the same room for annotations (like "+1.2 GB") so
	// bars line up; when even that does not fit, drop the bar.
	const annotationW = 11
	fixed := lipgloss.Width(roleCol) + nameW + 2 + 2
	barW := min(44, w-fixed-baseW-annotationW)
	if barW < 6 {
		return roleCol + name + stats
	}
	in := 0.0
	if sp.Total > 0 {
		in = float64(incoming) / float64(sp.Total)
	}
	return roleCol + name + usageBar(barW, sp.UsedFrac(), in) + "  " + stats
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

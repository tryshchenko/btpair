package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tryshchenko/btpair/internal/engine"
	"github.com/tryshchenko/btpair/internal/volume"
)

// newPairModel is a short wizard: choose main, choose replica, name the pair,
// confirm. It writes identity files to both drives.
type newPairModel struct {
	stage   int // 0 main, 1 replica, 2 name, 3 label, 4 confirm
	cursor  int
	main    *volume.Volume
	replica *volume.Volume
	name    textinput.Model
	label   textinput.Model
	err     string
	choices []*volume.Volume
}

func newNewPair(a *App) *newPairModel {
	name := textinput.New()
	name.Placeholder = "e.g. work, media, archive"
	name.CharLimit = 64
	name.Prompt = sAccent.Render("› ")
	lbl := textinput.New()
	lbl.Placeholder = "optional, e.g. Work SSDs (Samsung T7)"
	lbl.CharLimit = 80
	lbl.Prompt = sAccent.Render("› ")
	return &newPairModel{name: name, label: lbl, choices: a.unpaired}
}

func (m *newPairModel) init() tea.Cmd { return nil }

func (m *newPairModel) options() []*volume.Volume {
	if m.stage == 1 {
		var out []*volume.Volume
		for _, v := range m.choices {
			if v != m.main {
				out = append(out, v)
			}
		}
		return out
	}
	return m.choices
}

func (m *newPairModel) update(a *App, msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyMsg)
	if isKey && k.String() == "esc" {
		if m.stage == 0 {
			a.screen = scrDash
			return nil
		}
		m.stage--
		m.cursor, m.err = 0, ""
		m.focus()
		return nil
	}
	switch m.stage {
	case 0, 1:
		if !isKey {
			return nil
		}
		opts := m.options()
		switch k.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(opts)-1 {
				m.cursor++
			}
		case "enter":
			if m.stage == 0 {
				m.main = opts[m.cursor]
			} else {
				m.replica = opts[m.cursor]
			}
			m.stage++
			m.cursor = 0
			m.focus()
		}
		return nil
	case 2, 3:
		if isKey && k.String() == "enter" {
			if m.stage == 2 && strings.TrimSpace(m.name.Value()) == "" {
				m.err = "a pair needs a name"
				return nil
			}
			m.err = ""
			m.stage++
			m.focus()
			return nil
		}
		var cmd tea.Cmd
		if m.stage == 2 {
			m.name, cmd = m.name.Update(msg)
		} else {
			m.label, cmd = m.label.Update(msg)
		}
		return cmd
	case 4:
		if isKey && (k.String() == "enter" || k.String() == "y") {
			id := strings.TrimSpace(m.name.Value())
			if err := engine.InitPair(m.main, m.replica, id, strings.TrimSpace(m.label.Value())); err != nil {
				m.err = err.Error()
				return nil
			}
			a.setFlash(engine.LevelInfo, "pair "+id+" created — "+m.main.Name+" is main, "+m.replica.Name+" is replica")
			a.screen = scrDash
			a.scanned = false
			return a.scan()
		}
	}
	return nil
}

func (m *newPairModel) focus() {
	m.name.Blur()
	m.label.Blur()
	switch m.stage {
	case 2:
		m.name.Focus()
	case 3:
		m.label.Focus()
	}
}

func (m *newPairModel) view(a *App) string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(header(w, []string{"new pair"}, m.progressDots()))
	b.WriteString("\n\n")

	done := func(k, v string) {
		b.WriteString(sOK.Render("✓ ") + sMuted.Render(padRight(k, 10)) + sText.Render(v) + "\n")
	}
	if m.stage > 0 {
		done("main", m.main.Name)
	}
	if m.stage > 1 {
		done("replica", m.replica.Name)
	}
	if m.stage > 2 {
		done("name", m.name.Value())
	}
	if m.stage > 3 && m.label.Value() != "" {
		done("label", m.label.Value())
	}
	if m.stage > 0 {
		b.WriteString("\n")
	}

	switch m.stage {
	case 0, 1:
		q := "Which is the main drive?"
		sub := "You work on the main drive. It is only ever read during a sync — never written to."
		if m.stage == 1 {
			q = "Which drive is the replica?"
			sub = "The replica becomes a mirror of the main drive. Files only on the replica are moved to its trash."
		}
		b.WriteString(sBold.Render(q) + "\n" + sMuted.Width(w).Render(sub) + "\n\n")
		for i, v := range m.options() {
			cur := "  "
			if i == m.cursor {
				cur = sAccent.Render("▸ ")
			}
			b.WriteString(cur + a.driveRow("", v, 0, w-2) + "\n")
		}
	case 2:
		b.WriteString(sBold.Render("Name this pair") + "\n" + sMuted.Render("Short identifier stored on both drives: letters, digits, - and _.") + "\n\n")
		b.WriteString(m.name.View() + "\n")
	case 3:
		b.WriteString(sBold.Render("Add a description") + "\n" + sMuted.Render("Shown next to the name. Press enter to skip.") + "\n\n")
		b.WriteString(m.label.View() + "\n")
	case 4:
		b.WriteString(sBold.Render("Ready to pair") + "\n\n")
		b.WriteString(sSub.Render("btpair will write a small ") + sText.Render(".backuptool/") + sSub.Render(" folder to the root of both drives.") + "\n")
		b.WriteString(sSub.Render("No other files are touched until you run a backup.") + "\n")
	}
	if m.err != "" {
		b.WriteString("\n" + sErr.Render("✗ "+m.err) + "\n")
	}

	var hints string
	switch m.stage {
	case 0, 1:
		hints = helpBar("↑↓", "choose", "enter", "select", "esc", "back")
	case 4:
		hints = helpBar("enter", "create pair", "esc", "back")
	default:
		hints = helpBar("enter", "continue", "esc", "back")
	}
	return layout(a, b.String(), a.footer(hints))
}

func (m *newPairModel) progressDots() string {
	var s []string
	for i := 0; i < 5; i++ {
		switch {
		case i < m.stage:
			s = append(s, sAccent.Render("●"))
		case i == m.stage:
			s = append(s, sAccent2.Render("●"))
		default:
			s = append(s, sFaint.Render("●"))
		}
	}
	return strings.Join(s, " ")
}

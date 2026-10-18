// Package tui is btpair's interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/engine"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/volume"
)

type Options struct {
	DevRoot string
	Config  *config.Config
}

type screen int

const (
	scrDash screen = iota
	scrPicker
	scrRun
	scrHistory
	scrNewPair
)

type App struct {
	opts Options
	cfg  *config.Config

	w, h int
	tick int

	screen   screen
	vols     []*volume.Volume
	pairs    []*engine.Pair
	unpaired []*volume.Volume
	spaces   map[string]volume.Space    // keyed by volume UUID
	trash    map[string][]meta.TrashRun // keyed by replica UUID
	scanned  bool
	scanErr  error
	polling  bool

	flash      string
	flashLevel engine.Level
	flashAt    time.Time

	dash   dashModel
	picker *pickerModel
	run    *runModel
	hist   *historyModel
	np     *newPairModel
}

type (
	tickMsg struct{}
	scanMsg struct {
		vols []*volume.Volume
		err  error
	}
	spaceMsg map[string]volume.Space
	trashMsg struct {
		uuid string
		runs []meta.TrashRun
	}
	trashEmptiedMsg struct {
		pair         string
		files, bytes int64
		err          error
	}
)

func New(opts Options) *App {
	return &App{opts: opts, cfg: opts.Config, spaces: map[string]volume.Space{}, trash: map[string][]meta.TrashRun{}}
}

func Run(opts Options) error {
	_, err := tea.NewProgram(New(opts), tea.WithAltScreen()).Run()
	return err
}

func (a *App) Init() tea.Cmd { return tea.Batch(a.scan(), tickCmd()) }

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (a *App) scan() tea.Cmd {
	dev := a.opts.DevRoot
	return func() tea.Msg {
		vols, err := volume.Detect(context.Background(), dev)
		return scanMsg{vols, err}
	}
}

// pollSpace measures free space for the given drives off the UI thread.
func (a *App) pollSpace(vols ...*volume.Volume) tea.Cmd {
	if a.polling {
		return nil
	}
	a.polling = true
	return func() tea.Msg {
		out := spaceMsg{}
		for _, v := range vols {
			if v == nil {
				continue
			}
			if sp, err := v.Space(); err == nil {
				out[v.UUID] = sp
			}
		}
		return out
	}
}

// measureTrash sizes each connected replica's trash off the UI thread.
func (a *App) measureTrash() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range a.pairs {
		if v := p.Replica; v != nil {
			cmds = append(cmds, func() tea.Msg {
				runs, _ := meta.ListTrash(v.Path)
				return trashMsg{v.UUID, runs}
			})
		}
	}
	return tea.Batch(cmds...)
}

func (a *App) setFlash(l engine.Level, msg string) {
	a.flash, a.flashLevel, a.flashAt = msg, l, time.Now()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil

	case tickMsg:
		a.tick++
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())
		if a.flash != "" && time.Since(a.flashAt) > 6*time.Second {
			a.flash = ""
		}
		if a.screen == scrRun && a.run != nil && a.tick%10 == 0 {
			cmds = append(cmds, a.pollSpace(a.run.req.Pair.Main, a.run.req.Pair.Replica))
		}
		return a, tea.Batch(cmds...)

	case scanMsg:
		a.scanned = true
		a.scanErr = msg.err
		a.vols = msg.vols
		a.pairs, a.unpaired = engine.Group(msg.vols)
		a.dash.clamp(len(a.pairs))
		return a, tea.Batch(a.pollSpace(msg.vols...), a.measureTrash())

	case trashMsg:
		a.trash[msg.uuid] = msg.runs
		return a, nil

	case trashEmptiedMsg:
		a.dash.emptying = ""
		if msg.err != nil {
			a.setFlash(engine.LevelError, "emptying trash: "+msg.err.Error())
		} else {
			a.setFlash(engine.LevelInfo, fmt.Sprintf("emptied trash on %s: %s, %s files", msg.pair, engine.HumanBytes(msg.bytes), humanCount(msg.files)))
		}
		var vols []*volume.Volume
		if p := a.pairByID(msg.pair); p != nil {
			vols = append(vols, p.Replica)
		}
		return a, tea.Batch(a.pollSpace(vols...), a.measureTrash())

	case spaceMsg:
		a.polling = false
		for k, v := range msg {
			a.spaces[k] = v
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && a.screen != scrRun {
			return a, tea.Quit
		}
	}

	switch a.screen {
	case scrDash:
		return a, a.updateDash(msg)
	case scrPicker:
		return a, a.picker.update(a, msg)
	case scrRun:
		return a, a.run.update(a, msg)
	case scrHistory:
		return a, a.hist.update(a, msg)
	case scrNewPair:
		return a, a.np.update(a, msg)
	}
	return a, nil
}

func (a *App) View() string {
	if a.w == 0 {
		return ""
	}
	var body string
	switch a.screen {
	case scrDash:
		body = a.viewDash()
	case scrPicker:
		body = a.picker.view(a)
	case scrRun:
		body = a.run.view(a)
	case scrHistory:
		body = a.hist.view(a)
	case scrNewPair:
		body = a.np.view(a)
	}
	return lipgloss.NewStyle().Padding(1, 2, 0, 2).Render(fitHeight(body, a.h-1))
}

// contentWidth is the usable width inside the outer padding.
func (a *App) contentWidth() int { return min(max(a.w-4, 40), 160) }

// fitHeight trims or pads a view so the footer stays pinned to the bottom.
func fitHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h && h > 0 {
		// keep the footer (last line) visible
		lines = append(lines[:h-1], lines[len(lines)-1])
	}
	return strings.Join(lines, "\n")
}

// layout places a body and a footer, pushing the footer to the bottom.
func layout(a *App, body, footer string) string {
	bh := lipgloss.Height(body)
	fh := lipgloss.Height(footer)
	gap := a.h - 1 - bh - fh
	if gap < 1 {
		gap = 1
	}
	return body + strings.Repeat("\n", gap) + footer
}

func (a *App) footer(hints string) string {
	w := a.contentWidth()
	line := hints
	if a.flash != "" {
		st := sOK
		icon := "✓ "
		switch a.flashLevel {
		case engine.LevelWarn:
			st, icon = sWarn, "! "
		case engine.LevelError:
			st, icon = sErr, "✗ "
		}
		return rule(w) + "\n" + spread(st.Render(icon+a.flash), "", w)
	}
	return rule(w) + "\n" + fitHints(line, w)
}

func (a *App) pairByID(id string) *engine.Pair {
	for _, p := range a.pairs {
		if p.ID == id {
			return p
		}
	}
	return nil
}

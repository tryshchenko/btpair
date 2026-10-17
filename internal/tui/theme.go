package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
)

// Palette: calm neutrals, one violet accent fading into cyan for motion,
// and soft semantic colours. Every colour adapts to light terminals.
var (
	cText    = lipgloss.AdaptiveColor{Light: "#1F2430", Dark: "#E6E6EF"}
	cSub     = lipgloss.AdaptiveColor{Light: "#5C6370", Dark: "#A0A3B4"}
	cMuted   = lipgloss.AdaptiveColor{Light: "#8A8F9C", Dark: "#6E7185"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#D5D7DE", Dark: "#34364A"}
	cTrack   = lipgloss.AdaptiveColor{Light: "#E6E7EC", Dark: "#2A2C3D"}
	cAccent  = lipgloss.AdaptiveColor{Light: "#6D4AFF", Dark: "#A48CFF"}
	cAccent2 = lipgloss.AdaptiveColor{Light: "#0B8FB3", Dark: "#5FD4F4"}
	cOK      = lipgloss.AdaptiveColor{Light: "#1E9E5A", Dark: "#6EE7A8"}
	cWarn    = lipgloss.AdaptiveColor{Light: "#B7791F", Dark: "#F5C46B"}
	cErr     = lipgloss.AdaptiveColor{Light: "#D23F57", Dark: "#FF7A90"}
	cSelBg   = lipgloss.AdaptiveColor{Light: "#EFEBFF", Dark: "#24203A"}

	gradFrom = adaptive("#6D4AFF", "#8B6CFF")
	gradTo   = adaptive("#0B8FB3", "#5FD4F4")
)

var (
	sText    = lipgloss.NewStyle().Foreground(cText)
	sSub     = lipgloss.NewStyle().Foreground(cSub)
	sMuted   = lipgloss.NewStyle().Foreground(cMuted)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sAccent  = lipgloss.NewStyle().Foreground(cAccent)
	sAccent2 = lipgloss.NewStyle().Foreground(cAccent2)
	sOK      = lipgloss.NewStyle().Foreground(cOK)
	sWarn    = lipgloss.NewStyle().Foreground(cWarn)
	sErr     = lipgloss.NewStyle().Foreground(cErr)
	sBold    = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sLabel   = lipgloss.NewStyle().Foreground(cMuted).Bold(true) // small caps-ish section labels
	sKey     = lipgloss.NewStyle().Foreground(cSub).Bold(true)

	sLogo = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

func adaptive(light, dark string) colorful.Color {
	hex := dark
	if !lipgloss.HasDarkBackground() {
		hex = light
	}
	c, _ := colorful.Hex(hex)
	return c
}

// label renders a section heading like "PHASES".
func label(s string) string { return sLabel.Render(strings.ToUpper(s)) }

// pill renders a small status badge.
func pill(text string, fg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Foreground(fg).Bold(true).Render("● " + text)
}

func badge(text string, fg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#14141F"}).
		Background(fg).Bold(true).Padding(0, 1).Render(text)
}

var partials = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// gradientBar draws a smooth, eighth-cell-precise bar fading from the
// accent to cyan. An indeterminate bar (frac < 0) shows a sweeping glow.
func gradientBar(width int, frac float64, tick int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	if frac < 0 {
		// indeterminate: a soft comet travelling along the track
		pos := float64(tick%(width*2)) / 2
		for i := 0; i < width; i++ {
			d := math.Abs(float64(i) - pos)
			if d < 6 {
				t := 1 - d/6
				c := gradFrom.BlendLuv(gradTo, float64(i)/float64(width))
				track, _ := colorful.Hex(hexOf(cTrack))
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(track.BlendLuv(c, t).Hex())).Render("━"))
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(cTrack).Render("━"))
			}
		}
		return b.String()
	}
	frac = math.Max(0, math.Min(1, frac))
	cells := frac * float64(width)
	full := int(cells)
	rem := int((cells - float64(full)) * 8)
	for i := 0; i < full; i++ {
		c := gradFrom.BlendLuv(gradTo, float64(i)/math.Max(1, float64(width-1)))
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Render("█"))
	}
	n := full
	if n < width && rem > 0 {
		c := gradFrom.BlendLuv(gradTo, float64(full)/math.Max(1, float64(width-1)))
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Background(cTrack).Render(string(partials[rem])))
		n++
	}
	if n < width {
		b.WriteString(lipgloss.NewStyle().Background(cTrack).Render(strings.Repeat(" ", width-n)))
	}
	return b.String()
}

// usageBar is a slim disk-usage bar coloured by how full the disk is.
// incoming (0..1 of total) is drawn as a dimmer segment after the used part.
func usageBar(width int, used, incoming float64) string {
	if width <= 0 {
		return ""
	}
	col := cOK
	switch {
	case used+incoming >= 0.9:
		col = cErr
	case used+incoming >= 0.75:
		col = cWarn
	}
	u := int(math.Round(used * float64(width)))
	if used > 0 && u == 0 {
		u = 1
	}
	in := int(math.Round(incoming * float64(width)))
	if incoming > 0 && in == 0 {
		in = 1
	}
	if u+in > width {
		in = max(0, width-u)
	}
	return lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("━", u)) +
		lipgloss.NewStyle().Foreground(cAccent2).Render(strings.Repeat("╍", in)) +
		lipgloss.NewStyle().Foreground(cTrack).Render(strings.Repeat("━", width-u-in))
}

func hexOf(c lipgloss.AdaptiveColor) string {
	if lipgloss.HasDarkBackground() {
		return c.Dark
	}
	return c.Light
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinner(tick int) string { return sAccent.Render(spinFrames[tick%len(spinFrames)]) }

func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

func humanCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func clock(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func shortDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return clock(d)
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan 2006")
	}
}

// truncMid shortens s to width display cells, keeping both ends — the most
// informative parts of a path.
func truncMid(s string, width int) string {
	if width <= 1 || ansi.StringWidth(s) <= width {
		return s
	}
	r := []rune(s)
	keep := width - 1
	head := keep / 3
	tail := keep - head
	if tail > len(r) {
		tail = len(r)
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

func truncEnd(s string, width int) string { return ansi.Truncate(s, width, "…") }

// padRight pads a (possibly styled) string to width cells.
func padRight(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// spread places left and right on one line of the given width.
func spread(left, right string, width int) string {
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return truncEnd(left, max(0, width-ansi.StringWidth(right)-1)) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func rule(width int) string { return sFaint.Render(strings.Repeat("─", max(0, width))) }

var hintSep = sFaint.Render("  ·  ")

// fitHints drops trailing hints that do not fit, rather than wrapping.
func fitHints(s string, width int) string {
	if ansi.StringWidth(s) <= width {
		return s
	}
	parts := strings.Split(s, hintSep)
	for len(parts) > 1 {
		parts = parts[:len(parts)-1]
		if out := strings.Join(parts, hintSep); ansi.StringWidth(out) <= width {
			return out
		}
	}
	return truncEnd(s, width)
}

// helpBar renders "key action · key action" hints.
func helpBar(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+" "+sMuted.Render(pairs[i+1]))
	}
	return strings.Join(parts, hintSep)
}

func header(width int, crumbs []string, right string) string {
	left := sLogo.Render("◆ btpair")
	for _, c := range crumbs {
		left += sFaint.Render("  ›  ") + sText.Render(c)
	}
	return spread(left, right, width)
}

package engine

import (
	"fmt"
	"path/filepath"

	"github.com/tryshchenko/btpair/internal/rsync"
)

// perCause is how many errors of one kind are logged individually before
// the rest are folded into a single summary line.
const perCause = 3

// problemLog turns rsync's per-file error lines into readable log entries:
// the first few of each cause are shown with a plain-language hint, the rest
// are counted and summarized once the transfer ends.
type problemLog struct {
	r       *runner
	roots   []string
	total   int
	order   []string
	byCause map[string]*causeGroup
	reasons map[string]string // path → cause
}

type causeGroup struct {
	hint   string
	count  int
	folder string // common folder of the affected paths, if any
}

func newProblemLog(r *runner, roots ...string) *problemLog {
	return &problemLog{r: r, roots: roots, byCause: map[string]*causeGroup{}, reasons: map[string]string{}}
}

func (l *problemLog) add(line string) {
	p := rsync.ParseError(line, l.roots...)
	if p.Summary {
		return // rsync's "some files were not transferred"; we summarize ourselves
	}
	l.total++
	g := l.byCause[p.Cause]
	if g == nil {
		g = &causeGroup{hint: p.Hint, folder: filepath.Dir(p.Path)}
		l.byCause[p.Cause] = g
		l.order = append(l.order, p.Cause)
	}
	g.count++
	if p.Path != "" {
		l.reasons[p.Path] = p.Cause
		g.folder = commonDir(g.folder, filepath.Dir(p.Path))
	}
	if g.count > perCause {
		return
	}
	switch {
	case p.Path == "":
		l.r.errorf("%s", p.Raw)
	case p.Hint != "":
		l.r.errorf("%s: %s — %s", p.Path, p.Cause, p.Hint)
	default:
		l.r.errorf("%s: %s", p.Path, p.Cause)
	}
}

func (l *problemLog) flush() {
	for _, c := range l.order {
		g := l.byCause[c]
		if g.count <= perCause {
			continue
		}
		where := ""
		if g.folder != "" && g.folder != "." {
			where = " in " + g.folder
		}
		msg := fmt.Sprintf("…and %d more files%s failed the same way (%s)", g.count-perCause, where, c)
		if g.hint != "" {
			msg += " — " + g.hint
		}
		l.r.errorf("%s", msg)
	}
}

func (l *problemLog) reason(path string) string {
	if c, ok := l.reasons[path]; ok {
		return c
	}
	return "rsync error"
}

func commonDir(a, b string) string {
	for a != b {
		if len(a) > len(b) {
			a = filepath.Dir(a)
		} else {
			b = filepath.Dir(b)
		}
		if a == "." || b == "." || a == "/" || b == "/" {
			return "."
		}
	}
	return a
}

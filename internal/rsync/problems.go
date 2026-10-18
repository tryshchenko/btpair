package rsync

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Problem is one rsync error line, decoded into the file it concerns and why.
type Problem struct {
	Path    string // relative to the drive root when rsync gave one
	Cause   string // rsync's reason, e.g. "Operation not permitted"
	Hint    string // what that usually means, in plain words
	Summary bool   // rsync's closing "some files were not transferred" line
	Raw     string
}

var (
	renameErr = regexp.MustCompile(`rename "[^"]+" -> "([^"]+)": (.+?) \(\d+\)$`)
	unlinkErr = regexp.MustCompile(`(?:unlink|rmdir)\((.+)\) failed: (.+?) \(\d+\)$`)
	quotedErr = regexp.MustCompile(`"([^"]+)"[^"]*?: (.+?) \(\d+\)$`)
	vanished  = regexp.MustCompile(`^file has vanished: "([^"]+)"`)
)

var hints = []struct{ cause, hint string }{
	{"Operation not permitted", "file is locked (Finder ▸ Get Info ▸ Locked) or protected by macOS"},
	{"Permission denied", "no permission to read or write it"},
	{"No space left on device", "the replica is full"},
	{"Input/output error", "the drive could not read or write it — check the cable and the drive's health"},
	{"File name too long", "the name is too long for the replica's filesystem"},
	{"Illegal byte sequence", "the name contains characters the replica's filesystem rejects"},
	{"Invalid argument", "the name is not allowed on the replica's filesystem (e.g. a trailing dot or space on exFAT)"},
	{"vanished", "the file changed or disappeared during the backup"},
}

// ParseError decodes an rsync stderr line. roots are the drive roots (main
// and replica), used to turn absolute paths into drive-relative ones.
func ParseError(line string, roots ...string) Problem {
	p := Problem{Raw: line, Cause: line}
	switch {
	case strings.HasPrefix(line, "rsync error:"):
		p.Summary = true
		return p
	case vanished.MatchString(line):
		m := vanished.FindStringSubmatch(line)
		p.Path, p.Cause = m[1], "vanished"
	default:
		for _, re := range []*regexp.Regexp{renameErr, unlinkErr, quotedErr} {
			if m := re.FindStringSubmatch(line); m != nil {
				p.Path, p.Cause = m[1], m[2]
				break
			}
		}
	}
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(p.Path) {
			continue
		}
		if rel, err := filepath.Rel(root, p.Path); err == nil && !strings.HasPrefix(rel, "..") {
			p.Path = rel
			break
		}
	}
	for _, h := range hints {
		if strings.Contains(p.Cause, h.cause) {
			p.Hint = h.hint
			break
		}
	}
	return p
}

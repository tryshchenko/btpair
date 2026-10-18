package rsync

import (
	"strings"
	"testing"
)

func TestParseError(t *testing.T) {
	const src, dst = "/Volumes/MAIN", "/Volumes/REPL"
	cases := []struct {
		line, path, cause, hint string
		summary                 bool
	}{
		{ // the temp file must not be reported as the failed path
			line: `rsync: [receiver] rename "/Volumes/REPL/Music/Art/.01 - Song.mp3.QvH6sC" -> "Music/Art/01 - Song.mp3": Operation not permitted (1)`,
			path: "Music/Art/01 - Song.mp3", cause: "Operation not permitted", hint: "locked",
		},
		{
			line: `rsync: [generator] delete_file: unlink(Old/gone (1).txt) failed: Operation not permitted (1)`,
			path: "Old/gone (1).txt", cause: "Operation not permitted", hint: "locked",
		},
		{
			line: `rsync: [sender] send_files failed to open "/Volumes/MAIN/secret.txt": Permission denied (13)`,
			path: "secret.txt", cause: "Permission denied", hint: "permission",
		},
		{
			line: `rsync: [receiver] write failed on "/Volumes/REPL/big.bin": No space left on device (28)`,
			path: "big.bin", cause: "No space left on device", hint: "full",
		},
		{
			line: `file has vanished: "/Volumes/REPL/tmp/x"`,
			path: "tmp/x", cause: "vanished", hint: "disappeared",
		},
		{
			line:    `rsync error: some files/attrs were not transferred (see previous errors) (code 23) at main.c(1412) [sender=3.5.1]`,
			summary: true,
		},
	}
	for _, c := range cases {
		p := ParseError(c.line, src, dst)
		if p.Summary != c.summary {
			t.Errorf("%q: summary = %v", c.line, p.Summary)
			continue
		}
		if c.summary {
			continue
		}
		if p.Path != c.path || p.Cause != c.cause || !strings.Contains(p.Hint, c.hint) {
			t.Errorf("%q\n got  path=%q cause=%q hint=%q\n want path=%q cause=%q hint~%q", c.line, p.Path, p.Cause, p.Hint, c.path, c.cause, c.hint)
		}
	}
}

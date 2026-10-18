//go:build darwin

package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tryshchenko/btpair/internal/meta"
)

func lock(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Chflags(path, lockFlags&0x2); err != nil {
		t.Fatal(err)
	}
}

func locked(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Sys().(*syscall.Stat_t).Flags&lockFlags != 0
}

// unlockAll lets t.TempDir clean up after a test that locked files.
func unlockAll(t *testing.T, root string) {
	t.Cleanup(func() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil {
				syscall.Chflags(p, 0)
			}
			return nil
		})
	})
}

func TestLockedReplicaFilesAreReplacedAndDeleted(t *testing.T) {
	f := newFixture(t,
		map[string]string{"Music/song.mp3": "new and longer", "keep.txt": "k"},
		map[string]string{"Music/song.mp3": "old", "gone.txt": "stale"})
	unlockAll(t, f.root)
	lock(t, filepath.Join(f.replica(), "Music/song.mp3"))
	lock(t, filepath.Join(f.replica(), "gone.txt"))

	rec, evs := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if b, _ := os.ReadFile(filepath.Join(f.replica(), "Music/song.mp3")); string(b) != "new and longer" {
		t.Errorf("locked replica file not replaced: %q", b)
	}
	if exists(filepath.Join(f.replica(), "gone.txt")) {
		t.Error("locked replica file not deleted")
	}
	if !strings.Contains(logText(evs), "unlocked 2 locked items") {
		t.Errorf("unlock not reported:\n%s", logText(evs))
	}
}

func TestMainLocksAreNeverTouched(t *testing.T) {
	f := newFixture(t, map[string]string{"a.txt": "main"}, nil)
	unlockAll(t, f.root)
	p := filepath.Join(f.main(), "a.txt")
	lock(t, p)

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if !locked(p) {
		t.Error("main file was unlocked")
	}
	if b, _ := os.ReadFile(filepath.Join(f.replica(), "a.txt")); string(b) != "main" {
		t.Errorf("locked main file not copied: %q", b)
	}
}

func TestErrorsAreGroupedPerCause(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	files := map[string]string{"ok.txt": "fine"}
	for i := range 6 {
		files[filepath.Join("Private", "f"+string(rune('a'+i))+".txt")] = "x"
	}
	f := newFixture(t, files, nil)
	var paths []string
	for p := range files {
		if strings.HasPrefix(p, "Private/") {
			full := filepath.Join(f.main(), p)
			os.Chmod(full, 0o000)
			paths = append(paths, full)
		}
	}
	t.Cleanup(func() {
		for _, p := range paths {
			os.Chmod(p, 0o644)
		}
	})

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusPartial)
	var shown, summary int
	for _, e := range rec.Errors {
		switch {
		case strings.Contains(e, "…and 3 more files in Private failed the same way (Permission denied)"):
			summary++
		case strings.Contains(e, "Permission denied — no permission"):
			shown++
		}
	}
	if shown != perCause || summary != 1 || len(rec.Errors) != perCause+1 {
		t.Errorf("want %d detailed errors and one summary, got:\n%s", perCause, strings.Join(rec.Errors, "\n"))
	}
	if len(rec.Pending) != 6 {
		t.Fatalf("pending = %+v", rec.Pending)
	}
	for _, p := range rec.Pending {
		if !strings.Contains(p.Path, "Private/f") || p.Reason != "Permission denied" {
			t.Errorf("pending item = %+v", p)
		}
	}
}

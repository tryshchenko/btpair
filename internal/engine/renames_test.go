package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/meta"
)

// trashed lists the files in the replica's trash for one run.
func trashed(t *testing.T, f *fixture, runID string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(meta.TrashDir(f.replica()), runID)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func mirrored(t *testing.T, f *fixture) {
	t.Helper()
	if got, want := tree(t, f.replica()), tree(t, f.main()); got != want {
		t.Fatalf("replica is not a mirror\nreplica:\n%s\nmain:\n%s", got, want)
	}
}

// A renamed folder is renamed on the replica: nothing is copied again and
// nothing lands in trash.
func TestFolderRenameIsNotRecopied(t *testing.T) {
	files := map[string]string{"keep.txt": "k"}
	for i := 0; i < 120; i++ {
		files[fmt.Sprintf("Holiday 2014/IMG_%03d.jpg", i)] = strings.Repeat(string(rune('a'+i%26)), 1000+i)
	}
	f := newFixture(t, files, nil)
	f.setPair(config.PairConfig{MaxDeletePercent: 25})
	mustStatus(t, mustRun(t, f), meta.StatusOK)

	os.MkdirAll(filepath.Join(f.main(), "Trips"), 0o755)
	if err := os.Rename(filepath.Join(f.main(), "Holiday 2014"), filepath.Join(f.main(), "Trips/Italy 2014")); err != nil {
		t.Fatal(err)
	}
	// No "allow large deletions": after renaming there is nothing to delete.
	rec, evs := f.run()
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 120 || rec.Stats.BytesTransferred != 0 {
		t.Errorf("renamed %d, transferred %d bytes; want 120 renamed and nothing copied", rec.Stats.FilesRenamed, rec.Stats.BytesTransferred)
	}
	if got := trashed(t, f, rec.RunID); len(got) != 0 {
		t.Errorf("renamed files went to trash: %v", got[:min(5, len(got))])
	}
	var shown bool
	for _, ev := range evs {
		if l, ok := ev.(Log); ok && l.Kind == KindRename && strings.Contains(l.Text, "Holiday 2014/IMG_000.jpg → Trips/Italy 2014/IMG_000.jpg") {
			shown = true
		}
	}
	if !shown {
		t.Error("renames were not reported")
	}
}

func mustRun(t *testing.T, f *fixture) meta.RunRecord {
	t.Helper()
	rec, _ := f.run()
	return rec
}

func TestFileRenameAndMove(t *testing.T) {
	f := newFixture(t, map[string]string{"a/video.mov": strings.Repeat("v", 300_000), "b/x.txt": "x"}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	os.Rename(filepath.Join(f.main(), "a/video.mov"), filepath.Join(f.main(), "b/renamed.mov"))
	rec := mustRun(t, f)
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 1 || rec.Stats.BytesTransferred != 0 {
		t.Errorf("renamed %d, transferred %d", rec.Stats.FilesRenamed, rec.Stats.BytesTransferred)
	}
}

// Same size and timestamp are not enough: the contents must agree too.
func TestLookalikeWithDifferentContentIsCopied(t *testing.T) {
	f := newFixture(t, map[string]string{"old.bin": strings.Repeat("A", 200_000)}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	os.Remove(filepath.Join(f.main(), "old.bin"))
	writeFiles(t, f.main(), map[string]string{"new.bin": strings.Repeat("B", 200_000)}) // same size, same mtime (t0)
	rec, evs := f.run(allowDelete)
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 0 {
		t.Fatal("a different file was treated as a rename")
	}
	if got := trashed(t, f, rec.RunID); len(got) != 1 || got[0] != "old.bin" {
		t.Errorf("trash = %v; the deleted file must be kept", got)
	}
	if !strings.Contains(logText(evs), "differ in content") {
		t.Error("the mismatch was not mentioned")
	}
}

// Several same-size, same-time candidates: pair by name where unique,
// leave the rest to rsync.
func TestAmbiguousCandidatesFallBackToCopy(t *testing.T) {
	same := strings.Repeat("c", 50_000)
	f := newFixture(t, map[string]string{"in/p.bin": same, "in/q.bin": same + "", "in/clip.mov": strings.Repeat("m", 70_000)}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	// p and q (identical size and time) both renamed to unrelated names;
	// clip.mov moved, keeping its name.
	os.MkdirAll(filepath.Join(f.main(), "out"), 0o755)
	os.Rename(filepath.Join(f.main(), "in/p.bin"), filepath.Join(f.main(), "out/r.bin"))
	os.Rename(filepath.Join(f.main(), "in/q.bin"), filepath.Join(f.main(), "out/s.bin"))
	os.Rename(filepath.Join(f.main(), "in/clip.mov"), filepath.Join(f.main(), "out/clip.mov"))
	rec, _ := f.run(allowDelete)
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 1 {
		t.Errorf("renamed %d; want only the unambiguous clip.mov", rec.Stats.FilesRenamed)
	}
}

func TestDryRunOnlyReportsRenames(t *testing.T) {
	f := newFixture(t, map[string]string{"a.mov": strings.Repeat("v", 100_000), "k": "k"}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	os.Rename(filepath.Join(f.main(), "a.mov"), filepath.Join(f.main(), "b.mov"))
	before := tree(t, f.replica())
	rec, evs := f.run(dryRun)
	mustStatus(t, rec, meta.StatusDryRun)
	if tree(t, f.replica()) != before {
		t.Fatal("dry run renamed files")
	}
	if !strings.Contains(logText(evs), "would rename 1 files") {
		t.Errorf("dry run did not report the rename:\n%s", logText(evs))
	}
}

func TestRenameDetectionCanBeTurnedOff(t *testing.T) {
	f := newFixture(t, map[string]string{"a.mov": strings.Repeat("v", 100_000), "k": "k"}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	f.setPair(config.PairConfig{NoRenameDetection: true})
	os.Rename(filepath.Join(f.main(), "a.mov"), filepath.Join(f.main(), "b.mov"))
	rec := mustRun(t, f)
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 0 || rec.Stats.BytesTransferred != 100_000 {
		t.Errorf("renamed %d, transferred %d; want a plain copy", rec.Stats.FilesRenamed, rec.Stats.BytesTransferred)
	}
}

// Renaming a big folder needs no free space on the replica.
func TestRenameNeedsNoFreeSpace(t *testing.T) {
	smallDrives(t, 300<<10)
	f := newFixture(t, map[string]string{"Big/a.bin": kib(100), "Big/b.bin": kib(90)}, nil)
	mustStatus(t, mustRun(t, f), meta.StatusOK)
	os.Rename(filepath.Join(f.main(), "Big"), filepath.Join(f.main(), "Bigger"))
	rec := mustRun(t, f)
	mustStatus(t, rec, meta.StatusOK)
	mirrored(t, f)
	if rec.Stats.FilesRenamed != 2 {
		t.Errorf("renamed %d", rec.Stats.FilesRenamed)
	}
}

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/volume"
)

func smallDrives(t *testing.T, capacity uint64) {
	old := volume.DevCapacity
	volume.DevCapacity = capacity
	t.Cleanup(func() { volume.DevCapacity = old })
}

func kib(n int) string { return strings.Repeat("x", n<<10) }

func trashIDs(t *testing.T, f *fixture) []string {
	t.Helper()
	runs, err := meta.ListTrash(f.replica())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// The user deletes a lot on main, then fills the space with new files. The
// deleted files sit in the replica's trash; the next sync must empty that
// trash to make room instead of refusing forever.
func TestTrashMakesRoomForNewFiles(t *testing.T) {
	smallDrives(t, 400<<10)
	f := newFixture(t, map[string]string{"junk/a.bin": kib(100), "junk/b.bin": kib(100), "keep.txt": "k"}, nil)
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)

	os.RemoveAll(filepath.Join(f.main(), "junk"))
	rec, _ = f.run(allowDelete)
	mustStatus(t, rec, meta.StatusOK)
	junkRun := rec.RunID
	if !exists(filepath.Join(meta.TrashDir(f.replica()), junkRun, "junk", "a.bin")) {
		t.Fatal("deleted files did not go to trash")
	}

	writeFiles(t, f.main(), map[string]string{"useful/c.bin": kib(100), "useful/d.bin": kib(100)})
	rec, evs := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if got, want := tree(t, f.replica()), tree(t, f.main()); got != want {
		t.Fatalf("replica is not a mirror\nreplica:\n%s\nmain:\n%s", got, want)
	}
	if exists(filepath.Join(meta.TrashDir(f.replica()), junkRun)) {
		t.Fatal("old trash was not emptied")
	}
	if log := logText(evs); !strings.Contains(log, "emptied trash from") || !strings.Contains(log, "to make room") {
		t.Errorf("emptying was not reported:\n%s", log)
	}
}

func TestDryRunOnlyReportsTrashItWouldEmpty(t *testing.T) {
	smallDrives(t, 400<<10)
	f := newFixture(t, map[string]string{"keep.txt": "k", "new.bin": kib(200)}, nil)
	writeFiles(t, filepath.Join(meta.TrashDir(f.replica()), "20250101-000000.000"), map[string]string{"old.bin": kib(200)})

	rec, evs := f.run(dryRun)
	mustStatus(t, rec, meta.StatusDryRun)
	if ids := trashIDs(t, f); len(ids) != 1 {
		t.Fatalf("dry run emptied trash: %v", ids)
	}
	if !strings.Contains(logText(evs), "would empty trash from") {
		t.Errorf("dry run did not say it would empty trash:\n%s", logText(evs))
	}
}

func TestEmptiesOldestTrashFirstAndOnlyAsNeeded(t *testing.T) {
	smallDrives(t, 500<<10)
	f := newFixture(t, map[string]string{"keep.txt": "k", "new.bin": kib(150)}, nil)
	for _, id := range []string{"20250101-000000.000", "20250201-000000.000", "20250301-000000.000"} {
		writeFiles(t, filepath.Join(meta.TrashDir(f.replica()), id), map[string]string{"old.bin": kib(120)})
	}
	// 500 KiB drive, 360 KiB of trash: ~140 KiB free, 150 KiB needed. One
	// run's worth is enough.
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	ids := trashIDs(t, f)
	if len(ids) != 2 || ids[0] != "20250201-000000.000" || ids[1] != "20250301-000000.000" {
		t.Fatalf("trash left = %v; want only the oldest run emptied", ids)
	}
}

func TestRefusesWhenEvenEmptyTrashIsNotEnough(t *testing.T) {
	smallDrives(t, 300<<10)
	f := newFixture(t, map[string]string{"keep.txt": "k", "huge.bin": kib(400)}, nil)
	writeFiles(t, filepath.Join(meta.TrashDir(f.replica()), "20250101-000000.000"), map[string]string{"old.bin": kib(50)})

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "would not be enough") {
		t.Errorf("message %q does not explain that trash would not help", rec.Message)
	}
	if ids := trashIDs(t, f); len(ids) != 1 {
		t.Fatal("trash emptied although the copy could not fit anyway")
	}
	if exists(filepath.Join(f.replica(), "huge.bin")) {
		t.Fatal("copied despite insufficient space")
	}
}

func TestNoTrashPairStillRefusesWhenFull(t *testing.T) {
	smallDrives(t, 64<<10)
	f := newFixture(t, map[string]string{"big.bin": kib(100)}, nil)
	f.setPair(config.PairConfig{NoTrash: true})
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "free") {
		t.Errorf("message %q does not mention free space", rec.Message)
	}
}

// Deleting a few big files is a large deletion even though the file count
// is small.
func TestMassDeleteGuardCountsSize(t *testing.T) {
	old := massDeleteMinBytes
	massDeleteMinBytes = 100 << 10
	t.Cleanup(func() { massDeleteMinBytes = old })

	files := map[string]string{"films/huge.mkv": kib(300)}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("docs/%02d.txt", i)] = "small"
	}
	f := newFixture(t, files, files)
	f.setPair(config.PairConfig{MaxDeletePercent: 25})
	os.Remove(filepath.Join(f.main(), "films", "huge.mkv"))
	before := tree(t, f.replica())

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "would delete 307.2 kB of") {
		t.Errorf("message %q does not state the size", rec.Message)
	}
	if tree(t, f.replica()) != before {
		t.Fatal("replica modified despite the guard")
	}
	rec, _ = f.run(allowDelete)
	mustStatus(t, rec, meta.StatusOK)
	if exists(filepath.Join(f.replica(), "films", "huge.mkv")) {
		t.Fatal("allowed run did not delete")
	}
}

// Runs that delete nothing must not push real deletions out of the trash.
func TestQuietRunsDoNotExpireTrash(t *testing.T) {
	f := newFixture(t, map[string]string{"keep.txt": "k"}, map[string]string{"keep.txt": "k", "gone.txt": "deleted on main"})
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	deleted := rec.RunID
	for i := 0; i < 7; i++ { // more than trash_keep (5); each copies, none deletes
		writeFiles(t, f.main(), map[string]string{fmt.Sprintf("new%d.txt", i): "n"})
		rec, _ = f.run()
		mustStatus(t, rec, meta.StatusOK)
	}
	if ids := trashIDs(t, f); len(ids) != 1 || ids[0] != deleted {
		t.Fatalf("trash = %v; want only %s", ids, deleted)
	}
}

func TestEmptyTrash(t *testing.T) {
	f := newFixture(t, map[string]string{"keep.txt": "k"}, nil)
	writeFiles(t, filepath.Join(meta.TrashDir(f.replica()), "20250101-000000.000"), map[string]string{"a": "12", "d/b": "345"})
	writeFiles(t, filepath.Join(meta.TrashDir(f.replica()), "20250201-000000.000"), map[string]string{"c": "6789"})
	runs, _ := meta.ListTrash(f.replica())
	if s := TrashSummary(runs); s != "9 B from 2 runs, oldest 1 Jan 2025" {
		t.Errorf("TrashSummary = %q", s)
	}

	// Never while a sync holds the pair.
	lock, err := lockPair(f.replica())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := EmptyTrash(f.pair); err == nil {
		t.Fatal("emptied trash while the pair was locked")
	}
	lock.Close()

	files, bytes, err := EmptyTrash(f.pair)
	if err != nil || files != 3 || bytes != 9 {
		t.Fatalf("EmptyTrash = %d files, %d bytes, %v", files, bytes, err)
	}
	if ids := trashIDs(t, f); len(ids) != 0 {
		t.Fatalf("trash left: %v", ids)
	}
	if TrashSummary(nil) != "" {
		t.Error("empty trash should summarize to nothing")
	}
}

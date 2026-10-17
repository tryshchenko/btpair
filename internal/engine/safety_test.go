package engine

// Safety invariants. If any of these fail, a sync could lose data.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/fsutil"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/steps"
	"github.com/tryshchenko/btpair/internal/volume"
)

var drifted = struct{ main, replica map[string]string }{
	main: map[string]string{
		"keep.txt":          "same on both",
		"dir/new.txt":       "only on main",
		"changed.txt":       "version two, longer",
		"deep/a/b/c/d.txt":  "nested",
		"spaces in name.md": "spaces",
		"ünïcode/файл.txt":  "unicode",
	},
	replica: map[string]string{
		"keep.txt":          "same on both",
		"changed.txt":       "version one",
		"replica-only.txt":  "stale",
		"old/dir/stale.bin": "stale tree",
	},
}

func TestSyncMakesReplicaAnExactMirror(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if got, want := tree(t, f.replica()), tree(t, f.main()); got != want {
		t.Fatalf("replica is not a mirror\nreplica:\n%s\nmain:\n%s", got, want)
	}
}

func TestSyncNeverModifiesMain(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	before := tree(t, f.main())
	idBefore, _ := os.ReadFile(meta.IdentityPath(f.main()))

	for i := 0; i < 2; i++ { // the second run takes the "already in sync" path
		rec, _ := f.run()
		mustStatus(t, rec, meta.StatusOK)
	}
	if after := tree(t, f.main()); after != before {
		t.Fatalf("main changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if exists(filepath.Join(f.main(), "replica-only.txt")) {
		t.Fatal("a replica-only file appeared on main")
	}
	idAfter, _ := os.ReadFile(meta.IdentityPath(f.main()))
	if string(idBefore) != string(idAfter) {
		t.Fatal("main identity was rewritten")
	}
}

func TestReplicaFilesAreTrashedNotDestroyed(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)

	trash := filepath.Join(meta.TrashDir(f.replica()), rec.RunID)
	for path, want := range map[string]string{
		"replica-only.txt":  "stale",
		"old/dir/stale.bin": "stale tree",
		"changed.txt":       "version one", // overwritten files are kept too
	} {
		got, err := os.ReadFile(filepath.Join(trash, path))
		if err != nil || string(got) != want {
			t.Errorf("trash/%s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestNoTrashOptionDeletesOutright(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	f.setPair(config.PairConfig{NoTrash: true})
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if exists(filepath.Join(meta.TrashDir(f.replica()), rec.RunID)) {
		t.Fatal("trash created despite no_trash")
	}
}

func TestMetadataNeverCrossesDrives(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	// Main-only metadata (step state) must not leak to the replica.
	writeFiles(t, filepath.Join(f.main(), meta.Dir, "steps", "x"), map[string]string{"state": "main secret"})

	for i := 0; i < 3; i++ {
		rec, _ := f.run()
		mustStatus(t, rec, meta.StatusOK)
		writeFiles(t, f.main(), map[string]string{"churn.txt": strings.Repeat("x", i+1)})
	}

	for vol, want := range map[string]meta.Role{f.main(): meta.RoleMain, f.replica(): meta.RoleReplica} {
		id, err := meta.LoadIdentity(vol)
		if err != nil || id == nil || id.Role != want {
			t.Fatalf("%s identity = %+v, %v; want role %s", vol, id, err, want)
		}
	}
	if exists(filepath.Join(f.replica(), meta.Dir, "steps", "x")) {
		t.Fatal("main step state was copied to replica")
	}
	// The replica's own history and trash survive later syncs.
	runs, _ := meta.ListRuns(f.replica(), 0)
	if len(runs) != 3 {
		t.Fatalf("replica has %d run records, want 3", len(runs))
	}
	if !exists(meta.TrashDir(f.replica())) {
		t.Fatal("replica trash was deleted by a later sync")
	}
}

func TestRefusesEmptyMain(t *testing.T) {
	f := newFixture(t, map[string]string{".DS_Store": "x"}, drifted.replica)
	before := tree(t, f.replica())
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "empty") {
		t.Errorf("message %q does not explain the empty main", rec.Message)
	}
	if tree(t, f.replica()) != before {
		t.Fatal("replica modified")
	}
}

func TestMassDeleteGuard(t *testing.T) {
	replica := map[string]string{"keep.txt": "k"}
	for i := 0; i < 100; i++ {
		replica["old/"+strings.Repeat("f", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"] = "old"
	}
	f := newFixture(t, map[string]string{"keep.txt": "k"}, replica)
	f.setPair(config.PairConfig{MaxDeletePercent: 25})
	before := tree(t, f.replica())

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "would delete") {
		t.Errorf("message %q does not mention deletions", rec.Message)
	}
	if tree(t, f.replica()) != before {
		t.Fatal("replica modified despite the guard")
	}

	rec, _ = f.run(allowDelete)
	mustStatus(t, rec, meta.StatusOK)
	if tree(t, f.replica()) != tree(t, f.main()) {
		t.Fatal("allowed run did not mirror")
	}
}

func TestFreeSpaceGuard(t *testing.T) {
	old := volume.DevCapacity
	volume.DevCapacity = 64 << 10 // 64 KiB "drives"
	t.Cleanup(func() { volume.DevCapacity = old })

	f := newFixture(t, map[string]string{"big.bin": strings.Repeat("x", 100<<10)}, nil)
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "free") {
		t.Errorf("message %q does not mention free space", rec.Message)
	}
	if exists(filepath.Join(f.replica(), "big.bin")) {
		t.Fatal("copied despite insufficient space")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	step := &fakeStep{id: "s", writes: map[string]string{"from-step.txt": "x"}}
	m, r := tree(t, f.main()), tree(t, f.replica())
	stateBefore, _ := os.ReadFile(meta.StatePath(f.replica()))

	rec, evs := f.run(dryRun, withSteps(step))
	mustStatus(t, rec, meta.StatusDryRun)
	if tree(t, f.main()) != m || tree(t, f.replica()) != r {
		t.Fatal("dry run modified a drive")
	}
	if step.runs != 0 || step.plans != 1 {
		t.Fatalf("dry run: step ran %d times, planned %d; want 0 and 1", step.runs, step.plans)
	}
	stateAfter, _ := os.ReadFile(meta.StatePath(f.replica()))
	if string(stateBefore) != string(stateAfter) || exists(meta.RunsDir(f.replica())) {
		t.Fatal("dry run wrote metadata")
	}
	// The plan is still reported, including what would be deleted.
	var plan bool
	for _, ev := range evs {
		if p, ok := ev.(Planned); ok && p.Plan.FilesToDelete > 0 && p.Plan.FilesToCopy > 0 {
			plan = true
		}
	}
	if !plan {
		t.Error("dry run did not report a plan with copies and deletions")
	}
}

func TestPathsLimitScope(t *testing.T) {
	f := newFixture(t,
		map[string]string{"Photos/a.jpg": "a", "Docs/b.txt": "b", "Other/c.txt": "c"},
		map[string]string{"Photos/stale.jpg": "s", "Unmanaged/keep.txt": "replica's own"},
	)
	f.setPair(config.PairConfig{Paths: []string{"Photos", "Docs/"}})
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)

	for path, want := range map[string]bool{
		"Photos/a.jpg":       true,
		"Docs/b.txt":         true,
		"Other/c.txt":        false, // outside paths: not copied
		"Photos/stale.jpg":   false, // inside paths: mirrored away
		"Unmanaged/keep.txt": true,  // outside paths: never deleted
	} {
		if got := exists(filepath.Join(f.replica(), path)); got != want {
			t.Errorf("replica %s exists = %v, want %v", path, got, want)
		}
	}
}

func TestExcludes(t *testing.T) {
	f := newFixture(t, map[string]string{
		"a.txt": "a", "build/x.tmp": "tmp", ".DS_Store": "finder", "sub/._resource": "apple double",
	}, nil)
	f.setPair(config.PairConfig{Exclude: []string{"*.tmp"}})
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if !exists(filepath.Join(f.replica(), "a.txt")) {
		t.Fatal("regular file not copied")
	}
	for _, p := range []string{"build/x.tmp", ".DS_Store", "sub/._resource"} {
		if exists(filepath.Join(f.replica(), p)) {
			t.Errorf("excluded %s was copied", p)
		}
	}
}

func TestChecksumCatchesSameSizeSameMtime(t *testing.T) {
	f := newFixture(t, map[string]string{"f.txt": "AAAA"}, map[string]string{"f.txt": "BBBB"})
	// Same size, same mtime: the quick check cannot see the difference.
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if got, _ := os.ReadFile(filepath.Join(f.replica(), "f.txt")); string(got) != "BBBB" {
		t.Skip("rsync quick check unexpectedly detected the change")
	}
	rec, _ = f.run(func(r *Request) { r.Checksum = true })
	mustStatus(t, rec, meta.StatusOK)
	if got, _ := os.ReadFile(filepath.Join(f.replica(), "f.txt")); string(got) != "AAAA" {
		t.Fatalf("checksum run left %q", got)
	}
}

func TestUnreadableFileGivesPartialWithPending(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	f := newFixture(t, map[string]string{"ok.txt": "fine", "secret.txt": "locked"}, nil)
	locked := filepath.Join(f.main(), "secret.txt")
	os.Chmod(locked, 0o000)
	t.Cleanup(func() { os.Chmod(locked, 0o644) })

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusPartial)
	if !exists(filepath.Join(f.replica(), "ok.txt")) {
		t.Error("readable file was not copied")
	}
	var pending bool
	for _, p := range rec.Pending {
		pending = pending || (p.Kind == "file" && strings.Contains(p.Path, "secret.txt"))
	}
	if !pending {
		t.Errorf("unreadable file not recorded as pending: %+v", rec.Pending)
	}
	if st, _ := meta.LoadState(f.replica()); st.Status != meta.StatusPartial || len(st.Pending) == 0 {
		t.Errorf("replica state = %s with %d pending", st.Status, len(st.Pending))
	}
}

func TestCancelMidSync(t *testing.T) {
	f := newFixture(t, map[string]string{"big.bin": strings.Repeat("z", 8<<20)}, nil)
	f.setPair(config.PairConfig{BWLimit: "1M"}) // ~8s transfer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1500 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	rec, _ := f.runCtx(ctx)
	mustStatus(t, rec, meta.StatusCancelled)
	if time.Since(start) > 6*time.Second {
		t.Error("cancellation took too long")
	}
	if len(rec.Errors) > 0 {
		t.Errorf("cancellation produced errors: %v", rec.Errors)
	}
	for _, v := range []string{f.main(), f.replica()} {
		if st, _ := meta.LoadState(v); st.Status != meta.StatusCancelled {
			t.Errorf("%s state = %s, want cancelled", v, st.Status)
		}
	}
	// A cancelled run must not leave a truncated file posing as complete.
	if b, err := os.ReadFile(filepath.Join(f.replica(), "big.bin")); err == nil && len(b) != 8<<20 {
		t.Fatalf("partial file left on replica (%d bytes)", len(b))
	}
}

func TestInterruptedRunIsDetectedAndCompleted(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	good := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	crashed := meta.State{Status: meta.StatusInProgress, RunID: "20250101-000000", StartedAt: time.Now().Add(-time.Hour), LastSuccessAt: good}
	for _, v := range []string{f.main(), f.replica()} {
		meta.SaveState(v, crashed)
	}
	f.redetect()

	rec, evs := f.run()
	mustStatus(t, rec, meta.StatusOK)
	if !strings.Contains(logText(evs), "did not finish") {
		t.Error("interrupted run was not reported")
	}
	if tree(t, f.replica()) != tree(t, f.main()) {
		t.Fatal("not mirrored after recovery")
	}
}

func TestStateIsInProgressDuringRun(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	seen := map[string]meta.Status{}
	step := &probeStep{fakeStep: fakeStep{id: "probe"}, probe: func() {
		for _, v := range []string{f.main(), f.replica()} {
			st, _ := meta.LoadState(v)
			seen[v] = st.Status
		}
	}}
	rec, _ := f.run(withSteps(step))
	mustStatus(t, rec, meta.StatusOK)
	for v, st := range seen {
		if st != meta.StatusInProgress {
			t.Errorf("%s state during run = %s, want in_progress", v, st)
		}
	}
	if len(seen) != 2 {
		t.Fatal("probe did not run")
	}
}

type probeStep struct {
	fakeStep
	probe func()
}

func (p *probeStep) Run(ctx context.Context, env steps.Env, r steps.Reporter) (string, error) {
	p.probe()
	return p.fakeStep.Run(ctx, env, r)
}

func TestConcurrentRunsAreBlocked(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	lock, err := lockPair(f.replica())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	before := tree(t, f.replica())
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if !strings.Contains(rec.Message, "already") {
		t.Errorf("message %q", rec.Message)
	}
	if tree(t, f.replica()) != before {
		t.Fatal("replica modified while locked")
	}
}

func TestDriveSwappedAfterDetectionIsRefused(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	// Between detection and running, the replica's identity changes (e.g. a
	// different drive was mounted at the same path).
	var id meta.Identity
	fsutil.ReadJSON(meta.IdentityPath(f.replica()), &id)
	id.VolumeUUID = "SOMETHING-ELSE"
	meta.SaveIdentity(f.replica(), id)
	before := tree(t, f.replica())

	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusFailed)
	if tree(t, f.replica()) != before {
		t.Fatal("replica modified")
	}
}

func TestTrashIsPruned(t *testing.T) {
	f := newFixture(t, map[string]string{"f.txt": "0"}, nil)
	f.setPair(config.PairConfig{TrashKeep: 2})
	for i := 1; i <= 4; i++ {
		writeFiles(t, f.main(), map[string]string{"f.txt": strings.Repeat("v", i)})
		rec, _ := f.run()
		mustStatus(t, rec, meta.StatusOK)
	}
	entries, _ := os.ReadDir(meta.TrashDir(f.replica()))
	if len(entries) != 2 {
		t.Fatalf("trash holds %d runs, want 2", len(entries))
	}
}

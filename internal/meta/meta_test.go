package meta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIdentityRoundTrip(t *testing.T) {
	vol := t.TempDir()
	if id, err := LoadIdentity(vol); id != nil || err != nil {
		t.Fatalf("unmanaged drive: got %v, %v", id, err)
	}
	want := Identity{PairID: "p", Role: RoleReplica, VolumeUUID: "U1", PeerUUID: "U2", Label: "L", CreatedAt: time.Now().Truncate(time.Second)}
	if err := SaveIdentity(vol, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadIdentity(vol)
	if err != nil {
		t.Fatal(err)
	}
	want.Version = schemaVersion
	if *got != want {
		t.Fatalf("got %+v\nwant %+v", *got, want)
	}
}

func TestCorruptIdentityIsAnError(t *testing.T) {
	vol := t.TempDir()
	os.MkdirAll(filepath.Join(vol, Dir), 0o755)
	os.WriteFile(IdentityPath(vol), []byte("{not json"), 0o644)
	if _, err := LoadIdentity(vol); err == nil {
		t.Fatal("corrupt identity must not be treated as unmanaged")
	}
}

// The on-disk format is read by other tools and future versions; keep the
// field names stable.
func TestOnDiskFieldNames(t *testing.T) {
	vol := t.TempDir()
	SaveIdentity(vol, Identity{PairID: "p", Role: RoleMain, VolumeUUID: "u", PeerUUID: "v"})
	SaveState(vol, State{Status: StatusOK, RunID: "r", StartedAt: time.Now(), Pending: []PendingItem{{Kind: "file", Path: "x", Reason: "y"}}})
	id, _ := os.ReadFile(IdentityPath(vol))
	st, _ := os.ReadFile(StatePath(vol))
	for _, f := range []string{`"pair_id"`, `"role": "main"`, `"volume_uuid"`, `"peer_uuid"`, `"version"`} {
		if !strings.Contains(string(id), f) {
			t.Errorf("identity.json missing %s:\n%s", f, id)
		}
	}
	for _, f := range []string{`"status": "ok"`, `"run_id"`, `"started_at"`, `"pending"`, `"stats"`} {
		if !strings.Contains(string(st), f) {
			t.Errorf("state.json missing %s:\n%s", f, st)
		}
	}
	// Zero times are omitted rather than written as year 0001.
	if strings.Contains(string(st), "0001-01-01") {
		t.Errorf("zero time serialized:\n%s", st)
	}
}

func TestStateDefaultsToNever(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil || st.Status != StatusNever {
		t.Fatalf("got %+v, %v", st, err)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	vol := t.TempDir()
	for i := 0; i < 5; i++ {
		SaveState(vol, State{Status: StatusOK})
	}
	entries, _ := os.ReadDir(filepath.Join(vol, Dir))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestRunIDsSortChronologicallyAndAreUnique(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a := NewRunID(base)
	b := NewRunID(base.Add(time.Millisecond))
	c := NewRunID(base.Add(time.Second))
	if !(a < b && b < c) {
		t.Fatalf("not sortable: %s %s %s", a, b, c)
	}
	if strings.ContainsAny(a, "/: ") {
		t.Fatalf("run id %q is not filename-safe", a)
	}
}

func TestListRunsNewestFirstWithLimit(t *testing.T) {
	vol := t.TempDir()
	base := time.Now()
	for i := 0; i < 5; i++ {
		SaveRun(vol, RunRecord{State: State{RunID: NewRunID(base.Add(time.Duration(i) * time.Second)), Status: StatusOK}})
	}
	os.WriteFile(filepath.Join(RunsDir(vol), "junk.txt"), []byte("x"), 0o644)
	runs, err := ListRuns(vol, 3)
	if err != nil || len(runs) != 3 {
		t.Fatalf("got %d runs, %v", len(runs), err)
	}
	if !(runs[0].RunID > runs[1].RunID && runs[1].RunID > runs[2].RunID) {
		t.Errorf("not newest first: %s %s %s", runs[0].RunID, runs[1].RunID, runs[2].RunID)
	}
	if none, err := ListRuns(t.TempDir(), 0); none != nil || err != nil {
		t.Errorf("empty drive: %v %v", none, err)
	}
}

func TestPruneTrashKeepsNewest(t *testing.T) {
	vol := t.TempDir()
	for _, n := range []string{"20260101-000000.000", "20260102-000000.000", "20260103-000000.000", "20260104-000000.000"} {
		os.MkdirAll(filepath.Join(TrashDir(vol), n, "x"), 0o755)
	}
	if err := PruneTrash(vol, 2); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(TrashDir(vol))
	if len(entries) != 2 || entries[0].Name() != "20260103-000000.000" {
		t.Fatalf("kept %v", entries)
	}
	if err := PruneTrash(t.TempDir(), 2); err != nil {
		t.Errorf("missing trash dir: %v", err)
	}
}

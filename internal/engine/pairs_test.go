package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryshchenko/backup-tool/internal/meta"
	"github.com/tryshchenko/backup-tool/internal/volume"
)

func detect(t *testing.T, root string) []*volume.Volume {
	t.Helper()
	vols, err := volume.Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return vols
}

func TestRolesComeFromMetadataNotNames(t *testing.T) {
	root := t.TempDir()
	// Names suggest the opposite of the roles we assign.
	for _, n := range []string{"I-AM-MAIN", "I-AM-REPLICA"} {
		os.MkdirAll(filepath.Join(root, n), 0o755)
	}
	vols := detect(t, root)
	if err := InitPair(vols[1], vols[0], "p", ""); err != nil {
		t.Fatal(err)
	}
	pairs, unpaired := Group(detect(t, root))
	if len(pairs) != 1 || len(unpaired) != 0 {
		t.Fatalf("pairs=%d unpaired=%d", len(pairs), len(unpaired))
	}
	p := pairs[0]
	if p.Main.Name != "I-AM-REPLICA" || p.Replica.Name != "I-AM-MAIN" || !p.Ready() {
		t.Fatalf("main=%s replica=%s problems=%v", p.Main.Name, p.Replica.Name, p.Problems)
	}
}

func TestGroupProblems(t *testing.T) {
	cases := map[string]func(t *testing.T, root string, m, r *volume.Volume) string{
		"cloned identity": func(t *testing.T, root string, m, r *volume.Volume) string {
			clone := filepath.Join(root, "C-CLONE")
			os.MkdirAll(filepath.Join(clone, meta.Dir), 0o755)
			b, _ := os.ReadFile(meta.IdentityPath(r.Path))
			os.WriteFile(meta.IdentityPath(clone), b, 0o644)
			return "cloned"
		},
		"two mains": func(t *testing.T, root string, m, r *volume.Volume) string {
			id, _ := meta.LoadIdentity(r.Path)
			id.Role = meta.RoleMain
			meta.SaveIdentity(r.Path, *id)
			return "two main"
		},
		"peer mismatch": func(t *testing.T, root string, m, r *volume.Volume) string {
			id, _ := meta.LoadIdentity(r.Path)
			id.PeerUUID = "NOT-THE-MAIN"
			meta.SaveIdentity(r.Path, *id)
			return "peers"
		},
		"unknown role (e.g. legacy metadata)": func(t *testing.T, root string, m, r *volume.Volume) string {
			id, _ := meta.LoadIdentity(r.Path)
			id.Role = "slave"
			meta.SaveIdentity(r.Path, *id)
			return "unknown role"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, n := range []string{"A-MAIN", "B-REPLICA"} {
				os.MkdirAll(filepath.Join(root, n), 0o755)
			}
			vols := detect(t, root)
			if err := InitPair(vols[0], vols[1], "p", ""); err != nil {
				t.Fatal(err)
			}
			want := mutate(t, root, vols[0], vols[1])
			pairs, _ := Group(detect(t, root))
			if len(pairs) != 1 || pairs[0].Ready() {
				t.Fatalf("pair should not be ready: %+v", pairs)
			}
			if !strings.Contains(strings.Join(pairs[0].Problems, " | "), want) {
				t.Errorf("problems %v do not mention %q", pairs[0].Problems, want)
			}
		})
	}
}

func TestInitPairRefusals(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"A", "B", "C"} {
		os.MkdirAll(filepath.Join(root, n), 0o755)
	}
	vols := detect(t, root)
	a, b, c := vols[0], vols[1], vols[2]

	for name, tc := range map[string]struct {
		m, r *volume.Volume
		id   string
	}{
		"same drive":  {a, a, "p"},
		"missing":     {a, nil, "p"},
		"empty id":    {a, b, ""},
		"bad id":      {a, b, "has space"},
		"slash in id": {a, b, "a/b"},
	} {
		if err := InitPair(tc.m, tc.r, tc.id, ""); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := InitPair(a, b, "p", ""); err != nil {
		t.Fatal(err)
	}
	// Already-managed drives cannot be re-paired by accident.
	if err := InitPair(a, c, "q", ""); err == nil {
		t.Error("re-paired a managed drive")
	}
	if err := InitPair(c, b, "q", ""); err == nil {
		t.Error("re-paired a managed drive")
	}
}

func TestPairStatePrefersNewest(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	rec, _ := f.run()
	mustStatus(t, rec, meta.StatusOK)
	// Simulate the replica having missed the final write: it still says in_progress
	// from an older run.
	old := *f.pair.Replica.State
	old.Status, old.StartedAt = meta.StatusInProgress, old.StartedAt.Add(-1e12)
	meta.SaveState(f.replica(), old)
	f.redetect()
	if st := f.pair.State(); st.Status != meta.StatusOK {
		t.Fatalf("State() = %s, want the newer ok state", st.Status)
	}
}

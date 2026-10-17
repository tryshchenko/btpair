package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tryshchenko/backup-tool/internal/config"
	"github.com/tryshchenko/backup-tool/internal/meta"
	"github.com/tryshchenko/backup-tool/internal/rsync"
	"github.com/tryshchenko/backup-tool/internal/steps"
	"github.com/tryshchenko/backup-tool/internal/volume"
)

// t0 is the mtime given to every fixture file, so identical files look
// identical to rsync's quick check.
var t0 = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

// fixture is a dev-mode pair: two folders acting as main and replica drives.
type fixture struct {
	t    *testing.T
	root string
	pair *Pair
	cfg  *config.Config
}

// newFixture creates a paired main ("A-MAIN") and replica ("B-REPLICA")
// holding the given files (path → content).
func newFixture(t *testing.T, mainFiles, replicaFiles map[string]string) *fixture {
	t.Helper()
	if _, _, err := rsync.Find(""); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	m, r := filepath.Join(root, "A-MAIN"), filepath.Join(root, "B-REPLICA")
	os.MkdirAll(m, 0o755)
	os.MkdirAll(r, 0o755)
	writeFiles(t, m, mainFiles)
	writeFiles(t, r, replicaFiles)

	vols, err := volume.Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitPair(vols[0], vols[1], "t", "test pair"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, cfg: &config.Config{Pairs: map[string]config.PairConfig{}, Steps: map[string]config.StepConfig{}}}
	f.redetect()
	return f
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(full, t0, t0)
	}
}

// redetect rescans the drives, as the UI does before each run.
func (f *fixture) redetect() {
	f.t.Helper()
	vols, err := volume.Detect(context.Background(), f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	pairs, _ := Group(vols)
	if len(pairs) != 1 {
		f.t.Fatalf("expected one pair, got %d", len(pairs))
	}
	f.pair = pairs[0]
}

func (f *fixture) main() string    { return f.pair.Main.Path }
func (f *fixture) replica() string { return f.pair.Replica.Path }

func (f *fixture) setPair(pc config.PairConfig) { f.cfg.Pairs["t"] = pc }

// run executes a request built from opts and returns the record and every
// event emitted.
func (f *fixture) run(opts ...func(*Request)) (meta.RunRecord, []Event) {
	f.t.Helper()
	return f.runCtx(context.Background(), opts...)
}

func (f *fixture) runCtx(ctx context.Context, opts ...func(*Request)) (meta.RunRecord, []Event) {
	f.t.Helper()
	req := Request{Pair: f.pair, Config: f.cfg}
	for _, o := range opts {
		o(&req)
	}
	ch := make(chan Event, 1024)
	go Run(ctx, req, ch)
	var evs []Event
	var rec meta.RunRecord
	for ev := range ch {
		evs = append(evs, ev)
		if fin, ok := ev.(Finished); ok {
			rec = fin.Record
		}
	}
	return rec, evs
}

func dryRun(r *Request)      { r.DryRun = true }
func allowDelete(r *Request) { r.AllowMassDelete = true }
func withSteps(s ...steps.Step) func(*Request) {
	return func(r *Request) { r.Steps = s }
}

// tree hashes every regular file under root, skipping bt metadata, into a
// sorted "path hash" listing that is easy to diff.
func tree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && rel == meta.Dir {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			io.Copy(h, f)
			f.Close()
			lines = append(lines, rel+" "+hex.EncodeToString(h.Sum(nil))[:12])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func logText(evs []Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if l, ok := ev.(Log); ok {
			b.WriteString(l.Text + "\n")
		}
	}
	return b.String()
}

func mustStatus(t *testing.T, rec meta.RunRecord, want meta.Status) {
	t.Helper()
	if rec.Status != want {
		t.Fatalf("status = %s, want %s (message %q, errors %v)", rec.Status, want, rec.Message, rec.Errors)
	}
}

// fakeStep is a scriptable extra step.
type fakeStep struct {
	id       string
	upToDate bool
	planErr  error
	runErr   error
	summary  string
	writes   map[string]string // files written to the main drive when run
	runs     int
	plans    int
}

func (s *fakeStep) ID() string            { return s.id }
func (s *fakeStep) Title() string         { return "Fake " + s.id }
func (s *fakeStep) Description() string   { return "" }
func (s *fakeStep) DefaultSelected() bool { return true }
func (s *fakeStep) Plan(context.Context, steps.Env) (steps.Plan, error) {
	s.plans++
	if s.upToDate {
		return steps.Plan{Summary: "up to date", UpToDate: true}, s.planErr
	}
	return steps.Plan{Summary: "has work"}, s.planErr
}
func (s *fakeStep) Run(_ context.Context, env steps.Env, r steps.Reporter) (string, error) {
	s.runs++
	r.Progress(1, 2, "")
	for p, c := range s.writes {
		full := filepath.Join(env.Main, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	r.Progress(2, 2, "")
	return s.summary, s.runErr
}

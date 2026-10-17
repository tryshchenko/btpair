package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/steps"
)

func TestStepOutputReachesReplica(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	step := &fakeStep{id: "gen", summary: "made 1 file", writes: map[string]string{"generated/out.txt": "from step"}}
	rec, _ := f.run(withSteps(step))
	mustStatus(t, rec, meta.StatusOK)
	if step.runs != 1 {
		t.Fatalf("step ran %d times", step.runs)
	}
	if b, _ := os.ReadFile(filepath.Join(f.replica(), "generated/out.txt")); string(b) != "from step" {
		t.Fatal("step output was not mirrored (steps must run before the sync)")
	}
	if len(rec.Steps) != 1 || rec.Steps[0].Message != "made 1 file" || rec.Steps[0].Status != meta.StatusOK {
		t.Errorf("step result = %+v", rec.Steps)
	}
}

func TestUpToDateStepIsNotRun(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	step := &fakeStep{id: "s", upToDate: true}
	rec, _ := f.run(withSteps(step))
	mustStatus(t, rec, meta.StatusOK)
	if step.runs != 0 {
		t.Fatal("up-to-date step was run")
	}
	if !rec.Steps[0].Skipped {
		t.Error("step not recorded as skipped")
	}
}

func TestFailedStepDoesNotBlockSync(t *testing.T) {
	f := newFixture(t, drifted.main, drifted.replica)
	bad := &fakeStep{id: "bad", runErr: errors.New("server down")}
	good := &fakeStep{id: "good"}
	rec, _ := f.run(withSteps(bad, good))
	mustStatus(t, rec, meta.StatusPartial)
	if good.runs != 1 {
		t.Error("a failing step stopped later steps")
	}
	if tree(t, f.replica()) != tree(t, f.main()) {
		t.Fatal("a failing step stopped the sync")
	}
	if len(rec.Pending) != 1 || rec.Pending[0].Kind != "step" || rec.Pending[0].Path != "bad" || !strings.Contains(rec.Pending[0].Reason, "server down") {
		t.Errorf("pending = %+v", rec.Pending)
	}
}

func TestPlanErrorIsAStepFailure(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	step := &fakeStep{id: "s", planErr: errors.New("no token")}
	rec, _ := f.run(withSteps(step))
	mustStatus(t, rec, meta.StatusPartial)
	if step.runs != 0 {
		t.Error("step ran after its plan failed")
	}
}

func TestPendingStepsCarryOverUntilTheySucceed(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	flaky := &fakeStep{id: "flaky", runErr: errors.New("boom")}
	rec, _ := f.run(withSteps(flaky))
	mustStatus(t, rec, meta.StatusPartial)

	// Not selected next time: it stays pending.
	f.redetect()
	rec, _ = f.run()
	if len(rec.Pending) != 1 || rec.Pending[0].Path != "flaky" {
		t.Fatalf("unselected failed step dropped from pending: %+v", rec.Pending)
	}

	// Selected and succeeds: cleared.
	f.redetect()
	flaky.runErr = nil
	rec, _ = f.run(withSteps(flaky))
	mustStatus(t, rec, meta.StatusOK)
	if len(rec.Pending) != 0 {
		t.Fatalf("pending not cleared: %+v", rec.Pending)
	}
}

func TestStepStateDirIsOnMainAndStable(t *testing.T) {
	f := newFixture(t, drifted.main, nil)
	var dirs []string
	for i := 0; i < 2; i++ {
		s := &envStep{fakeStep: fakeStep{id: "st"}}
		f.run(withSteps(s))
		dirs = append(dirs, s.env.StateDir)
		if s.env.Main != f.main() || s.env.PairID != "t" {
			t.Fatalf("env = %+v", s.env)
		}
	}
	if dirs[0] != dirs[1] || !strings.HasPrefix(dirs[0], f.main()) {
		t.Fatalf("state dirs %v: must be stable and on main", dirs)
	}
}

// ── event contract the UI depends on ─────────────────────────────────────

func TestEventStreamContract(t *testing.T) {
	cases := map[string]func(*fixture) []func(*Request){
		"ok":      func(*fixture) []func(*Request) { return nil },
		"dry run": func(*fixture) []func(*Request) { return []func(*Request){dryRun} },
		"steps": func(*fixture) []func(*Request) {
			return []func(*Request){withSteps(&fakeStep{id: "a"}, &fakeStep{id: "b", upToDate: true}, &fakeStep{id: "c", runErr: errors.New("x")})}
		},
		"preflight failure": func(f *fixture) []func(*Request) {
			f.pair.Replica = nil
			return nil
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, drifted.main, drifted.replica)
			opts := setup(f)
			req := Request{Pair: f.pair, Config: f.cfg}
			for _, o := range opts {
				o(&req)
			}
			known := map[string]bool{}
			for _, p := range Phases(req) {
				known[p.ID] = true
			}
			_, evs := f.run(opts...)

			if _, ok := evs[len(evs)-1].(Finished); !ok {
				t.Fatalf("last event is %T, want Finished", evs[len(evs)-1])
			}
			open := map[string]bool{}
			finished := 0
			for _, ev := range evs {
				switch e := ev.(type) {
				case PhaseStarted:
					if !known[e.ID] {
						t.Errorf("started unknown phase %q", e.ID)
					}
					if open[e.ID] {
						t.Errorf("phase %q started twice", e.ID)
					}
					open[e.ID] = true
				case PhaseFinished:
					if !known[e.ID] {
						t.Errorf("finished unknown phase %q", e.ID)
					}
					delete(open, e.ID)
				case Progress:
					if !known[e.ID] {
						t.Errorf("progress for unknown phase %q", e.ID)
					}
					if e.Total > 0 && (e.Done < 0 || e.Done > e.Total) {
						t.Errorf("progress %d/%d out of range", e.Done, e.Total)
					}
				case Finished:
					finished++
				}
			}
			if len(open) > 0 {
				t.Errorf("phases never finished: %v", open)
			}
			if finished != 1 {
				t.Errorf("%d Finished events", finished)
			}
		})
	}
}

func TestSyncProgressIsMonotonic(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 30; i++ {
		files[filepath.Join("d", string(rune('a'+i%26))+strings.Repeat("x", i))] = strings.Repeat("y", 100_000)
	}
	f := newFixture(t, files, nil)
	_, evs := f.run()
	var last int64 = -1
	var total, files0 int64
	for _, ev := range evs {
		if p, ok := ev.(Progress); ok && p.ID == PhaseSync && p.Unit == "bytes" {
			if p.Done < last {
				t.Fatalf("progress went backwards: %d → %d", last, p.Done)
			}
			last, total, files0 = p.Done, p.Total, p.Files
		}
	}
	if last != total || total == 0 {
		t.Errorf("final progress %d/%d, want complete", last, total)
	}
	if files0 != 30 {
		t.Errorf("final file count %d, want 30", files0)
	}
}

type envStep struct {
	fakeStep
	env steps.Env
}

func (s *envStep) Run(ctx context.Context, env steps.Env, r steps.Reporter) (string, error) {
	s.env = env
	return s.fakeStep.Run(ctx, env, r)
}

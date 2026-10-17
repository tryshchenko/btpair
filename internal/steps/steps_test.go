package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tryshchenko/backup-tool/internal/config"
)

type recorder struct {
	mu       sync.Mutex
	progress [][3]any
	items    []string
	logs     []string
	warns    []string
}

func (r *recorder) Progress(d, t int64, u string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, [3]any{d, t, u})
}
func (r *recorder) Current(s string) { r.mu.Lock(); r.items = append(r.items, s); r.mu.Unlock() }
func (r *recorder) Log(s string)     { r.mu.Lock(); r.logs = append(r.logs, s); r.mu.Unlock() }
func (r *recorder) Warn(s string)    { r.mu.Lock(); r.warns = append(r.warns, s); r.mu.Unlock() }

func shell(t *testing.T, sc config.StepConfig) (Step, Env) {
	t.Helper()
	sc.Type = "shell"
	cfg := &config.Config{Steps: map[string]config.StepConfig{"s": sc}, Pairs: map[string]config.PairConfig{}}
	list, err := ForPair(cfg, "p")
	if err != nil {
		t.Fatal(err)
	}
	main := t.TempDir()
	return list[0], Env{PairID: "p", Main: main, StateDir: filepath.Join(main, ".backuptool", "steps", "s")}
}

func TestShellProtocol(t *testing.T) {
	s, env := shell(t, config.StepConfig{Run: `
echo "::progress 1 4"
echo "::item first thing"
echo "plain log line"
echo "::warn careful"
echo "::progress 4096 8192 bytes"
echo "summary: all good"
`})
	r := &recorder{}
	summary, err := s.Run(context.Background(), env, r)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "summary: all good" {
		t.Errorf("summary = %q (last plain line)", summary)
	}
	if len(r.progress) != 2 || r.progress[0] != [3]any{int64(1), int64(4), ""} || r.progress[1] != [3]any{int64(4096), int64(8192), "bytes"} {
		t.Errorf("progress = %v", r.progress)
	}
	if len(r.items) != 1 || r.items[0] != "first thing" {
		t.Errorf("items = %v", r.items)
	}
	if len(r.warns) != 1 || r.warns[0] != "careful" {
		t.Errorf("warns = %v", r.warns)
	}
	for _, l := range r.logs {
		if strings.HasPrefix(l, "::") {
			t.Errorf("protocol line leaked into log: %q", l)
		}
	}
}

func TestShellEnvironment(t *testing.T) {
	s, env := shell(t, config.StepConfig{
		Env: map[string]string{"EXTRA": "yes"},
		Run: `echo "$BT_MAIN|$BT_PAIR|$BT_STATE_DIR|$BT_DRY_RUN|$EXTRA|$(pwd -P)"`,
	})
	summary, err := s.Run(context.Background(), env, &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(env.Main)
	want := strings.Join([]string{env.Main, "p", env.StateDir, "false", "yes", real}, "|")
	if summary != want {
		t.Fatalf("got  %q\nwant %q", summary, want)
	}
	if _, err := os.Stat(env.StateDir); err != nil {
		t.Error("state dir not created before running")
	}
}

func TestShellFailureIncludesStderr(t *testing.T) {
	s, env := shell(t, config.StepConfig{Run: `echo working; echo "token expired" >&2; exit 3`})
	r := &recorder{}
	_, err := s.Run(context.Background(), env, r)
	if err == nil || !strings.Contains(err.Error(), "token expired") {
		t.Fatalf("err = %v", err)
	}
	if len(r.warns) != 1 {
		t.Errorf("stderr should be surfaced as warnings: %v", r.warns)
	}
}

func TestShellCancel(t *testing.T) {
	s, env := shell(t, config.StepConfig{Run: `sleep 30`})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Run(ctx, env, &recorder{})
	if err == nil || time.Since(start) > 7*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestShellPlan(t *testing.T) {
	cases := []struct {
		skipIf   string
		upToDate bool
		summary  string
	}{
		{"", false, "runs every time"},
		{`echo "fetched 2m ago"; exit 0`, true, "fetched 2m ago"},
		{`exit 0`, true, "up to date"},
		{`echo "3 repos changed"; exit 1`, false, "3 repos changed"},
		{`exit 1`, false, "has work to do"},
	}
	for _, c := range cases {
		s, env := shell(t, config.StepConfig{Run: "true", SkipIf: c.skipIf})
		p, err := s.Plan(context.Background(), env)
		if err != nil || p.UpToDate != c.upToDate || p.Summary != c.summary {
			t.Errorf("skip_if %q: got %+v, %v", c.skipIf, p, err)
		}
	}
}

func TestShellStateDirPersistsBetweenRuns(t *testing.T) {
	s, env := shell(t, config.StepConfig{
		SkipIf: `test -f "$BT_STATE_DIR/done" && echo done && exit 0; exit 1`,
		Run:    `touch "$BT_STATE_DIR/done"`,
	})
	if p, _ := s.Plan(context.Background(), env); p.UpToDate {
		t.Fatal("up to date before first run")
	}
	if _, err := s.Run(context.Background(), env, &recorder{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Plan(context.Background(), env); !p.UpToDate {
		t.Fatal("not up to date after run")
	}
}

func TestForPair(t *testing.T) {
	cfg := &config.Config{
		Steps: map[string]config.StepConfig{
			"b": {Type: "shell", Run: "true", Title: "Bee", Default: true},
			"a": {Type: "shell", Run: "true"},
		},
		Pairs: map[string]config.PairConfig{"only-b": {Steps: []string{"b"}}, "bad": {Steps: []string{"nope"}}},
	}
	all, err := ForPair(cfg, "other")
	if err != nil || len(all) != 2 || all[0].ID() != "a" || all[1].ID() != "b" {
		t.Fatalf("all steps in ID order: %v %v", ids(all), err)
	}
	if all[0].Title() != "a" || all[1].Title() != "Bee" || !all[1].DefaultSelected() || all[0].DefaultSelected() {
		t.Error("titles/defaults wrong")
	}
	only, _ := ForPair(cfg, "only-b")
	if len(only) != 1 || only[0].ID() != "b" {
		t.Fatalf("pair step list: %v", ids(only))
	}
	if _, err := ForPair(cfg, "bad"); err == nil {
		t.Error("unknown step id accepted")
	}
	cfg.Steps["x"] = config.StepConfig{Type: "no-such-type"}
	if _, err := ForPair(cfg, "other"); err == nil {
		t.Error("unknown step type accepted")
	}
	cfg.Steps["x"] = config.StepConfig{Type: "shell"}
	if _, err := ForPair(cfg, "other"); err == nil {
		t.Error("shell step without run accepted")
	}
}

func ids(s []Step) []string {
	var out []string
	for _, x := range s {
		out = append(out, x.ID())
	}
	return out
}

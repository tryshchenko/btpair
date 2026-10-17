package main

// End-to-end tests of the bt binary against dev drives.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryshchenko/backup-tool/internal/meta"
	"github.com/tryshchenko/backup-tool/internal/rsync"
)

var btBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bt-bin-")
	if err != nil {
		panic(err)
	}
	btBin = filepath.Join(dir, "bt")
	if out, err := exec.Command("go", "build", "-o", btBin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t       *testing.T
	root    string
	cfgPath string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, _, err := rsync.Find(""); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	for _, d := range []string{"M", "R"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "M", "doc.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "R", "stale.txt"), []byte("old"), 0o644)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfg, []byte(`
[steps.mark]
type = "shell"
run = 'echo stamped > "$BT_MAIN/stamp.txt"; echo "stamped"'
`), 0o644)
	return &env{t: t, root: root, cfgPath: cfg}
}

// bt runs the binary and returns combined output and exit code.
func (e *env) bt(args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(btBin, args...)
	cmd.Env = append(os.Environ(), "BT_DEV_ROOT="+e.root, "BT_CONFIG="+e.cfgPath, "XDG_STATE_HOME="+e.t.TempDir())
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return string(out), code
}

func (e *env) init() {
	e.t.Helper()
	if out, code := e.bt("init", "--main", "M", "--replica", "R", "--pair", "p", "--label", "Test"); code != 0 {
		e.t.Fatalf("init failed (%d): %s", code, out)
	}
}

func TestInitAndStatus(t *testing.T) {
	e := newEnv(t)
	out, _ := e.bt("status")
	if !strings.Contains(out, "unpaired  M") || !strings.Contains(out, "unpaired  R") {
		t.Fatalf("before init:\n%s", out)
	}
	e.init()
	out, code := e.bt("status")
	if code != 0 || !strings.Contains(out, "p  (Test)") || !strings.Contains(out, "main    M") || !strings.Contains(out, "replica R") || !strings.Contains(out, "last    never") {
		t.Fatalf("after init:\n%s", out)
	}
	if out, code := e.bt("init", "--main", "M", "--replica", "R", "--pair", "again"); code == 0 {
		t.Fatalf("re-init of managed drives succeeded:\n%s", out)
	}
}

func TestSyncDryRunThenReal(t *testing.T) {
	e := newEnv(t)
	e.init()

	out, code := e.bt("sync", "p", "--dry-run")
	if code != 0 || !strings.Contains(out, "result: dry_run") || !strings.Contains(out, "+ doc.txt") || !strings.Contains(out, "− stale.txt") {
		t.Fatalf("dry run (%d):\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(e.root, "R", "doc.txt")); err == nil {
		t.Fatal("dry run copied a file")
	}

	out, code = e.bt("sync", "p", "--steps", "mark")
	if code != 0 || !strings.Contains(out, "result: ok") || !strings.Contains(out, "stamped") {
		t.Fatalf("sync (%d):\n%s", code, out)
	}
	for _, f := range []string{"doc.txt", "stamp.txt"} {
		if _, err := os.Stat(filepath.Join(e.root, "R", f)); err != nil {
			t.Errorf("replica missing %s", f)
		}
	}
	var st meta.State
	b, _ := os.ReadFile(meta.StatePath(filepath.Join(e.root, "R")))
	json.Unmarshal(b, &st)
	if st.Status != meta.StatusOK || len(st.Steps) != 1 {
		t.Errorf("replica state = %+v", st)
	}

	out, _ = e.bt("status")
	if !strings.Contains(out, "last    ok") {
		t.Errorf("status after sync:\n%s", out)
	}
}

func TestSyncFailureExitCode(t *testing.T) {
	e := newEnv(t)
	e.init()
	os.Remove(filepath.Join(e.root, "M", "doc.txt")) // empty main
	out, code := e.bt("sync", "p")
	if code != 2 || !strings.Contains(out, "result: failed") {
		t.Fatalf("expected exit 2 and failure (%d):\n%s", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	e := newEnv(t)
	e.init()
	for _, args := range [][]string{
		{"sync"},                         // no pair
		{"sync", "nope"},                 // unknown pair
		{"sync", "p", "--steps", "nope"}, // unknown step
		{"frobnicate"},                   // unknown command
	} {
		if out, code := e.bt(args...); code == 0 {
			t.Errorf("bt %v succeeded:\n%s", args, out)
		}
	}
	if out, code := e.bt("help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("help (%d):\n%s", code, out)
	}
}

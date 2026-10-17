package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingConfigIsEmpty(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || cfg.Pairs == nil || cfg.Steps == nil {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestPairDefaults(t *testing.T) {
	cfg, _ := Load(filepath.Join(t.TempDir(), "nope.toml"))
	p := cfg.Pair("anything")
	if p.MaxDeletePercent != 25 || p.TrashKeep != 5 || p.NoTrash || p.AllowWritableMain {
		t.Fatalf("unsafe or unexpected defaults: %+v", p)
	}
}

func TestParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(`
rsync = "/opt/rsync"
[pairs.work]
paths = ["A", "B"]
exclude = ["*.tmp"]
max_delete_percent = 10
trash_keep = 2
bwlimit = "5M"
steps = ["x"]
allow_writable_main = true

[steps.x]
type = "shell"
title = "X"
default = true
run = "echo hi"
skip_if = "true"
env = { FOO = "bar" }
`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Pair("work")
	if cfg.Rsync != "/opt/rsync" || len(p.Paths) != 2 || p.MaxDeletePercent != 10 || p.TrashKeep != 2 ||
		p.BWLimit != "5M" || !p.AllowWritableMain || p.Steps[0] != "x" || p.Exclude[0] != "*.tmp" {
		t.Fatalf("pair = %+v", p)
	}
	s := cfg.Steps["x"]
	if s.Type != "shell" || !s.Default || s.Run != "echo hi" || s.SkipIf != "true" || s.Env["FOO"] != "bar" {
		t.Fatalf("step = %+v", s)
	}
}

func TestInvalidConfigIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("[pairs.work\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestDefaultExcludesCoverMacOSHousekeeping(t *testing.T) {
	want := map[string]bool{".Spotlight-V100": true, ".fseventsd": true, ".Trashes": true, ".DS_Store": true}
	for _, e := range DefaultExcludes {
		delete(want, e)
	}
	if len(want) > 0 {
		t.Fatalf("missing default excludes: %v", want)
	}
}

func TestDefaultPathFallsBackToOldName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BT_CONFIG", "")
	newPath := filepath.Join(home, ".config", "btpair", "config.toml")
	oldPath := filepath.Join(home, ".config", "bt", "config.toml")

	if got := DefaultPath(); got != newPath {
		t.Fatalf("no config anywhere: got %s, want %s", got, newPath)
	}
	os.MkdirAll(filepath.Dir(oldPath), 0o755)
	os.WriteFile(oldPath, nil, 0o644)
	if got := DefaultPath(); got != oldPath {
		t.Fatalf("only old config: got %s, want %s", got, oldPath)
	}
	os.MkdirAll(filepath.Dir(newPath), 0o755)
	os.WriteFile(newPath, nil, 0o644)
	if got := DefaultPath(); got != newPath {
		t.Fatalf("both configs: got %s, want %s", got, newPath)
	}
}

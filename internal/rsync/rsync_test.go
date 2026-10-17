package rsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseItem(t *testing.T) {
	cases := []struct {
		line           string
		del, file, new bool
		name           string
		size           int64
	}{
		{"@@>f+++++++++|30000000|big.bin", false, true, true, "big.bin", 30000000},
		{"@@>f.st......|12|a/b c.txt", false, true, false, "a/b c.txt", 12},
		{"@@cd+++++++++|96|sub/", false, false, true, "sub/", 96},
		{"@@*deleting  |0|old/gone.txt", true, false, false, "old/gone.txt", 0},
		{"@@>f+++++++++|5|weird|name.txt", false, true, true, "weird|name.txt", 5},
	}
	for _, c := range cases {
		it, ok := parseItem(c.line)
		if !ok {
			t.Fatalf("%q: not parsed", c.line)
		}
		if it.Deleting != c.del || it.IsFile != c.file || (!c.del && it.New != c.new) || it.Name != c.name || it.Size != c.size {
			t.Errorf("%q: got %+v", c.line, it)
		}
	}
	if _, ok := parseItem("Number of files: 4"); ok {
		t.Error("stats line parsed as item")
	}
}

func TestProgressLine(t *testing.T) {
	m := progressLine.FindStringSubmatch("     30,000,000  99%  714.47MB/s    0:00:00 (xfr#1, to-chk=2/4)")
	if m == nil || parseNum(m[1]) != 30000000 || m[2] != "99" {
		t.Fatalf("got %v", m)
	}
	if s := speed(m[3], m[4]); s < 714*(1<<20) || s > 715*(1<<20) {
		t.Errorf("speed %v", s)
	}
	if progressLine.FindStringSubmatch("         32,768   0%    0.00kB/s    0:00:00  ") == nil {
		t.Error("initial progress line not matched")
	}
}

func TestArgsRelativePaths(t *testing.T) {
	a := Options{Bin: "rsync", Src: "/M", Dst: "/S", Paths: []string{"Photos/", "/Docs"}}.args(false)
	n := len(a)
	if a[n-1] != "/S/" || a[n-2] != "/M/./Docs" || a[n-3] != "/M/./Photos" {
		t.Errorf("unexpected tail %v", a[n-3:])
	}
}

func TestArgsSafetyFlags(t *testing.T) {
	o := Options{Bin: "rsync", Src: "/M/", Dst: "/R", TrashDir: "/R/.backuptool/trash/1", Excludes: []string{"/.backuptool/", "*.tmp"}, BWLimit: "5M", Checksum: true}
	real := strings.Join(o.args(false), " ")
	dry := strings.Join(o.args(true), " ")
	for _, want := range []string{"--delete", "--backup", "--backup-dir=/R/.backuptool/trash/1", "--exclude=/.backuptool/", "--exclude=*.tmp", "--checksum", "--bwlimit=5M", "--info=progress2"} {
		if !strings.Contains(real, want) {
			t.Errorf("real args missing %s: %s", want, real)
		}
	}
	if !strings.Contains(dry, "--dry-run") || strings.Contains(dry, "--bwlimit") || strings.Contains(real, "--dry-run") {
		t.Errorf("dry/real mixup:\n%s\n%s", dry, real)
	}
	// Direction: source is always Src, destination is always Dst.
	a := o.args(false)
	if a[len(a)-2] != "/M/" || a[len(a)-1] != "/R/" {
		t.Fatalf("direction wrong: %v", a[len(a)-2:])
	}
	if strings.Contains(real, "--delete-excluded") {
		t.Fatal("--delete-excluded would wipe the replica's metadata")
	}
}

func TestFindRejectsOldRsync(t *testing.T) {
	if _, _, err := Find("/usr/bin/rsync"); err == nil {
		t.Skip("system rsync is new enough")
	}
	if _, _, err := Find("/no/such/rsync"); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func setupDirs(t *testing.T) (Options, string, string) {
	t.Helper()
	bin, _, err := Find("")
	if err != nil {
		t.Skip(err)
	}
	src, dst := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(src, fmt.Sprintf("f%02d.bin", i)), bytes.Repeat([]byte{byte(i)}, 50_000), 0o644)
	}
	os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("x"), 0o644)
	return Options{Bin: bin, Src: src, Dst: dst, TrashDir: filepath.Join(dst, ".trash"), Excludes: []string{"/.trash/"}}, src, dst
}

func TestAnalyzeAndRun(t *testing.T) {
	o, _, dst := setupDirs(t)
	p, err := Analyze(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if p.FilesToCopy != 20 || p.FilesToDelete != 1 || p.BytesToCopy != 20*50_000 || len(p.Copies) != 20 || p.Deletions[0] != "stale.txt" {
		t.Fatalf("plan = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dst, "f00.bin")); err == nil {
		t.Fatal("Analyze wrote files")
	}

	var updates []Update
	res, err := Run(context.Background(), o, func(u Update) { updates = append(updates, u) })
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDone != 20 || res.Deleted != 1 || res.Bytes != 20*50_000 || res.ExitCode != 0 || len(res.Errors) != 0 {
		t.Fatalf("result = %+v", res)
	}
	var copied, deleted int
	for _, u := range updates {
		if u.Copied != "" {
			copied++
		}
		if u.Deleting != "" {
			deleted++
		}
	}
	if copied != 20 || deleted != 1 {
		t.Errorf("events: %d copied, %d deleted", copied, deleted)
	}
	if _, err := os.Stat(filepath.Join(o.TrashDir, "stale.txt")); err != nil {
		t.Error("deleted file not moved to trash")
	}

	// Second pass: nothing to do.
	p, _ = Analyze(context.Background(), o)
	if p.FilesToCopy != 0 || p.FilesToDelete != 0 {
		t.Errorf("not idempotent: %+v", p)
	}
}

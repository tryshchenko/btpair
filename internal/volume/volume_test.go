package volume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tryshchenko/btpair/internal/meta"
)

func TestDetectDev(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"B", "A", ".hidden"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)

	vols, err := Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 || vols[0].Name != "A" || vols[1].Name != "B" {
		t.Fatalf("got %v", names(vols))
	}
	again, _ := Detect(context.Background(), root)
	if vols[0].UUID != again[0].UUID || vols[0].UUID == vols[1].UUID || !strings.HasPrefix(vols[0].UUID, "DEV-") {
		t.Fatalf("UUIDs must be stable and distinct: %s %s %s", vols[0].UUID, again[0].UUID, vols[1].UUID)
	}
	if vols[0].Managed() {
		t.Error("blank drive reported as managed")
	}
}

func TestDetectLoadsMetadata(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "A"), 0o755)
	meta.SaveIdentity(filepath.Join(root, "A"), meta.Identity{PairID: "p", Role: meta.RoleMain})
	vols, _ := Detect(context.Background(), root)
	if !vols[0].Managed() || vols[0].Identity.PairID != "p" || vols[0].State == nil {
		t.Fatalf("metadata not loaded: %+v", vols[0])
	}
}

func TestHasContent(t *testing.T) {
	dir := t.TempDir()
	if HasContent(dir) {
		t.Error("empty dir has content")
	}
	for _, n := range []string{meta.Dir, ".Spotlight-V100", ".fseventsd", ".Trashes", ".DS_Store"} {
		os.MkdirAll(filepath.Join(dir, n), 0o755)
	}
	if HasContent(dir) {
		t.Error("housekeeping-only drive has content")
	}
	os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644)
	if !HasContent(dir) {
		t.Error("real file not seen")
	}
	if HasContent(filepath.Join(dir, "missing")) {
		t.Error("missing dir has content")
	}
}

func TestDevSpace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), make([]byte, 1000), 0o644)
	v := &Volume{Path: dir, Dev: true}
	sp, err := v.Space()
	if err != nil || sp.Total != DevCapacity || sp.Used() != 1000 {
		t.Fatalf("got %+v, %v", sp, err)
	}
	if f := sp.UsedFrac(); f <= 0 || f >= 1 {
		t.Errorf("used frac %v", f)
	}
	if (Space{}).UsedFrac() != 0 {
		t.Error("zero space frac")
	}
}

func TestRemountIsNoOpForDevDrives(t *testing.T) {
	v := &Volume{Path: t.TempDir(), Dev: true}
	if err := v.RemountReadOnly(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := v.RemountReadWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func names(vs []*Volume) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func TestWaitForMountRetriesSlowDrives(t *testing.T) {
	oldFn, oldEvery := diskInfoFn, retryEvery
	t.Cleanup(func() { diskInfoFn, retryEvery = oldFn, oldEvery })
	retryEvery = time.Millisecond

	calls := 0
	diskInfoFn = func(ctx context.Context, dev string) (*diskInfo, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("diskutil info disk5s2: signal: killed") // what a busy 2 TB exFAT drive did
		case 2:
			return &diskInfo{}, nil // answered, but not mounted yet
		}
		return &diskInfo{MountPoint: "/Volumes/X", VolumeUUID: "U"}, nil
	}
	info, err := waitForMount(context.Background(), "disk5s2", time.Second)
	if err != nil || info.MountPoint != "/Volumes/X" || calls != 3 {
		t.Fatalf("info=%+v err=%v calls=%d", info, err, calls)
	}

	diskInfoFn = func(ctx context.Context, dev string) (*diskInfo, error) { return nil, errors.New("gone") }
	if _, err := waitForMount(context.Background(), "disk5s2", 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("expected the last error after giving up, got %v", err)
	}
}

// Package volume detects mounted external drives and reports disk usage.
//
// On a real system it scans /Volumes and asks diskutil about each mount.
// For development, a "dev root" can be given instead: every subdirectory is
// treated as a drive with a simulated capacity, so the whole flow can be
// exercised against plain folders.
package volume

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"howett.net/plist"

	"github.com/tryshchenko/btpair/internal/meta"
)

// DevCapacity is the simulated size of a dev-mode drive (a var so tests can
// shrink it).
var DevCapacity uint64 = 2 << 30

type Volume struct {
	Name     string
	Path     string
	UUID     string
	Device   string // e.g. disk4s1; empty in dev mode
	FS       string
	Dev      bool
	ReadOnly bool

	Identity *meta.Identity
	State    *meta.State
	MetaErr  error
}

func (v *Volume) Managed() bool { return v.Identity != nil }

type Space struct {
	Total uint64
	Free  uint64
}

func (s Space) Used() uint64 { return s.Total - s.Free }

func (s Space) UsedFrac() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Used()) / float64(s.Total)
}

// Space reports capacity and free bytes. Dev drives report the size of
// their contents against DevCapacity.
func (v *Volume) Space() (Space, error) {
	if v.Dev {
		var used uint64
		filepath.WalkDir(v.Path, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					used += uint64(info.Size())
				}
			}
			return nil
		})
		if used > DevCapacity {
			return Space{Total: used, Free: 0}, nil
		}
		return Space{Total: DevCapacity, Free: DevCapacity - used}, nil
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(v.Path, &st); err != nil {
		return Space{}, err
	}
	return Space{Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)}, nil
}

// LoadMeta (re)reads identity and state from the drive.
func (v *Volume) LoadMeta() {
	v.Identity, v.State, v.MetaErr = nil, nil, nil
	id, err := meta.LoadIdentity(v.Path)
	if err != nil {
		v.MetaErr = err
		return
	}
	v.Identity = id
	if id != nil {
		v.State, v.MetaErr = meta.LoadState(v.Path)
	}
}

// Detect lists candidate drives. devRoot, when non-empty, switches to dev mode.
func Detect(ctx context.Context, devRoot string) ([]*Volume, error) {
	var vols []*Volume
	var err error
	if devRoot != "" {
		vols, err = detectDev(devRoot)
	} else {
		vols, err = detectMac(ctx)
	}
	if err != nil {
		return nil, err
	}
	for _, v := range vols {
		v.LoadMeta()
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	return vols, nil
}

func detectDev(root string) ([]*Volume, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []*Volume
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(root, e.Name())
		sum := sha1.Sum([]byte(p))
		out = append(out, &Volume{
			Name: e.Name(),
			Path: p,
			UUID: "DEV-" + strings.ToUpper(hex.EncodeToString(sum[:8])),
			FS:   "dev",
			Dev:  true,
		})
	}
	return out, nil
}

type diskInfo struct {
	VolumeUUID     string `plist:"VolumeUUID"`
	VolumeName     string `plist:"VolumeName"`
	MountPoint     string `plist:"MountPoint"`
	DeviceID       string `plist:"DeviceIdentifier"`
	Internal       bool   `plist:"Internal"`
	FilesystemType string `plist:"FilesystemType"`
	WritableVolume bool   `plist:"WritableVolume"`
	BusProtocol    string `plist:"BusProtocol"`
}

func detectMac(ctx context.Context) ([]*Volume, error) {
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []*Volume
	)
	for _, e := range entries {
		p := filepath.Join("/Volumes", e.Name())
		// The boot volume appears as a symlink to "/".
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := DiskInfo(ctx, p)
			// Skip the internal disk and mounted disk images (.dmg installers).
			if err != nil || info.Internal || info.VolumeUUID == "" || (info.BusProtocol == "Disk Image" && os.Getenv("BT_ALLOW_DMG") == "") {
				return
			}
			mu.Lock()
			out = append(out, &Volume{
				Name:     info.VolumeName,
				Path:     p,
				UUID:     info.VolumeUUID,
				Device:   info.DeviceID,
				FS:       info.FilesystemType,
				ReadOnly: !info.WritableVolume,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

func DiskInfo(ctx context.Context, target string) (*diskInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "diskutil", "info", "-plist", target).Output()
	if err != nil {
		return nil, fmt.Errorf("diskutil info %s: %w", target, err)
	}
	var info diskInfo
	if _, err := plist.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RemountReadOnly remounts a real drive read-only, returning its (possibly
// changed) mount path. Dev drives are left untouched.
func (v *Volume) RemountReadOnly(ctx context.Context) error {
	if v.Dev {
		return nil
	}
	return v.remount(ctx, true)
}

// RemountReadWrite undoes RemountReadOnly.
func (v *Volume) RemountReadWrite(ctx context.Context) error {
	if v.Dev {
		return nil
	}
	return v.remount(ctx, false)
}

func (v *Volume) remount(ctx context.Context, readOnly bool) error {
	if err := diskutil(ctx, "unmount", v.Device); err != nil {
		return err
	}
	args := []string{"mount"}
	if readOnly {
		args = append(args, "readOnly")
	}
	if err := diskutil(ctx, append(args, v.Device)...); err != nil {
		return err
	}
	info, err := DiskInfo(ctx, v.Device)
	if err != nil {
		return err
	}
	if info.VolumeUUID != v.UUID {
		return fmt.Errorf("remounted %s but UUID changed (%s → %s)", v.Device, v.UUID, info.VolumeUUID)
	}
	if readOnly && info.WritableVolume {
		return fmt.Errorf("%s is still writable after read-only remount", v.Name)
	}
	v.Path = info.MountPoint
	v.ReadOnly = !info.WritableVolume
	return nil
}

func diskutil(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "diskutil", args...)
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return fmt.Errorf("diskutil %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// HasContent reports whether the drive holds anything besides btpair metadata
// and macOS housekeeping. Used to refuse syncing from an empty main.
func HasContent(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch e.Name() {
		case meta.Dir, ".Spotlight-V100", ".fseventsd", ".Trashes", ".TemporaryItems", ".DS_Store", ".DocumentRevisions-V100":
			continue
		}
		return true
	}
	return false
}

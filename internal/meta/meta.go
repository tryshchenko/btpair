// Package meta reads and writes the .backuptool metadata directory kept at
// the root of every managed drive. The metadata is the source of truth for
// which pair a drive belongs to and whether it is the main drive or the replica.
package meta

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tryshchenko/btpair/internal/fsutil"
)

// Dir is the metadata directory name at a drive's root. It is always
// excluded from synchronization.
const Dir = ".backuptool"

const schemaVersion = 1

type Role string

const (
	RoleMain    Role = "main"
	RoleReplica Role = "replica"
)

// Identity says who a drive is. Written once by `btpair init`.
type Identity struct {
	Version    int       `json:"version"`
	PairID     string    `json:"pair_id"`
	Role       Role      `json:"role"`
	VolumeUUID string    `json:"volume_uuid"`
	PeerUUID   string    `json:"peer_uuid"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"created_at"`
}

type Status string

const (
	StatusNever      Status = "never"
	StatusInProgress Status = "in_progress"
	StatusOK         Status = "ok"
	StatusPartial    Status = "partial" // finished, but some items failed
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusDryRun     Status = "dry_run"
)

// SyncStats summarizes what the rsync phase did.
type SyncStats struct {
	FilesTotal       int64 `json:"files_total"`
	FilesTransferred int64 `json:"files_transferred"`
	FilesDeleted     int64 `json:"files_deleted"`
	BytesTotal       int64 `json:"bytes_total"`
	BytesTransferred int64 `json:"bytes_transferred"`
}

type StepResult struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Status   Status        `json:"status"`
	Skipped  bool          `json:"skipped,omitempty"`
	Message  string        `json:"message,omitempty"`
	Duration time.Duration `json:"duration_ns"`
}

// PendingItem is something that did not make it across and should be
// retried by the next run.
type PendingItem struct {
	Kind   string `json:"kind"` // "file" or "step"
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// State is the last-run summary. Both drives of a pair carry a copy.
type State struct {
	Version       int           `json:"version"`
	Status        Status        `json:"status"`
	RunID         string        `json:"run_id,omitempty"`
	Host          string        `json:"host,omitempty"`
	StartedAt     time.Time     `json:"started_at,omitzero"`
	FinishedAt    time.Time     `json:"finished_at,omitzero"`
	LastSuccessAt time.Time     `json:"last_success_at,omitzero"`
	Message       string        `json:"message,omitempty"`
	Stats         SyncStats     `json:"stats"`
	Steps         []StepResult  `json:"steps,omitempty"`
	Pending       []PendingItem `json:"pending,omitempty"`
}

// RunRecord is the full record of one run, stored under runs/.
type RunRecord struct {
	State
	PairID      string   `json:"pair_id"`
	MainUUID    string   `json:"main_uuid"`
	ReplicaUUID string   `json:"replica_uuid"`
	DryRun      bool     `json:"dry_run,omitempty"`
	Errors      []string `json:"errors,omitempty"`
	Log         []string `json:"log,omitempty"`
}

func path(vol string, parts ...string) string {
	return filepath.Join(append([]string{vol, Dir}, parts...)...)
}

func IdentityPath(vol string) string { return path(vol, "identity.json") }
func StatePath(vol string) string    { return path(vol, "state.json") }
func RunsDir(vol string) string      { return path(vol, "runs") }
func TrashDir(vol string) string     { return path(vol, "trash") }
func StepsDir(vol string) string     { return path(vol, "steps") }

// LoadIdentity returns (nil, nil) when the drive is not managed by btpair.
func LoadIdentity(vol string) (*Identity, error) {
	var id Identity
	if err := fsutil.ReadJSON(IdentityPath(vol), &id); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &id, nil
}

func SaveIdentity(vol string, id Identity) error {
	id.Version = schemaVersion
	return fsutil.WriteJSON(IdentityPath(vol), id)
}

// LoadState returns a "never synced" state when no state file exists.
func LoadState(vol string) (*State, error) {
	st := State{Version: schemaVersion, Status: StatusNever}
	if err := fsutil.ReadJSON(StatePath(vol), &st); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return &st, nil
}

func SaveState(vol string, st State) error {
	st.Version = schemaVersion
	return fsutil.WriteJSON(StatePath(vol), st)
}

func SaveRun(vol string, rec RunRecord) error {
	rec.Version = schemaVersion
	return fsutil.WriteJSON(filepath.Join(RunsDir(vol), rec.RunID+".json"), rec)
}

// ListRuns returns run records newest first.
func ListRuns(vol string, limit int) ([]RunRecord, error) {
	entries, err := os.ReadDir(RunsDir(vol))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // run IDs sort chronologically
	var out []RunRecord
	for _, n := range names {
		if limit > 0 && len(out) >= limit {
			break
		}
		var rec RunRecord
		if err := fsutil.ReadJSON(filepath.Join(RunsDir(vol), n), &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// TrashRun is one run's trash folder on a replica: what that run deleted or
// overwrote.
type TrashRun struct {
	ID    string // the run ID, which is also the folder name
	Files int64
	Bytes int64
}

// Time is when the run that filled this trash started.
func (t TrashRun) Time() time.Time {
	at, _ := time.ParseInLocation("20060102-150405.000", t.ID, time.Local)
	return at
}

func trashIDs(vol string) ([]string, error) {
	entries, err := os.ReadDir(TrashDir(vol))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids) // run IDs sort chronologically
	return ids, nil
}

// ListTrash measures every trash folder on vol, oldest first.
func ListTrash(vol string) ([]TrashRun, error) {
	ids, err := trashIDs(vol)
	if err != nil {
		return nil, err
	}
	out := make([]TrashRun, 0, len(ids))
	for _, id := range ids {
		t := TrashRun{ID: id}
		err := filepath.WalkDir(filepath.Join(TrashDir(vol), id), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					t.Files++
					t.Bytes += info.Size()
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// TrashTotal sums a trash listing.
func TrashTotal(runs []TrashRun) (files, bytes int64) {
	for _, t := range runs {
		files += t.Files
		bytes += t.Bytes
	}
	return files, bytes
}

// RemoveTrash permanently deletes one run's trash folder.
func RemoveTrash(vol, id string) error {
	if id == "" || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
		return errors.New("invalid trash id " + id)
	}
	if err := os.RemoveAll(filepath.Join(TrashDir(vol), id)); err != nil {
		return err
	}
	// exFAT keeps a "._" AppleDouble sidecar next to the folder.
	os.Remove(filepath.Join(TrashDir(vol), "._"+id))
	return nil
}

// PruneTrash removes trash folders that hold no files (runs that deleted
// nothing), then keeps the newest `keep` of the rest.
func PruneTrash(vol string, keep int) error {
	ids, err := trashIDs(vol)
	if err != nil {
		return err
	}
	var kept []string
	for _, id := range ids {
		if hasFiles(filepath.Join(TrashDir(vol), id)) {
			kept = append(kept, id)
		} else if err := RemoveTrash(vol, id); err != nil {
			return err
		}
	}
	for len(kept) > keep {
		if err := RemoveTrash(vol, kept[0]); err != nil {
			return err
		}
		kept = kept[1:]
	}
	return nil
}

var errFound = errors.New("found")

func hasFiles(dir string) bool {
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return errFound // unreadable: keep it rather than guess
		}
		if !d.IsDir() {
			return errFound
		}
		return nil
	})
	return errors.Is(err, errFound)
}

// NewRunID returns a sortable, human-readable run identifier.
// Milliseconds keep two quick runs from sharing a record or trash folder.
func NewRunID(t time.Time) string { return t.Format("20060102-150405.000") }

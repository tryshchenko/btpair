package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/rsync"
	"github.com/tryshchenko/btpair/internal/volume"
)

// The replica's trash only borrows free space: when a sync needs room, the
// oldest runs' trash is emptied first, as little as possible. The trash of
// the run in progress is never touched.

// roomMargin is extra space freed beyond the bytes to copy, for filesystem
// overhead (exFAT clusters, directories) the estimate does not see.
func roomMargin(need int64) int64 { return need / 50 }

// makeRoom empties the oldest trash on the replica until the plan fits. In
// a dry run it only reports what it would empty.
func (r *runner) makeRoom(p *rsync.Plan, replica *volume.Volume) error {
	sp, err := replica.Space()
	if err != nil {
		return nil // can't measure; let rsync find out
	}
	need, free := p.BytesToCopy, int64(sp.Free)
	if need <= free {
		return nil
	}
	short := fmt.Errorf("replica needs %s but only %s is free", HumanBytes(need), HumanBytes(free))
	if r.pc.NoTrash {
		return short
	}
	runs, err := meta.ListTrash(replica.Path)
	if err != nil {
		return fmt.Errorf("%w (could not measure its trash: %v)", short, err)
	}
	var older []meta.TrashRun
	for _, t := range runs {
		if t.ID != r.rec.RunID && t.Bytes > 0 {
			older = append(older, t)
		}
	}
	_, trash := meta.TrashTotal(older)
	if free+trash < need {
		if trash == 0 {
			return short
		}
		return fmt.Errorf("replica needs %s but only %s is free, and emptying all %s of trash would not be enough", HumanBytes(need), HumanBytes(free), HumanBytes(trash))
	}

	target := need + roomMargin(need)
	var pick []meta.TrashRun
	for _, t := range older {
		if free >= target {
			break
		}
		pick = append(pick, t)
		free += t.Bytes
	}
	for _, t := range pick {
		what := fmt.Sprintf("trash from %s (%s, %d files)", t.Time().Format("2 Jan 15:04"), HumanBytes(t.Bytes), t.Files)
		if r.req.DryRun {
			r.warn("would empty %s to make room", what)
			continue
		}
		if err := meta.RemoveTrash(replica.Path, t.ID); err != nil {
			return fmt.Errorf("%w; emptying %s failed: %v", short, what, err)
		}
		r.warn("emptied %s to make room", what)
	}
	if r.req.DryRun {
		return nil
	}
	if sp, err := replica.Space(); err == nil && need > int64(sp.Free) {
		return fmt.Errorf("replica needs %s but only %s is free after emptying trash", HumanBytes(need), HumanBytes(int64(sp.Free)))
	}
	return nil
}

// massDeleteMinBytes is the replica size below which the size-based
// deletion limit is not applied (a var so tests can lower it).
var massDeleteMinBytes int64 = 1 << 30

// deleteLimit refuses a plan that would delete more than the configured
// share of the replica, by file count or by size.
func (r *runner) deleteLimit(p *rsync.Plan) error {
	if r.req.AllowMassDelete {
		return nil
	}
	limit := r.pc.MaxDeletePercent
	hint := ` — rerun with "allow large deletions" if intended`
	if entries := p.FilesTotal - p.FilesCreated + p.FilesToDelete; entries > 50 {
		if pct := float64(p.FilesToDelete) / float64(entries) * 100; pct > limit {
			return fmt.Errorf("would delete %d of %d entries on the replica (%.0f%% > %.0f%% limit)%s", p.FilesToDelete, entries, pct, limit, hint)
		}
	}
	// What the replica holds in scope today: main's total, minus what is
	// new or changed (approximate, as changed files' old sizes are unknown),
	// plus what is about to go.
	held := max(p.BytesTotal-p.BytesToCopy+p.BytesToDelete, p.BytesToDelete)
	if held >= massDeleteMinBytes {
		if pct := float64(p.BytesToDelete) / float64(held) * 100; pct > limit {
			return fmt.Errorf("would delete %s of %s on the replica (%.0f%% > %.0f%% limit)%s", HumanBytes(p.BytesToDelete), HumanBytes(held), pct, limit, hint)
		}
	}
	return nil
}

// EmptyTrash permanently deletes every trash folder on the pair's replica.
// It holds the pair lock, so it never races a sync.
func EmptyTrash(p *Pair) (files, bytes int64, err error) {
	if p.Replica == nil {
		return 0, 0, errors.New("replica drive is not connected")
	}
	if p.Replica.ReadOnly {
		return 0, 0, fmt.Errorf("replica %s is mounted read-only", p.Replica.Name)
	}
	lock, err := lockPair(p.Replica.Path)
	if err != nil {
		return 0, 0, err
	}
	defer lock.Close()
	runs, err := meta.ListTrash(p.Replica.Path)
	if err != nil {
		return 0, 0, err
	}
	var failed []string
	for _, t := range runs {
		if err := meta.RemoveTrash(p.Replica.Path, t.ID); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.ID, err))
			continue
		}
		files += t.Files
		bytes += t.Bytes
	}
	if len(failed) > 0 {
		return files, bytes, errors.New(strings.Join(failed, "; "))
	}
	return files, bytes, nil
}

// TrashSummary describes a trash listing in a few words, e.g.
// "812.4 GB from 3 runs, oldest 17 Oct". Empty when there is no trash.
func TrashSummary(runs []meta.TrashRun) string {
	var n int
	var oldest meta.TrashRun
	for _, t := range runs {
		if t.Files == 0 {
			continue
		}
		if n == 0 {
			oldest = t
		}
		n++
	}
	if n == 0 {
		return ""
	}
	_, bytes := meta.TrashTotal(runs)
	s := fmt.Sprintf("%s from %d run", HumanBytes(bytes), n)
	if n > 1 {
		s += "s"
	}
	return s + ", oldest " + oldest.Time().Format("2 Jan 2006")
}

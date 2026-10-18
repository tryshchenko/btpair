package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/tryshchenko/btpair/internal/rsync"
)

// Rename detection. rsync has none: a file renamed or moved on the main
// drive looks like a deletion plus a brand-new file, so the replica would
// copy it again in full and park the old copy in trash, which costs time and
// temporarily twice the space. Before syncing, btpair pairs up files the
// dry run would delete with files it would newly create, and renames them
// on the replica instead, so rsync then finds nothing to do for them.
//
// A pair must be unambiguous and match exactly on size and modification
// time (renames and moves keep both), and samples of the two files' contents
// must agree. Anything doubtful is left to rsync, which copies as before.

type rename struct {
	From, To string // relative to the drive roots
	Size     int64
}

type fileKey struct {
	size  int64
	mtime int64 // whole seconds: exFAT and FAT store coarser times than APFS
}

// findRenames pairs files that would be deleted on the replica with files
// that would be created, using size and mtime, then the file name to settle
// ties.
func findRenames(p *rsync.Plan, mainRoot, replicaRoot string) []rename {
	if len(p.Gone) == 0 || len(p.Added) == 0 {
		return nil
	}
	gone := map[fileKey][]string{}
	for _, e := range p.Gone {
		if k, ok := keyOf(filepath.Join(replicaRoot, e.Path), e.Size); ok {
			gone[k] = append(gone[k], e.Path)
		}
	}
	added := map[fileKey][]string{}
	for _, e := range p.Added {
		if k, ok := keyOf(filepath.Join(mainRoot, e.Path), e.Size); ok {
			if _, maybe := gone[k]; maybe {
				added[k] = append(added[k], e.Path)
			}
		}
	}
	var out []rename
	for k, to := range added {
		from := gone[k]
		if len(from) == 1 && len(to) == 1 {
			out = append(out, rename{from[0], to[0], k.size})
			continue
		}
		// Several candidates (e.g. a folder of same-size clips, all touched
		// at once): pair only where the file name is unique on both sides.
		byName := func(paths []string) map[string][]string {
			m := map[string][]string{}
			for _, p := range paths {
				m[path.Base(p)] = append(m[path.Base(p)], p)
			}
			return m
		}
		fromNames, toNames := byName(from), byName(to)
		for name, t := range toNames {
			if f := fromNames[name]; len(f) == 1 && len(t) == 1 {
				out = append(out, rename{f[0], t[0], k.size})
			}
		}
	}
	return out
}

func keyOf(full string, size int64) (fileKey, bool) {
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != size || size == 0 {
		return fileKey{}, false
	}
	return fileKey{size, fi.ModTime().Unix()}, true
}

const sampleSize = 64 << 10

// sameSamples compares the start, middle and end of two files: cheap, and
// enough to tell apart different files that share a size and a timestamp.
func sameSamples(a, b string, size int64) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, sampleSize), make([]byte, sampleSize)
	for _, off := range []int64{0, size/2 - sampleSize/2, size - sampleSize} {
		off = max(0, off)
		na, ea := fa.ReadAt(ba, off)
		nb, eb := fb.ReadAt(bb, off)
		if (ea != nil && ea != io.EOF) || (eb != nil && eb != io.EOF) || na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
	}
	return true
}

type renameResult struct {
	done           []rename
	bytes          int64
	mismatched     int // same size and time, different contents
	skipped        int // target already there, or the rename failed
	firstSkipError error
}

// applyRenames verifies each pair and renames it on the replica. In a dry
// run it only verifies.
func applyRenames(rs []rename, mainRoot, replicaRoot string, dryRun bool) renameResult {
	var res renameResult
	for _, r := range rs {
		from, to := filepath.Join(replicaRoot, r.From), filepath.Join(replicaRoot, r.To)
		if !sameSamples(filepath.Join(mainRoot, r.To), from, r.Size) {
			res.mismatched++
			continue
		}
		if dryRun {
			res.done = append(res.done, r)
			res.bytes += r.Size
			continue
		}
		if _, err := os.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
			res.skipped++
			continue
		}
		err := os.MkdirAll(filepath.Dir(to), 0o755)
		if err == nil {
			err = os.Rename(from, to)
		}
		if err != nil {
			// e.g. EPERM on a Finder-locked file: rsync will copy it instead
			if res.firstSkipError == nil {
				res.firstSkipError = err
			}
			res.skipped++
			continue
		}
		res.done = append(res.done, r)
		res.bytes += r.Size
	}
	return res
}

// maxRenameLog caps how many individual renames are logged.
const maxRenameLog = 200

// detectRenames finds and applies renames for plan p, logs them, and
// records them in the run's stats. It returns how many were applied (or,
// in a dry run, would be).
func (r *runner) detectRenames(ctx context.Context, p *rsync.Plan, mainRoot, replicaRoot string) (int, error) {
	if len(p.Gone) == 0 || len(p.Added) == 0 {
		return 0, nil
	}
	r.progress(Progress{ID: PhaseAnalyze, Current: "looking for renamed files…"}, true)
	pairs := findRenames(p, mainRoot, replicaRoot)
	if len(pairs) == 0 || ctx.Err() != nil {
		return 0, ctx.Err()
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].From < pairs[j].From })
	res := applyRenames(pairs, mainRoot, replicaRoot, r.req.DryRun)
	for i, rn := range res.done {
		if i == maxRenameLog {
			r.info("… and %d more renames", len(res.done)-maxRenameLog)
			break
		}
		r.fileEvent(KindRename, rn.From+" → "+rn.To)
	}
	if len(res.done) > 0 {
		verb := "renamed"
		if r.req.DryRun {
			verb = "would rename"
		}
		r.info("%s %d files on %s instead of copying them again (%s)", verb, len(res.done), r.req.Pair.Replica.Name, HumanBytes(res.bytes))
	}
	if res.mismatched > 0 {
		r.info("%d files matched by size and date but differ in content; they will be copied", res.mismatched)
	}
	if res.skipped > 0 {
		msg := fmt.Sprintf("%d renames skipped; those files will be copied instead", res.skipped)
		if res.firstSkipError != nil {
			msg += fmt.Sprintf(" (first: %v)", res.firstSkipError)
		}
		r.warn("%s", msg)
	}
	r.rec.Stats.FilesRenamed += int64(len(res.done))
	r.rec.Stats.BytesRenamed += res.bytes
	return len(res.done), nil
}

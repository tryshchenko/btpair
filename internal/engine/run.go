// Package engine orchestrates a backup run: preflight checks, extra steps,
// protecting the main drive, analysing and mirroring to the replica, and writing
// metadata. It reports everything through a channel of events so any front
// end (TUI or plain log) can render it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/rsync"
	"github.com/tryshchenko/btpair/internal/steps"
	"github.com/tryshchenko/btpair/internal/volume"
)

type PhaseKind int

const (
	KindCheck PhaseKind = iota
	KindStep
	KindSync
	KindMeta
)

type Phase struct {
	ID    string
	Title string
	Kind  PhaseKind
}

const (
	PhasePreflight = "preflight"
	PhaseProtect   = "protect"
	PhaseAnalyze   = "analyze"
	PhaseSync      = "sync"
	PhaseFinalize  = "finalize"
)

type Request struct {
	Pair            *Pair
	Config          *config.Config
	Steps           []steps.Step // the selected extra steps, in order
	DryRun          bool
	AllowMassDelete bool
	Checksum        bool // compare file contents, catching same-size/same-mtime edits
	RsyncBin        string
}

// Phases lists what a run will go through, so a UI can draw it up front.
func Phases(req Request) []Phase {
	ps := []Phase{{PhasePreflight, "Preflight checks", KindCheck}}
	for _, s := range req.Steps {
		ps = append(ps, Phase{"step:" + s.ID(), s.Title(), KindStep})
	}
	return append(ps,
		Phase{PhaseProtect, "Lock main drive read-only", KindCheck},
		Phase{PhaseAnalyze, "Analyze changes", KindSync},
		Phase{PhaseSync, "Mirror main → replica", KindSync},
		Phase{PhaseFinalize, "Write metadata", KindMeta},
	)
}

type PhaseStatus int

const (
	StatusPending PhaseStatus = iota
	StatusRunning
	StatusDone
	StatusSkipped
	StatusWarn
	StatusFailed
)

type Level int

// LogKind tags file-level activity so a UI can style it.
type LogKind int

const (
	KindNote LogKind = iota
	KindCopy
	KindDelete
)

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

// Events sent by Run.
type (
	PhaseStarted  struct{ ID string }
	PhaseFinished struct {
		ID     string
		Status PhaseStatus
		Note   string
	}
	Progress struct {
		ID          string
		Done        int64
		Total       int64
		Unit        string // "" or "bytes"
		Speed       float64
		Current     string
		CurrentSize int64
		Files       int64
		FilesTotal  int64
		Deleted     int64
	}
	Log struct {
		At    time.Time
		Level Level
		Kind  LogKind
		Text  string
	}
	Planned  struct{ Plan *rsync.Plan }
	Finished struct{ Record meta.RunRecord }
)

type Event any

const (
	maxLog    = 2000
	maxErrors = 1000
)

type runner struct {
	req      Request
	pc       config.PairConfig
	ev       chan<- Event
	rec      meta.RunRecord
	started  bool // in_progress was written to the drives
	failed   bool // a fatal error stopped the run
	partial  bool // something did not complete
	lastProg time.Time
	lock     *os.File
}

// Run executes the request and closes ev when done. The final event is
// always Finished.
func Run(ctx context.Context, req Request, ev chan<- Event) {
	defer close(ev)
	now := time.Now()
	host, _ := os.Hostname()
	r := &runner{req: req, ev: ev, pc: req.Config.Pair(req.Pair.ID)}
	r.rec = meta.RunRecord{
		State:  meta.State{Status: meta.StatusInProgress, RunID: meta.NewRunID(now), Host: host, StartedAt: now},
		PairID: req.Pair.ID,
		DryRun: req.DryRun,
	}
	if req.Pair.Main != nil {
		r.rec.MainUUID = req.Pair.Main.UUID
	}
	if req.Pair.Replica != nil {
		r.rec.ReplicaUUID = req.Pair.Replica.UUID
	}

	r.run(ctx)

	r.rec.FinishedAt = time.Now()
	switch {
	case ctx.Err() != nil:
		r.rec.Status = meta.StatusCancelled
		r.rec.Message = "cancelled by user"
	case r.failed:
		r.rec.Status = meta.StatusFailed
	case req.DryRun:
		r.rec.Status = meta.StatusDryRun
	case r.partial:
		r.rec.Status = meta.StatusPartial
	default:
		r.rec.Status = meta.StatusOK
		r.rec.LastSuccessAt = r.rec.FinishedAt
	}
	r.finalize()
	if r.lock != nil {
		r.lock.Close()
	}
	ev <- Finished{Record: r.rec}
}

func (r *runner) run(ctx context.Context) {
	mainVol, replica := r.req.Pair.Main, r.req.Pair.Replica

	if !r.phase(PhasePreflight, func() (PhaseStatus, string, error) { return r.preflight() }) {
		return
	}

	if !r.req.DryRun {
		r.rec.LastSuccessAt = r.prevSuccess()
		for _, v := range []*volume.Volume{mainVol, replica} {
			if err := meta.SaveState(v.Path, r.rec.State); err != nil {
				r.fail("cannot write state to %s: %v", v.Name, err)
				return
			}
		}
		r.started = true
	}

	for _, s := range r.req.Steps {
		if ctx.Err() != nil {
			return
		}
		r.runStep(ctx, s)
	}
	if ctx.Err() != nil {
		return
	}

	protected := false
	ok := r.phase(PhaseProtect, func() (PhaseStatus, string, error) {
		switch {
		case r.req.DryRun:
			return StatusSkipped, "dry run", nil
		case mainVol.Dev:
			return StatusSkipped, "dev drive", nil
		case r.pc.AllowWritableMain:
			return StatusWarn, "disabled in config", nil
		}
		r.info("remounting %s read-only", mainVol.Name)
		if err := mainVol.RemountReadOnly(ctx); err != nil {
			return StatusFailed, "", fmt.Errorf("could not remount the main drive read-only (close apps using it, or set allow_writable_main): %w", err)
		}
		protected = true
		return StatusDone, "mounted read-only", nil
	})
	defer func() {
		if protected {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := mainVol.RemountReadWrite(rctx); err != nil {
				r.errorf("could not remount %s read-write: %v — run `diskutil mount %s`", mainVol.Name, err, mainVol.Device)
			} else {
				r.info("%s is writable again", mainVol.Name)
			}
		}
	}()
	if !ok {
		return
	}

	opts := rsync.Options{
		Bin:      r.req.RsyncBin,
		Src:      mainVol.Path,
		Dst:      replica.Path,
		Paths:    r.pc.Paths,
		BWLimit:  r.pc.BWLimit,
		Checksum: r.req.Checksum,
		Excludes: append(append([]string{"/" + meta.Dir + "/"}, config.DefaultExcludes...), r.pc.Exclude...),
	}
	if !r.pc.NoTrash {
		opts.TrashDir = filepath.Join(meta.TrashDir(replica.Path), r.rec.RunID)
	}

	var plan *rsync.Plan
	if !r.phase(PhaseAnalyze, func() (PhaseStatus, string, error) {
		r.progress(Progress{ID: PhaseAnalyze, Current: "scanning both drives…"}, true)
		p, err := rsync.Analyze(ctx, opts)
		if err != nil {
			return StatusFailed, "", err
		}
		plan = p
		r.ev <- Planned{Plan: p}
		r.rec.Stats.FilesTotal, r.rec.Stats.BytesTotal = p.FilesTotal, p.BytesTotal
		note := fmt.Sprintf("%d to copy · %d to delete · %s", p.FilesToCopy, p.FilesToDelete, HumanBytes(p.BytesToCopy))
		if p.BytesToDelete > 0 {
			note = fmt.Sprintf("%d to copy (%s) · %d to delete (%s)", p.FilesToCopy, HumanBytes(p.BytesToCopy), p.FilesToDelete, HumanBytes(p.BytesToDelete))
		}
		return r.guards(p, replica, note)
	}) {
		return
	}

	r.phase(PhaseSync, func() (PhaseStatus, string, error) {
		if r.req.DryRun {
			for _, c := range plan.Copies {
				r.fileEvent(KindCopy, c)
			}
			if n := plan.FilesToCopy - int64(len(plan.Copies)); n > 0 {
				r.info("… and %d more files to copy", n)
			}
			for _, d := range plan.Deletions {
				r.fileEvent(KindDelete, d)
			}
			if n := plan.FilesToDelete - int64(len(plan.Deletions)); n > 0 {
				r.info("… and %d more deletions", n)
			}
			return StatusSkipped, "dry run — nothing written", nil
		}
		if plan.FilesToCopy == 0 && plan.FilesToDelete == 0 {
			return StatusDone, "already in sync", nil
		}
		if n, stuck := unlockTree(replica.Path, r.pc.Paths, meta.Dir); n > 0 || len(stuck) > 0 {
			if n > 0 {
				r.info("unlocked %d locked items on %s so they can be updated (Finder's Locked flag)", n, replica.Name)
			}
			for _, p := range stuck {
				r.warn("could not unlock %s on %s", p, replica.Name)
			}
		}
		total := plan.BytesToCopy
		filesTotal := plan.FilesToCopy
		problems := newProblemLog(r, mainVol.Path, replica.Path)
		res, err := rsync.Run(ctx, opts, func(u rsync.Update) {
			if u.Error != "" && ctx.Err() == nil { // rsync complains about SIGINT on cancel
				problems.add(u.Error)
			}
			if u.Deleting != "" {
				r.fileEvent(KindDelete, u.Deleting)
			}
			if u.Copied != "" {
				r.fileEvent(KindCopy, u.Copied)
			}
			r.progress(Progress{
				ID: PhaseSync, Done: u.Bytes, Total: total, Unit: "bytes", Speed: u.Speed,
				Current: u.Current, CurrentSize: u.CurrentSize, Files: u.FilesDone, FilesTotal: filesTotal, Deleted: u.Deleted,
			}, false)
		})
		if res != nil {
			r.rec.Stats.BytesTransferred = res.Bytes
			r.rec.Stats.FilesTransferred = res.FilesDone
			r.rec.Stats.FilesDeleted = res.Deleted
			r.progress(Progress{
				ID: PhaseSync, Done: res.Bytes, Total: total, Unit: "bytes",
				Files: res.FilesDone, FilesTotal: filesTotal, Deleted: res.Deleted,
			}, true)
			seen := map[string]bool{}
			for _, p := range res.FailedPath {
				if !seen[p] {
					seen[p] = true
					r.rec.Pending = append(r.rec.Pending, meta.PendingItem{Kind: "file", Path: p, Reason: problems.reason(p)})
				}
			}
		}
		problems.flush()
		if err != nil {
			return StatusFailed, "", err
		}
		note := fmt.Sprintf("%d copied · %d deleted · %s", res.FilesDone, res.Deleted, HumanBytes(res.Bytes))
		if problems.total > 0 {
			r.partial = true
			return StatusWarn, fmt.Sprintf("%s · %d errors", note, problems.total), nil
		}
		return StatusDone, note, nil
	})
}

func (r *runner) preflight() (PhaseStatus, string, error) {
	p := r.req.Pair
	switch {
	case p.Main == nil:
		return StatusFailed, "", errors.New("main drive is not connected")
	case p.Replica == nil:
		return StatusFailed, "", errors.New("replica drive is not connected")
	case len(p.Problems) > 0:
		return StatusFailed, "", errors.New(p.Problems[0])
	}
	m, s := p.Main, p.Replica
	if m.Identity.Role != meta.RoleMain || s.Identity.Role != meta.RoleReplica {
		return StatusFailed, "", errors.New("drive roles do not match metadata")
	}
	if m.Path == s.Path {
		return StatusFailed, "", errors.New("main and replica resolve to the same path")
	}
	for _, v := range []*volume.Volume{m, s} {
		if _, err := os.Stat(v.Path); err != nil {
			return StatusFailed, "", fmt.Errorf("%s is not reachable: %w", v.Name, err)
		}
	}
	// Re-read identities from disk right before acting, in case drives were
	// swapped since detection.
	for _, v := range []*volume.Volume{m, s} {
		id, err := meta.LoadIdentity(v.Path)
		if err != nil || id == nil || id.VolumeUUID != v.UUID || id.PairID != p.ID {
			return StatusFailed, "", fmt.Errorf("%s metadata changed since detection — rescan", v.Name)
		}
	}
	if !volume.HasContent(m.Path) {
		return StatusFailed, "", fmt.Errorf("main drive %s looks empty — refusing to mirror it onto the replica", m.Name)
	}
	if s.ReadOnly && !r.req.DryRun {
		return StatusFailed, "", fmt.Errorf("replica %s is mounted read-only", s.Name)
	}
	if r.req.RsyncBin == "" {
		bin, _, err := rsync.Find(r.req.Config.Rsync)
		if err != nil {
			return StatusFailed, "", err
		}
		r.req.RsyncBin = bin
	}
	if !r.req.DryRun {
		f, err := lockPair(s.Path)
		if err != nil {
			return StatusFailed, "", err
		}
		r.lock = f
	}
	if st := p.State(); st != nil && st.Status == meta.StatusInProgress {
		r.warn("previous run %s did not finish — it will be completed now", st.RunID)
	}
	selected := map[string]bool{}
	for _, s := range r.req.Steps {
		selected[s.ID()] = true
	}
	for _, it := range r.prevPending() {
		// Files are retried by every sync; steps only when selected, so
		// unselected failed steps stay pending.
		if it.Kind == "step" && !selected[it.Path] {
			r.rec.Pending = append(r.rec.Pending, it)
			r.warn("step %s is still pending from an earlier run (not selected)", it.Path)
			continue
		}
		r.info("retrying from last run: %s %s", it.Kind, it.Path)
	}
	return StatusDone, fmt.Sprintf("%s → %s", m.Name, s.Name), nil
}

func (r *runner) guards(p *rsync.Plan, replica *volume.Volume, note string) (PhaseStatus, string, error) {
	if err := r.deleteLimit(p); err != nil {
		return StatusFailed, note, err
	}
	if err := r.makeRoom(p, replica); err != nil {
		return StatusFailed, note, err
	}
	return StatusDone, note, nil
}

func (r *runner) runStep(ctx context.Context, s steps.Step) {
	id := "step:" + s.ID()
	env := steps.Env{
		PairID:   r.req.Pair.ID,
		Main:     r.req.Pair.Main.Path,
		StateDir: filepath.Join(meta.StepsDir(r.req.Pair.Main.Path), s.ID()),
		DryRun:   r.req.DryRun,
	}
	res := meta.StepResult{ID: s.ID(), Title: s.Title()}
	start := time.Now()
	r.phase(id, func() (PhaseStatus, string, error) {
		r.progress(Progress{ID: id, Current: "planning…"}, true)
		plan, err := s.Plan(ctx, env)
		if err != nil {
			return StatusFailed, "", fmt.Errorf("planning failed: %w", err)
		}
		if plan.UpToDate {
			res.Skipped = true
			return StatusSkipped, plan.Summary, nil
		}
		if r.req.DryRun {
			return StatusSkipped, "would run: " + plan.Summary, nil
		}
		summary, err := s.Run(ctx, env, &stepReporter{r: r, id: id})
		if err != nil {
			return StatusFailed, "", err
		}
		if summary == "" {
			summary = "done"
		}
		return StatusDone, summary, nil
	}, func(st PhaseStatus, note string, err error) {
		res.Duration = time.Since(start)
		res.Message = note
		switch st {
		case StatusFailed:
			res.Status = meta.StatusFailed
			if err != nil {
				res.Message = err.Error()
			}
			if ctx.Err() == nil {
				r.partial = true
				r.rec.Pending = append(r.rec.Pending, meta.PendingItem{Kind: "step", Path: s.ID(), Reason: res.Message})
			}
		default:
			res.Status = meta.StatusOK
		}
		r.rec.Steps = append(r.rec.Steps, res)
	})
}

// phase runs fn as the named phase. A failed phase is fatal unless it is an
// extra step. Optional after hooks observe the outcome.
func (r *runner) phase(id string, fn func() (PhaseStatus, string, error), after ...func(PhaseStatus, string, error)) bool {
	r.ev <- PhaseStarted{ID: id}
	st, note, err := fn()
	if err != nil && st != StatusFailed {
		st = StatusFailed
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			note = "cancelled"
		} else {
			r.errorf("%s", err)
			if note == "" {
				note = err.Error()
			}
		}
	}
	for _, a := range after {
		a(st, note, err)
	}
	shown := st
	if errors.Is(err, context.Canceled) {
		shown = StatusWarn
	}
	r.ev <- PhaseFinished{ID: id, Status: shown, Note: note}
	if st != StatusFailed || strings.HasPrefix(id, "step:") {
		return true
	}
	if !errors.Is(err, context.Canceled) {
		r.failed = true
		if r.rec.Message == "" && err != nil {
			r.rec.Message = err.Error()
		}
	}
	return false
}

func (r *runner) finalize() {
	if !r.started {
		// Nothing was written to the drives; still mark the phase.
		r.ev <- PhaseFinished{ID: PhaseFinalize, Status: StatusSkipped, Note: map[bool]string{true: "dry run — drives untouched", false: "drives untouched"}[r.req.DryRun]}
		return
	}
	r.ev <- PhaseStarted{ID: PhaseFinalize}
	mainVol, replica := r.req.Pair.Main, r.req.Pair.Replica
	var errs []string
	for _, v := range []*volume.Volume{mainVol, replica} {
		if err := meta.SaveState(v.Path, r.rec.State); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", v.Name, err))
			continue
		}
		if err := meta.SaveRun(v.Path, r.rec); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", v.Name, err))
		}
	}
	if err := meta.PruneTrash(replica.Path, r.pc.TrashKeep); err != nil {
		r.warn("trash cleanup: %v", err)
	}
	if len(errs) > 0 {
		for _, e := range errs {
			r.errorf("metadata: %s", e)
		}
		r.ev <- PhaseFinished{ID: PhaseFinalize, Status: StatusFailed, Note: "could not write metadata"}
		return
	}
	r.ev <- PhaseFinished{ID: PhaseFinalize, Status: StatusDone, Note: "state saved on both drives"}
}

func (r *runner) prevSuccess() time.Time {
	if st := r.req.Pair.State(); st != nil {
		return st.LastSuccessAt
	}
	return time.Time{}
}

func (r *runner) prevPending() []meta.PendingItem {
	if st := r.req.Pair.State(); st != nil {
		return st.Pending
	}
	return nil
}

func (r *runner) progress(p Progress, force bool) {
	if !force && time.Since(r.lastProg) < 60*time.Millisecond {
		return
	}
	r.lastProg = time.Now()
	r.ev <- p
}

func (r *runner) log(l Level, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.ev <- Log{At: time.Now(), Level: l, Text: msg}
	prefix := map[Level]string{LevelInfo: "", LevelWarn: "warn: ", LevelError: "error: "}[l]
	if len(r.rec.Log) < maxLog {
		r.rec.Log = append(r.rec.Log, time.Now().Format("15:04:05 ")+prefix+msg)
	}
	if l == LevelError && len(r.rec.Errors) < maxErrors {
		r.rec.Errors = append(r.rec.Errors, msg)
	}
}

// fileEvent reports a copied/deleted path. These go to the UI only; the
// run record keeps counts rather than every path.
func (r *runner) fileEvent(k LogKind, path string) {
	r.ev <- Log{At: time.Now(), Level: LevelInfo, Kind: k, Text: path}
}

func (r *runner) info(f string, a ...any)   { r.log(LevelInfo, f, a...) }
func (r *runner) warn(f string, a ...any)   { r.log(LevelWarn, f, a...) }
func (r *runner) errorf(f string, a ...any) { r.log(LevelError, f, a...) }
func (r *runner) fail(f string, a ...any) {
	r.errorf(f, a...)
	r.failed = true
	r.rec.Message = fmt.Sprintf(f, a...)
}

type stepReporter struct {
	r           *runner
	id          string
	done, total int64
	unit, cur   string
}

func (s *stepReporter) emit(force bool) {
	s.r.progress(Progress{ID: s.id, Done: s.done, Total: s.total, Unit: s.unit, Current: s.cur}, force)
}
func (s *stepReporter) Progress(done, total int64, unit string) {
	s.done, s.total, s.unit = done, total, unit
	s.emit(done == total)
}
func (s *stepReporter) Current(item string) { s.cur = item; s.emit(false) }
func (s *stepReporter) Log(msg string)      { s.r.info("%s", msg) }
func (s *stepReporter) Warn(msg string)     { s.r.warn("%s", msg) }

// lockPair prevents two btpair processes from syncing the same pair at once.
func lockPair(replicaPath string) (*os.File, error) {
	p := filepath.Join(replicaPath, meta.Dir, "lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another btpair run is already syncing this pair")
	}
	return f, nil
}

// HumanBytes formats a byte count like "1.4 GB".
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

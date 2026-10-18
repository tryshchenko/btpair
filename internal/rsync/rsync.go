// Package rsync drives a one-way main → replica rsync and turns its output
// into structured progress updates.
package rsync

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var candidates = []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"}

// Find locates an rsync >= 3.1 (needed for --info=progress2). macOS ships
// openrsync, which is not good enough.
func Find(preferred string) (string, string, error) {
	paths := candidates
	if preferred != "" {
		paths = []string{preferred}
	} else if p, err := exec.LookPath("rsync"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			continue
		}
		m := regexp.MustCompile(`rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`).FindSubmatch(out)
		if m == nil {
			continue
		}
		major, _ := strconv.Atoi(string(m[1]))
		minor, _ := strconv.Atoi(string(m[2]))
		if major > 3 || (major == 3 && minor >= 1) {
			return p, fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3]), nil
		}
	}
	return "", "", errors.New("rsync 3.1+ not found — install it with `brew install rsync`")
}

type Options struct {
	Bin      string
	Src, Dst string   // drive roots
	Paths    []string // relative to Src; empty = whole drive
	Excludes []string
	// Perishable excludes are never copied, but don't keep a folder alive
	// on the replica: a folder deleted on main is removed even if it still
	// holds such files (exFAT's "._" metadata files, Finder's .DS_Store).
	Perishable []string
	TrashDir   string // absolute; empty disables --backup
	BWLimit    string
	Checksum   bool // compare content, not just size and mtime (slow)
}

func (o Options) args(dryRun bool) []string {
	a := []string{
		"-aHX", "--delete", "--delete-during",
		"--no-human-readable", "--no-inc-recursive",
		"--out-format=@@%i|%l|%n",
	}
	if dryRun {
		a = append(a, "--dry-run", "--stats")
	} else {
		a = append(a, "--info=progress2", "--stats")
	}
	if o.TrashDir != "" {
		a = append(a, "--backup", "--backup-dir="+o.TrashDir)
	}
	if o.Checksum {
		a = append(a, "--checksum")
	}
	if o.BWLimit != "" && !dryRun {
		a = append(a, "--bwlimit="+o.BWLimit)
	}
	for _, e := range o.Excludes {
		a = append(a, "--exclude="+e)
	}
	for _, e := range o.Perishable {
		a = append(a, "--filter=-p "+e)
	}
	src := strings.TrimSuffix(o.Src, "/")
	dst := strings.TrimSuffix(o.Dst, "/") + "/"
	if len(o.Paths) == 0 {
		return append(a, src+"/", dst)
	}
	// --relative with "/./" anchors keeps each path's structure under dst.
	a = append(a, "--relative")
	for _, p := range o.Paths {
		a = append(a, src+"/./"+strings.Trim(filepath.Clean(p), "/"))
	}
	return append(a, dst)
}

// Plan is the outcome of a dry run.
type Plan struct {
	FilesTotal    int64 // files on main within scope
	BytesTotal    int64
	FilesToCopy   int64 // new or changed regular files
	FilesNew      int64
	FilesCreated  int64 // entries (files + dirs) that do not exist on replica yet
	FilesToDelete int64
	BytesToCopy   int64
	BytesToDelete int64    // size of the replica files that would be deleted
	Deletions     []string // sample, capped
	Copies        []string // sample, capped
	// Every regular file that would be deleted from the replica, and every
	// regular file that would be newly created on it: the raw material for
	// rename detection. Paths are relative to the drive roots.
	Gone  []Entry
	Added []Entry
}

// Entry is a file path (relative to the drive root) and its size.
type Entry struct {
	Path string
	Size int64
}

var statLine = regexp.MustCompile(`^(Number of files|Number of created files|Number of deleted files|Number of regular files transferred|Total file size|Total transferred file size):\s*([\d,]+)`)

func parseNum(s string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	return n
}

// Analyze runs rsync in dry-run mode to learn how much work there is.
func Analyze(ctx context.Context, o Options) (*Plan, error) {
	cmd := exec.CommandContext(ctx, o.Bin, o.args(true)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && ctx.Err() == nil {
		if code := exitCode(err); code != 23 && code != 24 {
			return nil, fmt.Errorf("rsync dry run failed (exit %d): %s", code, lastLines(stderr.String(), 3))
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p := &Plan{}
	for _, line := range strings.Split(string(out), "\n") {
		if item, ok := parseItem(line); ok {
			switch {
			case item.Deleting:
				if len(p.Deletions) < 200 {
					p.Deletions = append(p.Deletions, item.Name)
				}
				// rsync doesn't report sizes for deletions; look on the replica.
				if !strings.HasSuffix(item.Name, "/") {
					if fi, err := os.Lstat(filepath.Join(o.Dst, item.Name)); err == nil && fi.Mode().IsRegular() {
						p.BytesToDelete += fi.Size()
						p.Gone = append(p.Gone, Entry{item.Name, fi.Size()})
					}
				}
			case item.IsFile:
				if item.New {
					p.FilesNew++
					p.Added = append(p.Added, Entry{item.Name, item.Size})
				}
				if len(p.Copies) < 200 {
					p.Copies = append(p.Copies, item.Name)
				}
			}
			continue
		}
		if m := statLine.FindStringSubmatch(line); m != nil {
			n := parseNum(m[2])
			switch m[1] {
			case "Number of files":
				p.FilesTotal = n
			case "Number of created files":
				p.FilesCreated = n
			case "Number of deleted files":
				p.FilesToDelete = n
			case "Number of regular files transferred":
				p.FilesToCopy = n
			case "Total file size":
				p.BytesTotal = n
			case "Total transferred file size":
				p.BytesToCopy = n
			}
		}
	}
	return p, nil
}

type item struct {
	Deleting bool
	IsFile   bool
	New      bool
	Size     int64
	Name     string
}

// parseItem decodes a line produced by --out-format=@@%i|%l|%n.
func parseItem(line string) (item, bool) {
	if !strings.HasPrefix(line, "@@") {
		return item{}, false
	}
	parts := strings.SplitN(line[2:], "|", 3)
	if len(parts) != 3 {
		return item{}, false
	}
	code := parts[0]
	it := item{Name: parts[2]}
	it.Size, _ = strconv.ParseInt(parts[1], 10, 64)
	if strings.HasPrefix(code, "*deleting") {
		it.Deleting = true
		return it, true
	}
	if len(code) >= 3 {
		it.IsFile = code[1] == 'f'
		it.New = strings.Contains(code[2:], "+++")
	}
	return it, true
}

// Update is a progress snapshot emitted while syncing.
type Update struct {
	Bytes       int64 // transferred so far (cumulative)
	Speed       float64
	FilesDone   int64
	Deleted     int64
	Current     string
	CurrentSize int64
	Deleting    string // path deleted by this line, if any
	Copied      string // path whose transfer starts with this line, if any
	Error       string // an rsync error line, if any
}

type Result struct {
	ExitCode   int
	Bytes      int64
	FilesDone  int64
	Deleted    int64
	Errors     []string
	FailedPath []string // drive-relative paths named by error lines
}

var progressLine = regexp.MustCompile(`^\s*([\d,]+)\s+(\d+)%\s+([\d.]+)([kMGT]?B)/s\s+(\S+)(?:\s+\(xfr#(\d+), (?:to|ir)-chk=(\d+)/(\d+)\))?`)

// Run performs the real sync, calling onUpdate as output arrives.
func Run(ctx context.Context, o Options, onUpdate func(Update)) (*Result, error) {
	if o.TrashDir != "" {
		if err := os.MkdirAll(o.TrashDir, 0o755); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, o.Bin, o.args(false)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	res := &Result{}
	errLines := make(chan string, 64)
	go func() {
		defer close(errLines)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			errLines <- sc.Text()
		}
	}()

	var u Update
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(splitCRLF)
	inStats := false
	for sc.Scan() {
		line := sc.Text()
		drainErrors(errLines, res, &u, onUpdate)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Number of files:") {
			inStats = true
		}
		if inStats {
			continue
		}
		if it, ok := parseItem(line); ok {
			if it.Deleting {
				u.Deleted++
				u.Deleting = it.Name
				onUpdate(u)
				u.Deleting = ""
			} else if it.IsFile {
				// A file counts as done once the next one starts (or rsync
				// exits cleanly), so a cancelled transfer is not counted.
				if u.Current != "" {
					u.FilesDone++
				}
				u.Current, u.CurrentSize = it.Name, it.Size
				u.Copied = it.Name
				onUpdate(u)
				u.Copied = ""
			}
			continue
		}
		if m := progressLine.FindStringSubmatch(line); m != nil {
			u.Bytes = parseNum(m[1])
			u.Speed = speed(m[3], m[4])
			onUpdate(u)
		}
	}
	waitErr := cmd.Wait()
	for l := range errLines {
		recordError(l, res, &u, onUpdate)
	}
	if waitErr == nil && u.Current != "" {
		u.FilesDone++
	}
	res.Bytes, res.FilesDone, res.Deleted = u.Bytes, u.FilesDone, u.Deleted
	for _, l := range res.Errors {
		if pr := ParseError(l, o.Src, o.Dst); !pr.Summary && pr.Path != "" {
			res.FailedPath = append(res.FailedPath, pr.Path)
		}
	}
	res.ExitCode = exitCode(waitErr)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	switch res.ExitCode {
	case 0, 23, 24: // 23/24: partial transfer; reported through res.Errors
		return res, nil
	}
	return res, fmt.Errorf("rsync exited with code %d: %s", res.ExitCode, lastLines(strings.Join(res.Errors, "\n"), 2))
}

func drainErrors(ch chan string, res *Result, u *Update, onUpdate func(Update)) {
	for {
		select {
		case l, ok := <-ch:
			if !ok {
				return
			}
			recordError(l, res, u, onUpdate)
		default:
			return
		}
	}
}

func recordError(line string, res *Result, u *Update, onUpdate func(Update)) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	res.Errors = append(res.Errors, line)
	u.Error = line
	onUpdate(*u)
	u.Error = ""
}

func speed(num, unit string) float64 {
	f, _ := strconv.ParseFloat(num, 64)
	switch unit {
	case "kB":
		f *= 1 << 10
	case "MB":
		f *= 1 << 20
	case "GB":
		f *= 1 << 30
	case "TB":
		f *= 1 << 40
	}
	return f
}

// splitCRLF splits on either \r or \n, since progress2 redraws with \r.
func splitCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
}

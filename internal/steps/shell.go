package steps

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tryshchenko/btpair/internal/config"
)

// Shell runs a user-defined command. It lets new steps be prototyped in
// config before (or instead of) becoming built-ins.
//
// The command runs via `sh -c` in the main drive root with BT_MAIN, BT_PAIR,
// BT_STATE_DIR and BT_DRY_RUN set. Lines on stdout may use a tiny protocol:
//
//	::progress <done> <total> [bytes]
//	::item <name>
//	::warn <message>
//
// Anything else is shown as activity. `skip_if`, when set, is run during
// planning: exit 0 means up to date (its first output line is the summary).
type Shell struct{ base }

func init() {
	Register("shell", func(id string, cfg config.StepConfig) (Step, error) {
		if strings.TrimSpace(cfg.Run) == "" {
			return nil, errors.New("shell step needs `run`")
		}
		return &Shell{base{id, cfg}}, nil
	})
}

func (s *Shell) command(ctx context.Context, env Env, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Dir = env.Main
	cmd.Env = append(os.Environ(),
		"BT_MAIN="+env.Main,
		"BT_PAIR="+env.PairID,
		"BT_STATE_DIR="+env.StateDir,
		"BT_DRY_RUN="+strconv.FormatBool(env.DryRun),
	)
	for k, v := range s.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func (s *Shell) Plan(ctx context.Context, env Env) (Plan, error) {
	if s.cfg.SkipIf == "" {
		return Plan{Summary: "runs every time"}, nil
	}
	if err := os.MkdirAll(env.StateDir, 0o755); err != nil {
		return Plan{}, err
	}
	out, err := s.command(ctx, env, s.cfg.SkipIf).Output()
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err == nil {
		if first == "" {
			first = "up to date"
		}
		return Plan{Summary: first, UpToDate: true}, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Plan{}, err
	}
	if first == "" {
		first = "has work to do"
	}
	return Plan{Summary: first}, nil
}

func (s *Shell) Run(ctx context.Context, env Env, r Reporter) (string, error) {
	if err := os.MkdirAll(env.StateDir, 0o755); err != nil {
		return "", err
	}
	cmd := s.command(ctx, env, s.cfg.Run)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = errW
	if err := cmd.Start(); err != nil {
		errR.Close()
		errW.Close()
		return "", err
	}
	errW.Close()

	var lastErr, lastOut string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(errR)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				lastErr = l
				r.Warn(l)
			}
		}
	}()

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "::progress "):
			f := strings.Fields(line)
			if len(f) >= 3 {
				d, _ := strconv.ParseInt(f[1], 10, 64)
				t, _ := strconv.ParseInt(f[2], 10, 64)
				unit := ""
				if len(f) > 3 {
					unit = f[3]
				}
				r.Progress(d, t, unit)
			}
		case strings.HasPrefix(line, "::item "):
			r.Current(strings.TrimPrefix(line, "::item "))
		case strings.HasPrefix(line, "::warn "):
			r.Warn(strings.TrimPrefix(line, "::warn "))
		case strings.TrimSpace(line) != "":
			lastOut = strings.TrimSpace(line)
			r.Log(line)
		}
	}
	<-done
	errR.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if lastErr != "" {
			return "", fmt.Errorf("%w: %s", err, lastErr)
		}
		return "", err
	}
	// The last plain output line is the step's result summary.
	return lastOut, nil
}

// Package steps defines "extra steps": work that runs against the main drive
// before it is mirrored to the replica (fetching repositories, exporting a
// photo library, …).
//
// A step is created from config by a Factory registered under a type name.
// Every step can Plan (cheaply decide whether there is anything to do, shown
// in the picker before a run) and Run. Steps keep whatever incremental state
// they need in Env.StateDir on the main drive, so repeated runs only do new work.
package steps

import (
	"context"
	"fmt"
	"sort"

	"github.com/tryshchenko/btpair/internal/config"
)

type Env struct {
	PairID   string
	Main     string // main drive root
	StateDir string // private, persistent directory for this step
	DryRun   bool
}

// Plan is a step's pre-run assessment.
type Plan struct {
	Summary  string // short, e.g. "4 repos changed" or "up to date"
	UpToDate bool   // nothing to do; Run is skipped
}

// Reporter is how a running step talks to the UI.
type Reporter interface {
	Progress(done, total int64, unit string) // unit: "" (items) or "bytes"
	Current(item string)
	Log(msg string)
	Warn(msg string)
}

type Step interface {
	ID() string
	Title() string
	Description() string
	DefaultSelected() bool
	Plan(ctx context.Context, env Env) (Plan, error)
	// Run does the work and returns a short result summary.
	Run(ctx context.Context, env Env, r Reporter) (string, error)
}

type Factory func(id string, cfg config.StepConfig) (Step, error)

var registry = map[string]Factory{}

// Register makes a step type available to config. Built-in steps call it
// from init().
func Register(typeName string, f Factory) { registry[typeName] = f }

// ForPair builds the steps offered for a pair: the pair's `steps` list if
// set, otherwise every configured step, in ID order.
func ForPair(cfg *config.Config, pairID string) ([]Step, error) {
	ids := cfg.Pair(pairID).Steps
	if len(ids) == 0 {
		for id := range cfg.Steps {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	var out []Step
	for _, id := range ids {
		sc, ok := cfg.Steps[id]
		if !ok {
			return nil, fmt.Errorf("pair %q references unknown step %q", pairID, id)
		}
		typ := sc.Type
		if typ == "" {
			typ = id
		}
		f, ok := registry[typ]
		if !ok {
			return nil, fmt.Errorf("step %q: unknown type %q", id, typ)
		}
		s, err := f(id, sc)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", id, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// base implements the descriptive part of Step from config.
type base struct {
	id  string
	cfg config.StepConfig
}

func (b base) ID() string { return b.id }
func (b base) Title() string {
	if b.cfg.Title != "" {
		return b.cfg.Title
	}
	return b.id
}
func (b base) Description() string   { return b.cfg.Description }
func (b base) DefaultSelected() bool { return b.cfg.Default }

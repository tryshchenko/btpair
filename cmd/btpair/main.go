// Command btpair mirrors main drives onto their replica drives.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/tryshchenko/btpair/internal/config"
	"github.com/tryshchenko/btpair/internal/engine"
	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/steps"
	"github.com/tryshchenko/btpair/internal/tui"
	"github.com/tryshchenko/btpair/internal/volume"
)

const usage = `btpair — main/replica drive backups

usage:
  btpair [flags]                      interactive dashboard
  btpair status [flags]               print pairs and their last run
  btpair init  --main NAME --replica NAME --pair ID [--label TEXT]
  btpair sync  PAIR [--dry-run] [--checksum] [--steps a,b | --all-steps] [--allow-deletes]
  btpair empty-trash PAIR [--yes]     permanently delete the replica's trash

flags:
  --dev DIR       treat each subfolder of DIR as a drive (or set BT_DEV_ROOT)
  --config FILE   config path (default ~/.config/btpair/config.toml, or BT_CONFIG)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "btpair:", err)
		os.Exit(1)
	}
}

type globals struct {
	dev     string
	cfgPath string
	cfg     *config.Config
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.dev, "dev", os.Getenv("BT_DEV_ROOT"), "dev root")
	fs.StringVar(&g.cfgPath, "config", config.DefaultPath(), "config file")
}

func (g *globals) load() error {
	cfg, err := config.Load(g.cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	g.cfg = cfg
	return nil
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var g globals
	fs := flag.NewFlagSet("btpair "+cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	g.register(fs)

	switch cmd {
	case "":
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := g.load(); err != nil {
			return err
		}
		return tui.Run(tui.Options{DevRoot: g.dev, Config: g.cfg})
	case "status":
		if err := fs.Parse(args); err != nil {
			return err
		}
		return status(&g)
	case "init":
		mainName := fs.String("main", "", "main drive name or path")
		replicaName := fs.String("replica", "", "replica drive name or path")
		pair := fs.String("pair", "", "pair id")
		lbl := fs.String("label", "", "description")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return initPair(&g, *mainName, *replicaName, *pair, *lbl)
	case "sync":
		dry := fs.Bool("dry-run", false, "analyze only")
		stepList := fs.String("steps", "", "comma-separated step IDs to run")
		allSteps := fs.Bool("all-steps", false, "run every configured step")
		allowDel := fs.Bool("allow-deletes", false, "allow large deletions")
		checksum := fs.Bool("checksum", false, "compare file contents (slow)")
		pairID := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			pairID, args = args[0], args[1:]
		}
		if err := fs.Parse(args); err != nil {
			return err
		}
		if pairID == "" && fs.NArg() > 0 {
			pairID = fs.Arg(0)
		}
		if pairID == "" {
			return errors.New("sync: which pair? (btpair sync PAIR)")
		}
		return syncPlain(&g, pairID, *dry, *stepList, *allSteps, *allowDel, *checksum)
	case "empty-trash":
		yes := fs.Bool("yes", false, "really delete")
		pairID := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			pairID, args = args[0], args[1:]
		}
		if err := fs.Parse(args); err != nil {
			return err
		}
		if pairID == "" && fs.NArg() > 0 {
			pairID = fs.Arg(0)
		}
		if pairID == "" {
			return errors.New("empty-trash: which pair? (btpair empty-trash PAIR)")
		}
		return emptyTrash(&g, pairID, *yes)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

func detect(g *globals) ([]*volume.Volume, []*engine.Pair, []*volume.Volume, error) {
	if err := g.load(); err != nil {
		return nil, nil, nil, err
	}
	vols, err := volume.Detect(context.Background(), g.dev)
	if err != nil {
		return nil, nil, nil, err
	}
	pairs, unpaired := engine.Group(vols)
	return vols, pairs, unpaired, nil
}

func status(g *globals) error {
	_, pairs, unpaired, err := detect(g)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		fmt.Printf("%s", p.ID)
		if p.Label != "" {
			fmt.Printf("  (%s)", p.Label)
		}
		fmt.Println()
		for _, side := range []struct {
			role string
			v    *volume.Volume
		}{{"main", p.Main}, {"replica", p.Replica}} {
			if side.v == nil {
				fmt.Printf("  %-7s not connected\n", side.role)
				continue
			}
			sp, _ := side.v.Space()
			fmt.Printf("  %-7s %-20s %s free of %s\n", side.role, side.v.Name, engine.HumanBytes(int64(sp.Free)), engine.HumanBytes(int64(sp.Total)))
		}
		if p.Replica != nil {
			if runs, err := meta.ListTrash(p.Replica.Path); err == nil {
				if sum := engine.TrashSummary(runs); sum != "" {
					fmt.Printf("  trash   %s\n", sum)
				}
			}
		}
		if st := p.State(); st != nil && st.Status != meta.StatusNever {
			fmt.Printf("  last    %s at %s, %d copied, %d deleted, %d pending\n", st.Status, st.StartedAt.Local().Format(time.DateTime),
				st.Stats.FilesTransferred, st.Stats.FilesDeleted, len(st.Pending))
		} else {
			fmt.Println("  last    never")
		}
		for _, pr := range p.Problems {
			fmt.Println("  problem", pr)
		}
	}
	for _, v := range unpaired {
		fmt.Printf("unpaired  %s  (%s)\n", v.Name, v.Path)
	}
	return nil
}

func findVol(vols []*volume.Volume, key string) *volume.Volume {
	for _, v := range vols {
		if v.Name == key || v.Path == key || v.UUID == key {
			return v
		}
	}
	return nil
}

func initPair(g *globals, mainName, replicaName, pair, lbl string) error {
	vols, _, _, err := detect(g)
	if err != nil {
		return err
	}
	m, s := findVol(vols, mainName), findVol(vols, replicaName)
	if m == nil || s == nil {
		return fmt.Errorf("could not find drives %q and %q", mainName, replicaName)
	}
	if err := engine.InitPair(m, s, pair, lbl); err != nil {
		return err
	}
	fmt.Printf("pair %s: %s (main) → %s (replica)\n", pair, m.Name, s.Name)
	return nil
}

func connectedPair(g *globals, id string) (*engine.Pair, error) {
	_, pairs, _, err := detect(g)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("pair %q is not connected", id)
}

func emptyTrash(g *globals, pairID string, yes bool) error {
	p, err := connectedPair(g, pairID)
	if err != nil {
		return err
	}
	if p.Replica == nil {
		return fmt.Errorf("the replica of %s is not connected", pairID)
	}
	runs, err := meta.ListTrash(p.Replica.Path)
	if err != nil {
		return err
	}
	sum := engine.TrashSummary(runs)
	if sum == "" {
		fmt.Printf("the trash on %s is empty\n", p.Replica.Name)
		return nil
	}
	if !yes {
		for _, t := range runs {
			if t.Files > 0 {
				fmt.Printf("  %s  %10s  %d files\n", t.Time().Format(time.DateTime), engine.HumanBytes(t.Bytes), t.Files)
			}
		}
		fmt.Printf("trash on %s: %s\nthis permanently deletes it; rerun with --yes to proceed\n", p.Replica.Name, sum)
		return nil
	}
	files, bytes, err := engine.EmptyTrash(p)
	fmt.Printf("emptied %s (%d files) from %s\n", engine.HumanBytes(bytes), files, p.Replica.Name)
	return err
}

// syncPlain runs a backup with line-oriented output, for scripts and logs.
func syncPlain(g *globals, pairID string, dry bool, stepList string, all, allowDel, checksum bool) error {
	pair, err := connectedPair(g, pairID)
	if err != nil {
		return err
	}
	available, err := steps.ForPair(g.cfg, pairID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, s := range strings.Split(stepList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	var sel []steps.Step
	for _, s := range available {
		if all || want[s.ID()] {
			sel = append(sel, s)
			delete(want, s.ID())
		}
	}
	for id := range want {
		return fmt.Errorf("unknown step %q", id)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ch := make(chan engine.Event, 1024)
	req := engine.Request{Pair: pair, Config: g.cfg, Steps: sel, DryRun: dry, AllowMassDelete: allowDel, Checksum: checksum}
	titles := map[string]string{}
	for _, p := range engine.Phases(req) {
		titles[p.ID] = p.Title
	}
	go engine.Run(ctx, req, ch)

	var rec meta.RunRecord
	var lastPct int64 = -1
	for ev := range ch {
		switch e := ev.(type) {
		case engine.PhaseStarted:
			fmt.Printf("▸ %s\n", titles[e.ID])
		case engine.PhaseFinished:
			mark := map[engine.PhaseStatus]string{engine.StatusDone: "✓", engine.StatusSkipped: "–", engine.StatusWarn: "!", engine.StatusFailed: "✗"}[e.Status]
			fmt.Printf("  %s %s  %s\n", mark, titles[e.ID], e.Note)
		case engine.Progress:
			if e.Total > 0 {
				pct := e.Done * 100 / e.Total
				if pct/10 != lastPct/10 {
					lastPct = pct
					if e.Unit == "bytes" {
						fmt.Printf("    %3d%%  %s / %s\n", pct, engine.HumanBytes(e.Done), engine.HumanBytes(e.Total))
					} else {
						fmt.Printf("    %3d%%  %d / %d\n", pct, e.Done, e.Total)
					}
				}
			}
		case engine.Log:
			prefix := map[engine.Level]string{engine.LevelInfo: "   ", engine.LevelWarn: "  !", engine.LevelError: "  ✗"}[e.Level]
			switch e.Kind {
			case engine.KindCopy:
				prefix = "  +"
			case engine.KindDelete:
				prefix = "  −"
			}
			fmt.Printf("%s %s\n", prefix, e.Text)
		case engine.Finished:
			rec = e.Record
		}
	}
	fmt.Printf("\nresult: %s", rec.Status)
	if rec.Message != "" {
		fmt.Printf(" — %s", rec.Message)
	}
	fmt.Println()
	switch rec.Status {
	case meta.StatusOK, meta.StatusDryRun:
		return nil
	}
	os.Exit(2)
	return nil
}

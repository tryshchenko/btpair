// Package config loads the optional user configuration. The drives' own
// metadata decides pairing and roles; config only tunes behaviour per pair
// and defines extra steps.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Rsync string                `toml:"rsync"` // path to rsync 3.x; auto-detected when empty
	Pairs map[string]PairConfig `toml:"pairs"`
	Steps map[string]StepConfig `toml:"steps"`
}

type PairConfig struct {
	// Paths limits sync to these directories relative to the drive root.
	// Empty means the whole drive.
	Paths            []string `toml:"paths"`
	Exclude          []string `toml:"exclude"`
	Steps            []string `toml:"steps"` // step IDs offered for this pair
	MaxDeletePercent float64  `toml:"max_delete_percent"`
	TrashKeep        int      `toml:"trash_keep"`
	NoTrash          bool     `toml:"no_trash"`
	BWLimit          string   `toml:"bwlimit"` // rsync --bwlimit, e.g. "50M"
	// AllowWritableMain skips the read-only remount of the main drive.
	AllowWritableMain bool `toml:"allow_writable_main"`
	// NoRenameDetection turns off renaming moved/renamed files on the
	// replica; they are then copied again in full.
	NoRenameDetection bool `toml:"no_rename_detection"`
}

// StepConfig describes an extra step. Type selects the implementation
// ("shell" or a built-in step ID); the remaining fields are type-specific.
type StepConfig struct {
	Type        string            `toml:"type"`
	Title       string            `toml:"title"`
	Description string            `toml:"description"`
	Default     bool              `toml:"default"`
	Run         string            `toml:"run"`
	SkipIf      string            `toml:"skip_if"`
	Env         map[string]string `toml:"env"`
	Options     map[string]any    `toml:"options"`
}

// DefaultExcludes are never copied from main to replica.
var DefaultExcludes = []string{
	".Spotlight-V100", ".fseventsd", ".Trashes", ".TemporaryItems",
	".DocumentRevisions-V100", ".DS_Store", "._*",
}

func DefaultPath() string {
	if p := os.Getenv("BT_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".config", "btpair", "config.toml")
	// Before the rename the tool was called bt; keep reading its config.
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		old := filepath.Join(home, ".config", "bt", "config.toml")
		if _, err := os.Stat(old); err == nil {
			return old
		}
	}
	return p
}

// Load returns an empty config when the file does not exist.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if cfg.Pairs == nil {
		cfg.Pairs = map[string]PairConfig{}
	}
	if cfg.Steps == nil {
		cfg.Steps = map[string]StepConfig{}
	}
	return cfg, nil
}

// Pair returns the pair's settings with defaults filled in.
func (c *Config) Pair(id string) PairConfig {
	p := c.Pairs[id]
	if p.MaxDeletePercent == 0 {
		p.MaxDeletePercent = 25
	}
	if p.TrashKeep == 0 {
		p.TrashKeep = 5
	}
	return p
}

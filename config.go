package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Limit        int    `toml:"limit"`
	AutoFixCI    bool   `toml:"auto_fix_ci"`
	AutoFixLabel string `toml:"auto_fix_label"`
	WorkDir      string `toml:"work_dir"`
	CustomPrompt string `toml:"custom_prompt"`

	AllowedOwners []string `toml:"allowed_owners"`

	SourcePath string `toml:"-"`
}

const defaultAutoFixLabel = "claude-auto-fix"

func defaultConfigPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "renovate-auto-review", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "renovate-auto-review", "config.toml"), nil
}

func loadConfig(path string) (*Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &configNotFoundError{Path: path}
		}
		return nil, fmt.Errorf("decode toml %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		fmt.Fprintln(os.Stderr, serr.warn(fmt.Sprintf("warn: unknown keys in %s: %s", path, strings.Join(keys, ", "))))
	}
	cfg.SourcePath = path
	applyConfigDefaults(&cfg)
	return &cfg, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Limit <= 0 {
		cfg.Limit = 50
	}
	if cfg.AutoFixLabel == "" {
		cfg.AutoFixLabel = defaultAutoFixLabel
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = os.TempDir()
	}
}

type configNotFoundError struct{ Path string }

func (e *configNotFoundError) Error() string {
	return fmt.Sprintf("config not found: %s", e.Path)
}

const sampleConfig = `# renovate-auto-review config (TOML)
# Location: $XDG_CONFIG_HOME/renovate-auto-review/config.toml
#           (defaults to ~/.config/renovate-auto-review/config.toml)
#
# Target repository is specified on the command line, e.g.:
#   renovate-auto-review Cassin01/multi_agent_control_tower

# Value passed to "gh pr list --limit" (optional)
limit = 50

# Let Claude attempt to fix failing CI (default false).
# When true, every Renovate PR in the repo is in scope. For per-PR opt-in,
# leave this false and attach auto_fix_label to the PR instead.
auto_fix_ci = false

# Label name used for per-PR opt-in (default "claude-auto-fix")
# auto_fix_label = "claude-auto-fix"

# Parent directory used for temporary work dirs during CI auto-fix (default os.TempDir())
# work_dir = "/tmp"

# Optional: extra instruction appended to every Claude prompt as an
# "[Additional instructions]" section. Useful for overriding output language,
# tone, or adding project-specific constraints. Leave empty to disable.
# custom_prompt = "Write the PR comment body in Japanese."

# Domain guard: only operate on repositories owned by these GitHub users/orgs.
# Matched case-insensitively against the "owner" part of the "owner/name"
# argument. Empty or unset = guard disabled (any owner allowed).
# allowed_owners = ["Cassin01", "my-org"]
`

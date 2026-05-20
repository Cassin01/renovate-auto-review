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
	Limit int      `toml:"limit"`
	Repos []string `toml:"repos"`

	SourcePath string `toml:"-"`
}

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
		fmt.Fprintf(os.Stderr, "warn: unknown keys in %s: %s\n", path, strings.Join(keys, ", "))
	}
	cfg.SourcePath = path
	applyConfigDefaults(&cfg)
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Limit <= 0 {
		cfg.Limit = 50
	}
}

func validateConfig(cfg *Config) error {
	if len(cfg.Repos) == 0 {
		return fmt.Errorf("config %s: repos is empty", cfg.SourcePath)
	}
	for i, r := range cfg.Repos {
		if !strings.Contains(r, "/") {
			return fmt.Errorf("config %s: repos[%d] %q is not owner/name", cfg.SourcePath, i, r)
		}
	}
	return nil
}

type configNotFoundError struct{ Path string }

func (e *configNotFoundError) Error() string {
	return fmt.Sprintf("config not found: %s", e.Path)
}

const sampleConfig = `# renovate-auto-review config (TOML)
# 配置先: $XDG_CONFIG_HOME/renovate-auto-review/config.toml
#         (デフォルトは ~/.config/renovate-auto-review/config.toml)

# gh pr list --limit に渡す値 (省略可)
limit = 50

# 対象 GitHub リポジトリ (owner/name)
repos = [
  "Cassin01/multi_agent_control_tower",
]
`

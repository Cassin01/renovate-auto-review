package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApplyConfigDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "zero limit filled",
			in:   Config{Limit: 0, Repos: []string{"a/b"}},
			want: Config{Limit: 50, Repos: []string{"a/b"}},
		},
		{
			name: "negative limit filled",
			in:   Config{Limit: -1, Repos: []string{"a/b"}},
			want: Config{Limit: 50, Repos: []string{"a/b"}},
		},
		{
			name: "values preserved",
			in:   Config{Limit: 7, Repos: []string{"a/b"}},
			want: Config{Limit: 7, Repos: []string{"a/b"}},
		},
		{
			name: "empty filled",
			in:   Config{},
			want: Config{Limit: 50},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in
			applyConfigDefaults(&got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("applyConfigDefaults() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantErr   bool
		errSubstr []string
	}{
		{
			name: "valid",
			cfg:  Config{Repos: []string{"a/b", "c/d"}, SourcePath: "/p"},
		},
		{
			name:      "empty repos",
			cfg:       Config{Repos: nil, SourcePath: "/some/path.toml"},
			wantErr:   true,
			errSubstr: []string{"/some/path.toml", "repos is empty"},
		},
		{
			name:      "repo without slash",
			cfg:       Config{Repos: []string{"no-slash"}, SourcePath: "/p"},
			wantErr:   true,
			errSubstr: []string{"no-slash", "[0]", "owner/name"},
		},
		{
			name:      "second repo without slash",
			cfg:       Config{Repos: []string{"ok/repo", "bad"}, SourcePath: "/p"},
			wantErr:   true,
			errSubstr: []string{"bad", "[1]"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(&tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				msg := err.Error()
				for _, s := range tc.errSubstr {
					if !strings.Contains(msg, s) {
						t.Errorf("error %q missing substring %q", msg, s)
					}
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		path := filepath.Join("testdata", "valid.toml")
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Limit != 10 {
			t.Errorf("Limit = %d, want 10", cfg.Limit)
		}
		if !reflect.DeepEqual(cfg.Repos, []string{"owner/a", "owner/b"}) {
			t.Errorf("Repos = %v, want [owner/a owner/b]", cfg.Repos)
		}
		if cfg.SourcePath != path {
			t.Errorf("SourcePath = %q, want %q", cfg.SourcePath, path)
		}
	})

	t.Run("defaults applied", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "defaults.toml"))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Limit != 50 {
			t.Errorf("Limit = %d, want 50", cfg.Limit)
		}
	})

	t.Run("not found", func(t *testing.T) {
		path := filepath.Join("testdata", "does-not-exist.toml")
		_, err := loadConfig(path)
		if err == nil {
			t.Fatal("expected error")
		}
		nf, ok := errors.AsType[*configNotFoundError](err)
		if !ok {
			t.Fatalf("expected *configNotFoundError, got %T: %v", err, err)
		}
		if nf.Path != path {
			t.Errorf("Path = %q, want %q", nf.Path, path)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		_, err := loadConfig(filepath.Join("testdata", "malformed.toml"))
		if err == nil {
			t.Fatal("expected error")
		}
		if _, ok := errors.AsType[*configNotFoundError](err); ok {
			t.Errorf("malformed should not be configNotFoundError, got %v", err)
		}
	})

	t.Run("missing repos", func(t *testing.T) {
		_, err := loadConfig(filepath.Join("testdata", "missing-repos.toml"))
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "repos is empty") {
			t.Errorf("error %q missing 'repos is empty'", err)
		}
	})

	t.Run("bad repo format", func(t *testing.T) {
		_, err := loadConfig(filepath.Join("testdata", "bad-repo.toml"))
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "owner/name") {
			t.Errorf("error %q missing 'owner/name'", err)
		}
	})

	t.Run("unknown keys load successfully", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "unknown-keys.toml"))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Limit != 5 {
			t.Errorf("Limit = %d", cfg.Limit)
		}
	})
}

func TestDefaultConfigPath(t *testing.T) {
	t.Run("XDG_CONFIG_HOME set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		got, err := defaultConfigPath()
		if err != nil {
			t.Fatalf("defaultConfigPath: %v", err)
		}
		want := filepath.Join("/tmp/xdg", "renovate-auto-review", "config.toml")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME empty falls back to HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/tmp/home")
		got, err := defaultConfigPath()
		if err != nil {
			t.Fatalf("defaultConfigPath: %v", err)
		}
		want := filepath.Join("/tmp/home", ".config", "renovate-auto-review", "config.toml")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

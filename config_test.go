package main

import (
	"errors"
	"fmt"
	"os"
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
			in:   Config{Limit: 0},
			want: Config{Limit: 50, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "negative limit filled",
			in:   Config{Limit: -1},
			want: Config{Limit: 50, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "values preserved",
			in:   Config{Limit: 7},
			want: Config{Limit: 7, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "empty filled",
			in:   Config{},
			want: Config{Limit: 50, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "auto-fix label preserved",
			in:   Config{Limit: 10, AutoFixLabel: "custom-label"},
			want: Config{Limit: 10, AutoFixLabel: "custom-label", RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "work_dir preserved",
			in:   Config{Limit: 10, WorkDir: "/var/tmp"},
			want: Config{Limit: 10, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: "/var/tmp", RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "auto_fix_ci flag preserved",
			in:   Config{Limit: 10, AutoFixCI: true},
			want: Config{Limit: 10, AutoFixCI: true, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "rebase label preserved",
			in:   Config{Limit: 10, RebaseLabel: "needs-rebase"},
			want: Config{Limit: 10, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: "needs-rebase", WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "auto_rebase_conflicts flag preserved",
			in:   Config{Limit: 10, AutoRebaseConflicts: true},
			want: Config{Limit: 10, AutoRebaseConflicts: true, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
		},
		{
			name: "renovate_authors preserved",
			in:   Config{Limit: 10, RenovateAuthors: []string{"app/my-renovate"}},
			want: Config{Limit: 10, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{"app/my-renovate"}},
		},
		{
			name: "empty renovate_authors filled",
			in:   Config{Limit: 10, RenovateAuthors: []string{}},
			want: Config{Limit: 10, AutoFixLabel: defaultAutoFixLabel, RebaseLabel: defaultRebaseLabel, WorkDir: os.TempDir(), RenovateAuthors: []string{renovateLogin}},
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

func TestParseRepoArg(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		want      string
		wantErr   bool
		errSubstr string
	}{
		{name: "valid", args: []string{"owner/name"}, want: "owner/name"},
		{name: "missing", args: nil, wantErr: true, errSubstr: "missing target repository"},
		{name: "empty slice", args: []string{}, wantErr: true, errSubstr: "missing target repository"},
		{name: "no slash", args: []string{"no-slash"}, wantErr: true, errSubstr: "owner/name"},
		{name: "leading slash", args: []string{"/leading"}, wantErr: true, errSubstr: "owner/name"},
		{name: "trailing slash", args: []string{"trailing/"}, wantErr: true, errSubstr: "owner/name"},
		{name: "too many", args: []string{"a/b", "c/d"}, wantErr: true, errSubstr: "only one repository"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRepoArg(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
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

	t.Run("custom_prompt loaded", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "custom-prompt.toml"))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.CustomPrompt != "コメントは日本語を使って" {
			t.Errorf("CustomPrompt = %q, want %q", cfg.CustomPrompt, "コメントは日本語を使って")
		}
	})

	t.Run("renovate_authors loaded", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "renovate-authors.toml"))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		want := []string{"app/renovate", "app/my-renovate"}
		if !reflect.DeepEqual(cfg.RenovateAuthors, want) {
			t.Errorf("loadConfig: want RenovateAuthors %v, got %v", want, cfg.RenovateAuthors)
		}
	})

	t.Run("renovate_authors unset defaults to app/renovate", func(t *testing.T) {
		cfg, err := loadConfig(filepath.Join("testdata", "defaults.toml"))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if want := []string{renovateLogin}; !reflect.DeepEqual(cfg.RenovateAuthors, want) {
			t.Errorf("loadConfig: want RenovateAuthors %v, got %v", want, cfg.RenovateAuthors)
		}
	})

	t.Run("renovate_authors many entries", func(t *testing.T) {
		entries := make([]string, 1000)
		for i := range entries {
			entries[i] = fmt.Sprintf("%q", fmt.Sprintf("app/renovate-%d", i))
		}
		path := filepath.Join(t.TempDir(), "config.toml")
		body := "renovate_authors = [" + strings.Join(entries, ", ") + "]\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if len(cfg.RenovateAuthors) != 1000 {
			t.Errorf("loadConfig: want 1000 RenovateAuthors, got %d", len(cfg.RenovateAuthors))
		}
	})

	invalidAuthors := []struct {
		name  string
		entry string
	}{
		{name: "user login", entry: "renovate-bot"},
		{name: "empty slug", entry: "app/"},
		{name: "blank", entry: ""},
		{name: "app prefix with different case", entry: "App/renovate"},
	}
	for _, tc := range invalidAuthors {
		t.Run("renovate_authors invalid: "+tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			body := fmt.Sprintf("renovate_authors = [%q]\n", tc.entry)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(path)
			if err == nil {
				t.Fatalf("loadConfig: want error for %q, got nil", tc.entry)
			}
			if !strings.Contains(err.Error(), "renovate_authors") {
				t.Errorf("loadConfig: want error mentioning renovate_authors, got %v", err)
			}
		})
	}

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

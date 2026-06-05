package main

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestIsRenovatePR(t *testing.T) {
	cases := []struct {
		name string
		pr   PR
		want bool
	}{
		{
			name: "bot + renovate label",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: true,
		},
		{
			name: "bot + multiple labels including renovate",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: "deps"}, {Name: renovateLabel}, {Name: "security"}},
			},
			want: true,
		},
		{
			name: "bot but no renovate label",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: "other"}},
			},
			want: false,
		},
		{
			name: "renovate label but not bot",
			pr: PR{
				Author: Author{IsBot: false, Login: renovateLogin},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: false,
		},
		{
			name: "bot but wrong login",
			pr: PR{
				Author: Author{IsBot: true, Login: "app/dependabot"},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: false,
		},
		{
			name: "zero value",
			pr:   PR{},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRenovatePR(tc.pr); got != tc.want {
				t.Errorf("isRenovatePR() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldSkipReview(t *testing.T) {
	const marker = "<!-- m -->"
	t0 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	earlier := t0.Add(-1 * time.Hour)
	later := t0.Add(1 * time.Hour)

	cases := []struct {
		name     string
		comments []Comment
		commits  []Commit
		want     bool
	}{
		{
			name:     "no marker comment",
			comments: []Comment{{Body: "lgtm", CreatedAt: t0}},
			commits:  []Commit{{CommittedDate: t0}},
			want:     false,
		},
		{
			name:     "marker present, last commit older",
			comments: []Comment{{Body: marker + "\nbody", CreatedAt: t0}},
			commits:  []Commit{{CommittedDate: earlier}},
			want:     true,
		},
		{
			name:     "marker present, last commit newer",
			comments: []Comment{{Body: marker + "\nbody", CreatedAt: t0}},
			commits:  []Commit{{CommittedDate: later}},
			want:     false,
		},
		{
			name: "multiple marker comments use latest",
			comments: []Comment{
				{Body: marker + " old", CreatedAt: earlier},
				{Body: marker + " new", CreatedAt: later},
			},
			commits: []Commit{{CommittedDate: t0}},
			want:    true,
		},
		{
			name: "multiple marker comments, commit after latest",
			comments: []Comment{
				{Body: marker + " old", CreatedAt: earlier},
				{Body: marker + " mid", CreatedAt: t0},
			},
			commits: []Commit{{CommittedDate: later}},
			want:    false,
		},
		{
			name:     "marker and commit at same instant",
			comments: []Comment{{Body: marker, CreatedAt: t0}},
			commits:  []Commit{{CommittedDate: t0}},
			want:     true,
		},
		{
			name:     "no commits but marker present",
			comments: []Comment{{Body: marker, CreatedAt: t0}},
			commits:  nil,
			want:     true,
		},
		{
			name: "multiple commits, latest used",
			comments: []Comment{
				{Body: marker, CreatedAt: t0},
			},
			commits: []Commit{
				{CommittedDate: earlier},
				{CommittedDate: later},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSkipReview(tc.comments, tc.commits, marker); got != tc.want {
				t.Errorf("shouldSkipReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasFailingChecks(t *testing.T) {
	cases := []struct {
		name   string
		checks []CheckRun
		want   bool
	}{
		{"empty", nil, false},
		{
			name: "all success",
			checks: []CheckRun{
				{Name: "lint", Conclusion: "SUCCESS", Status: "COMPLETED"},
				{Name: "test", Conclusion: "SUCCESS", Status: "COMPLETED"},
			},
			want: false,
		},
		{
			name: "one failure",
			checks: []CheckRun{
				{Name: "lint", Conclusion: "FAILURE", Status: "COMPLETED"},
				{Name: "test", Conclusion: "SUCCESS", Status: "COMPLETED"},
			},
			want: true,
		},
		{
			name: "cancelled counts as failure",
			checks: []CheckRun{
				{Name: "test", Conclusion: "CANCELLED", Status: "COMPLETED"},
			},
			want: true,
		},
		{
			name: "in_progress ignored",
			checks: []CheckRun{
				{Name: "test", Conclusion: "", Status: "IN_PROGRESS"},
			},
			want: false,
		},
		{
			name: "mixed in_progress and failure",
			checks: []CheckRun{
				{Name: "lint", Conclusion: "", Status: "IN_PROGRESS"},
				{Name: "test", Conclusion: "FAILURE", Status: "COMPLETED"},
			},
			want: true,
		},
		{
			name: "lowercase conclusion handled",
			checks: []CheckRun{
				{Name: "x", Conclusion: "failure", Status: "COMPLETED"},
			},
			want: true,
		},
		{
			name: "neutral not counted",
			checks: []CheckRun{
				{Name: "x", Conclusion: "NEUTRAL", Status: "COMPLETED"},
			},
			want: false,
		},
		{
			name: "status empty (StatusContext) failure",
			checks: []CheckRun{
				{Name: "renovate/artifacts", Conclusion: "FAILURE", Status: ""},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasFailingChecks(tc.checks); got != tc.want {
				t.Errorf("hasFailingChecks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPickFailedRunIDs(t *testing.T) {
	cases := []struct {
		name   string
		checks []CheckRun
		want   []string
	}{
		{"empty", nil, nil},
		{
			name: "extract single",
			checks: []CheckRun{
				{
					Conclusion: "FAILURE", Status: "COMPLETED",
					DetailsURL: "https://github.com/o/r/actions/runs/12345/job/678",
				},
			},
			want: []string{"12345"},
		},
		{
			name: "dedupe same run multiple jobs",
			checks: []CheckRun{
				{Conclusion: "FAILURE", Status: "COMPLETED", DetailsURL: "https://github.com/o/r/actions/runs/100/job/1"},
				{Conclusion: "FAILURE", Status: "COMPLETED", DetailsURL: "https://github.com/o/r/actions/runs/100/job/2"},
				{Conclusion: "FAILURE", Status: "COMPLETED", DetailsURL: "https://github.com/o/r/actions/runs/200/job/3"},
			},
			want: []string{"100", "200"},
		},
		{
			name: "ignore success",
			checks: []CheckRun{
				{Conclusion: "SUCCESS", Status: "COMPLETED", DetailsURL: "https://github.com/o/r/actions/runs/999/job/1"},
				{Conclusion: "FAILURE", Status: "COMPLETED", DetailsURL: "https://github.com/o/r/actions/runs/111/job/2"},
			},
			want: []string{"111"},
		},
		{
			name: "ignore non-actions URL (renovate/artifacts)",
			checks: []CheckRun{
				{Conclusion: "FAILURE", Status: "", DetailsURL: ""},
			},
			want: nil,
		},
		{
			name: "ignore in_progress",
			checks: []CheckRun{
				{Conclusion: "", Status: "IN_PROGRESS", DetailsURL: "https://github.com/o/r/actions/runs/777/job/1"},
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickFailedRunIDs(tc.checks)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pickFailedRunIDs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsGenuineCIFailure(t *testing.T) {
	cases := []struct {
		conclusion string
		want       bool
	}{
		{"FAILURE", true},
		{"failure", true},
		{"TIMED_OUT", true},
		{"STARTUP_FAILURE", true},
		{"ACTION_REQUIRED", true},
		{"CANCELLED", false},
		{"SUCCESS", false},
		{"SKIPPED", false},
		{"NEUTRAL", false},
		{"STALE", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.conclusion, func(t *testing.T) {
			if got := isGenuineCIFailure(tc.conclusion); got != tc.want {
				t.Errorf("isGenuineCIFailure(%q): want %v, got %v", tc.conclusion, tc.want, got)
			}
		})
	}
}

func TestHasAutoFixLabel(t *testing.T) {
	cases := []struct {
		name  string
		pr    PR
		label string
		want  bool
	}{
		{
			name:  "match",
			pr:    PR{Labels: []Label{{Name: "renovate"}, {Name: "claude-auto-fix"}}},
			label: "claude-auto-fix",
			want:  true,
		},
		{
			name:  "no labels",
			pr:    PR{},
			label: "claude-auto-fix",
			want:  false,
		},
		{
			name:  "different label",
			pr:    PR{Labels: []Label{{Name: "renovate"}}},
			label: "claude-auto-fix",
			want:  false,
		},
		{
			name:  "case sensitive",
			pr:    PR{Labels: []Label{{Name: "Claude-Auto-Fix"}}},
			label: "claude-auto-fix",
			want:  false,
		},
		{
			name:  "empty label opts out",
			pr:    PR{Labels: []Label{{Name: ""}}},
			label: "",
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAutoFixLabel(tc.pr, tc.label); got != tc.want {
				t.Errorf("hasAutoFixLabel() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTailBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "hello", 100, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "0123456789", 4, "... (truncated) ...\n6789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailBytes(tc.in, tc.max); got != tc.want {
				t.Errorf("tailBytes() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodePRFiles(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "go renovate PR",
			raw:  `{"files":[{"path":"go.mod","additions":1,"deletions":1},{"path":"go.sum","additions":2,"deletions":2}]}`,
			want: []string{"go.mod", "go.sum"},
		},
		{
			name: "empty files",
			raw:  `{"files":[]}`,
			want: []string{},
		},
		{
			name: "subdirectory paths preserved",
			raw:  `{"files":[{"path":"api/go.mod"},{"path":"api/go.sum"}]}`,
			want: []string{"api/go.mod", "api/go.sum"},
		},
		{
			name:    "malformed json",
			raw:     `not json`,
			wantErr: true,
		},
		{
			name: "missing files key returns empty",
			raw:  `{}`,
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodePRFiles([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodePRFiles: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decodePRFiles() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStderrOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"generic error", errors.New("boom"), ""},
		{"exit error with stderr", &exec.ExitError{Stderr: []byte("  oops\n")}, "oops"},
		{"exit error with empty stderr", &exec.ExitError{Stderr: nil}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stderrOf(tc.err); got != tc.want {
				t.Errorf("stderrOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPartialFailedLog(t *testing.T) {
	cases := []struct {
		name   string
		stdout []byte
		err    error
		want   bool
	}{
		{
			name:   "log not found with usable stdout",
			stdout: []byte("unit_test\tstep\tboom\n"),
			err:    &exec.ExitError{Stderr: []byte("log not found: 77104140470\n")},
			want:   true,
		},
		{
			name:   "log not found but no stdout",
			stdout: nil,
			err:    &exec.ExitError{Stderr: []byte("log not found: 77104730178\n")},
			want:   false,
		},
		{
			name:   "log not found but stdout is whitespace only",
			stdout: []byte("  \n\t"),
			err:    &exec.ExitError{Stderr: []byte("log not found: 1\n")},
			want:   false,
		},
		{
			name:   "unrelated error with stdout is not salvaged",
			stdout: []byte("partial\n"),
			err:    &exec.ExitError{Stderr: []byte("HTTP 404: Not Found\n")},
			want:   false,
		},
		{
			name:   "nil error",
			stdout: []byte("logs\n"),
			err:    nil,
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := partialFailedLog(tc.stdout, tc.err); got != tc.want {
				t.Errorf("partialFailedLog(): want %v, got %v", tc.want, got)
			}
		})
	}
}

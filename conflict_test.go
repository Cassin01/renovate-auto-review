package main

import (
	"strings"
	"testing"
	"time"
)

func TestShouldRequestRebase(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rebaseComment := []Comment{{Body: rebaseRequestMarker + " requested rebase", CreatedAt: t0}}
	reviewComment := []Comment{{Body: defaultMarker + "\n## Claude auto-fix", CreatedAt: t0}}

	cases := []struct {
		name     string
		cfg      Config
		pr       PR
		comments []Comment
		commits  []Commit
		want     bool
	}{
		{
			name: "normal: conflicting, enabled, no label, no marker",
			cfg:  Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: "CONFLICTING"},
			want: true,
		},
		{
			name: "disabled",
			cfg:  Config{AutoRebaseConflicts: false, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: "CONFLICTING"},
			want: false,
		},
		{
			name: "mergeable",
			cfg:  Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: "MERGEABLE"},
			want: false,
		},
		{
			name: "unknown",
			cfg:  Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: "UNKNOWN"},
			want: false,
		},
		{
			name: "empty mergeable",
			cfg:  Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: ""},
			want: false,
		},
		{
			name: "rebase label already present",
			cfg:  Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:   PR{Mergeable: "CONFLICTING", Labels: []Label{{Name: "rebase"}}},
			want: false,
		},
		{
			name:     "rebase request marker present, no new commits since",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: rebaseComment,
			commits:  []Commit{{CommittedDate: t0.Add(-time.Hour)}},
			want:     false,
		},
		{
			name:     "rebase request marker present, newer commit",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: rebaseComment,
			commits:  []Commit{{CommittedDate: t0.Add(time.Hour)}},
			want:     true,
		},
		{
			// Boundary: a commit at the same instant as the marker is not
			// "since", so the request stays suppressed.
			name:     "rebase request marker present, commit at the same instant",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: rebaseComment,
			commits:  []Commit{{CommittedDate: t0}},
			want:     false,
		},
		{
			// The review/auto-fix marker must not gate the rebase request: a
			// conflict comes from the base branch moving, so requiring a newer
			// PR commit would suppress the request indefinitely.
			name:     "review marker present, no new commits since",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: reviewComment,
			commits:  []Commit{{CommittedDate: t0.Add(-time.Second)}},
			want:     true,
		},
		{
			name:     "rebase request marker present, no commits at all",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: rebaseComment,
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRequestRebase(tc.pr, &tc.cfg, tc.comments, tc.commits)
			if got != tc.want {
				t.Errorf("shouldRequestRebase: want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestBuildRebaseRequestCommentBody(t *testing.T) {
	body := buildRebaseRequestCommentBody("rebase")

	firstLine, _, _ := strings.Cut(body, "\n")
	if !strings.Contains(firstLine, rebaseRequestMarker) {
		t.Errorf("buildRebaseRequestCommentBody: first line want marker %q, got %q", rebaseRequestMarker, firstLine)
	}
	if strings.Contains(body, defaultMarker) {
		t.Errorf("buildRebaseRequestCommentBody: want no review marker %q in body, got %q", defaultMarker, body)
	}
	if !strings.Contains(body, "rebase") {
		t.Errorf("buildRebaseRequestCommentBody: want label name %q in body, got %q", "rebase", body)
	}
	if !strings.HasSuffix(body, commentFooter) {
		t.Errorf("buildRebaseRequestCommentBody: want footer suffix, got %q", body)
	}
}

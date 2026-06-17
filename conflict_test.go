package main

import (
	"strings"
	"testing"
	"time"
)

func TestShouldRequestRebase(t *testing.T) {
	const marker = "<!-- rar -->"
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	markerComment := []Comment{{Body: marker + " requested rebase", CreatedAt: t0}}

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
			name:     "marker present, no new commits since",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: markerComment,
			commits:  []Commit{{CommittedDate: t0.Add(-time.Hour)}},
			want:     false,
		},
		{
			name:     "marker present, newer commit",
			cfg:      Config{AutoRebaseConflicts: true, RebaseLabel: "rebase"},
			pr:       PR{Mergeable: "CONFLICTING"},
			comments: markerComment,
			commits:  []Commit{{CommittedDate: t0.Add(time.Hour)}},
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRequestRebase(tc.pr, &tc.cfg, tc.comments, tc.commits, marker)
			if got != tc.want {
				t.Errorf("shouldRequestRebase: want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestBuildRebaseRequestCommentBody(t *testing.T) {
	const marker = "<!-- rar -->"
	body := buildRebaseRequestCommentBody(marker, "rebase")

	firstLine, _, _ := strings.Cut(body, "\n")
	if !strings.Contains(firstLine, marker) {
		t.Errorf("buildRebaseRequestCommentBody: first line want marker %q, got %q", marker, firstLine)
	}
	if !strings.Contains(body, "rebase") {
		t.Errorf("buildRebaseRequestCommentBody: want label name %q in body, got %q", "rebase", body)
	}
	if !strings.HasSuffix(body, commentFooter) {
		t.Errorf("buildRebaseRequestCommentBody: want footer suffix, got %q", body)
	}
}

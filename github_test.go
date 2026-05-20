package main

import (
	"errors"
	"os/exec"
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

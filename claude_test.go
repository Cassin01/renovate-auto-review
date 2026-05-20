package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		prNumber int
		marker   string
	}{
		{"plain", "owner/repo", 1, "<!-- m -->"},
		{"slash repo", "Cassin01/foo", 42, "<!-- claude-auto-mergeable-check -->"},
		{"large pr number", "org/name", 12345, "<!-- x -->"},
		{"custom marker", "a/b", 7, "MARK"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPrompt(tc.repo, tc.prNumber, tc.marker)

			if c := strings.Count(got, tc.repo); c < 4 {
				t.Errorf("repo %q should appear at least 4 times, got %d", tc.repo, c)
			}
			numStr := strconv.Itoa(tc.prNumber)
			if c := strings.Count(got, numStr); c < 4 {
				t.Errorf("prNumber %q should appear at least 4 times, got %d", numStr, c)
			}
			if c := strings.Count(got, tc.marker); c < 2 {
				t.Errorf("marker %q should appear at least 2 times, got %d", tc.marker, c)
			}

			for _, want := range []string{
				"experienced reviewer",
				"POSTED",
				"Claude auto review",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("expected substring %q not found", want)
				}
			}

			for _, bad := range []string{"%!s(MISSING)", "%!d(MISSING)", "%!(EXTRA"} {
				if strings.Contains(got, bad) {
					t.Errorf("unexpected format error marker %q present", bad)
				}
			}
		})
	}
}

func TestBuildFixPrompt(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		prNumber int
		headRef  string
		marker   string
		workDir  string
		logs     string
	}{
		{
			name: "typical", repo: "owner/repo", prNumber: 42,
			headRef: "renovate/foo", marker: "<!-- m -->",
			workDir: "/tmp/rar-42-abc", logs: "error: something broke",
		},
		{
			name: "empty logs", repo: "o/r", prNumber: 1,
			headRef: "renovate/bar", marker: "<!-- claude-auto-mergeable-check -->",
			workDir: "/tmp/x", logs: "",
		},
		{
			name: "large pr number", repo: "x/y", prNumber: 99999,
			headRef: "renovate/baz", marker: "M",
			workDir: "/var/tmp/rar", logs: strings.Repeat("a", 1000),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildFixPrompt(tc.repo, tc.prNumber, tc.headRef, tc.marker, tc.workDir, tc.logs)

			for _, want := range []string{
				tc.repo, tc.headRef, tc.marker, tc.workDir,
				strconv.Itoa(tc.prNumber),
				"Hard rules",
				"amend",
				"force",
				"rebase checkbox",
				"Fallback",
				"PUSHED",
				"POSTED",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("expected substring %q not found", want)
				}
			}

			if tc.logs != "" && !strings.Contains(got, tc.logs) {
				t.Errorf("expected logs to be embedded")
			}

			for _, bad := range []string{"%!s(MISSING)", "%!d(MISSING)", "%!(EXTRA"} {
				if strings.Contains(got, bad) {
					t.Errorf("unexpected format error marker %q present", bad)
				}
			}
		})
	}
}

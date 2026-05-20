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
				"あなたは熟練のレビュアーです",
				"POSTED",
				"Claude 自動レビュー",
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

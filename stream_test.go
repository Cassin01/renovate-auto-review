package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderClaudeStream(t *testing.T) {
	cases := []struct {
		name               string
		file               string
		wantResultContains string
		wantOut            []string
		notWantOut         []string
	}{
		{
			name:               "review posted",
			file:               "claude_stream_posted.jsonl",
			wantResultContains: "POSTED",
			wantOut: []string{
				"Reviewing PR #42.",
				"$ gh pr view 42 --repo o/r",
				"← title: bump foo to 1.2.3",
				"$ gh pr diff 42 --repo o/r",
				"… (", // long diff tool_result is truncated
				"$ gh pr comment 42 --repo o/r --body-file -",
				"Comment posted.",
			},
			notWantOut: []string{
				"sess-abc",   // system/init suppressed
				"session_id", // raw JSON not echoed
				"rate_limit", // noise event suppressed
				"MERGEABLE",  // truncated tail of first tool_result dropped
			},
		},
		{
			name:               "fix pushed",
			file:               "claude_stream_pushed.jsonl",
			wantResultContains: "PUSHED",
			wantOut: []string{
				"Fixing CI for PR #7.",
				"$ go test ./...",
				"← ok  example  0.12s",
				"$ git push origin HEAD:renovate/foo",
			},
			notWantOut: []string{"sess-xyz"},
		},
		{
			name:               "error result",
			file:               "claude_stream_error.jsonl",
			wantResultContains: "execution failed",
			wantOut:            []string{"Attempting the review."},
			notWantOut:         []string{"POSTED", "PUSHED"},
		},
		{
			name:               "noise is skipped",
			file:               "claude_stream_noise.jsonl",
			wantResultContains: "POSTED",
			wantOut:            []string{"Visible review line."},
			notWantOut: []string{
				"SHOULD_NOT_APPEAR",      // system/init
				"PARTIAL_DELTA",          // stream_event delta
				"this line is not valid", // non-JSON line
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			var out bytes.Buffer
			got, err := renderClaudeStream(bytes.NewReader(data), &out)
			if err != nil {
				t.Fatalf("renderClaudeStream: unexpected error: %v", err)
			}

			if !strings.Contains(got, tc.wantResultContains) {
				t.Errorf("renderClaudeStream: result want contains %q, got %q", tc.wantResultContains, got)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("renderClaudeStream: out want contains %q, got:\n%s", w, out.String())
				}
			}
			for _, nw := range tc.notWantOut {
				if strings.Contains(out.String(), nw) {
					t.Errorf("renderClaudeStream: out should not contain %q, got:\n%s", nw, out.String())
				}
			}
		})
	}
}

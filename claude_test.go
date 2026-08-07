package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		prNumber int
	}{
		{"plain", "owner/repo", 1},
		{"slash repo", "Cassin01/foo", 42},
		{"large pr number", "org/name", 12345},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPrompt(tc.repo, tc.prNumber, "")

			if c := strings.Count(got, tc.repo); c < 3 {
				t.Errorf("repo %q should appear at least 3 times, got %d", tc.repo, c)
			}
			numStr := strconv.Itoa(tc.prNumber)
			if c := strings.Count(got, numStr); c < 3 {
				t.Errorf("prNumber %q should appear at least 3 times, got %d", numStr, c)
			}

			for _, want := range []string{
				"experienced reviewer",
				"REVIEW_DONE",
				"Claude auto review",
				reviewCommentFile,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("expected substring %q not found", want)
				}
			}

			// Posting (and marker/footer decoration) is done by the calling
			// tool; the prompt must not instruct claude to do either.
			for _, bad := range []string{
				"POSTED",
				"gh pr comment",
				"github.com/Cassin01/renovate-auto-review",
				"%!s(MISSING)", "%!d(MISSING)", "%!(EXTRA",
			} {
				if strings.Contains(got, bad) {
					t.Errorf("unexpected substring %q present", bad)
				}
			}

			if strings.Contains(got, "[Additional instructions]") {
				t.Errorf("empty customPrompt should not emit [Additional instructions] section")
			}
		})
	}
}

func TestBuildPromptWithCustomPrompt(t *testing.T) {
	cases := []struct {
		name   string
		custom string
	}{
		{"japanese", "コメントは日本語を使って"},
		{"multiline", "rule one\nrule two\nrule three"},
		{"ascii", "be terse"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPrompt("owner/repo", 7, tc.custom)

			if c := strings.Count(got, "[Additional instructions]"); c != 1 {
				t.Errorf("section header should appear once, got %d", c)
			}
			if !strings.Contains(got, tc.custom) {
				t.Errorf("custom prompt %q not embedded verbatim", tc.custom)
			}
			for _, want := range []string{"Important:", "REVIEW_DONE", "Claude auto review"} {
				if !strings.Contains(got, want) {
					t.Errorf("invariant substring %q missing", want)
				}
			}
		})
	}
}

func TestBuildReviewCommentBody(t *testing.T) {
	const marker = "<!-- claude-auto-mergeable-check -->"
	cases := []struct {
		name   string
		review string
	}{
		{"normal", "## Claude auto review\n- **Merge verdict**: ✅ safe"},
		{"surrounding whitespace", "\n\n  ## Claude auto review\nbody  \n\n"},
		{"extreme large", strings.Repeat("x", 1<<20)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildReviewCommentBody(marker, tc.review)

			if !strings.HasPrefix(got, marker+"\n") {
				t.Errorf("buildReviewCommentBody: marker must be the first line, got prefix %q", got[:min(len(got), 60)])
			}
			if !strings.HasSuffix(got, commentFooter) {
				t.Errorf("buildReviewCommentBody: footer must be the verbatim suffix")
			}
			if !strings.Contains(got, strings.TrimSpace(tc.review)) {
				t.Errorf("buildReviewCommentBody: review content missing from body")
			}
		})
	}
}

func TestPublishReviewComment(t *testing.T) {
	const marker = "<!-- claude-auto-mergeable-check -->"

	writeReview := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, reviewCommentFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("posts marker+review+footer via gh pr comment", func(t *testing.T) {
		dir := t.TempDir()
		writeReview(t, dir, "## Claude auto review\n- **Merge verdict**: ✅ safe\n")

		var posted string
		fr := &fakeRunner{respond: func(call runnerCall) ([]byte, error) {
			if argsMatch(call, "gh", "pr", "comment") {
				data, err := os.ReadFile(call.args[len(call.args)-1])
				if err != nil {
					t.Fatalf("read body file: %v", err)
				}
				posted = string(data)
			}
			return nil, nil
		}}

		if err := publishReviewComment(fr.run, dir, "owner/repo", 42, marker); err != nil {
			t.Fatalf("publishReviewComment: unexpected error: %v", err)
		}
		if len(fr.calls) != 1 {
			t.Fatalf("publishReviewComment: want exactly 1 runner call, got %d", len(fr.calls))
		}
		call := fr.calls[0]
		if !argsMatch(call, "gh", "pr", "comment", "42", "--repo", "owner/repo", "--body-file") {
			t.Errorf("publishReviewComment: unexpected gh invocation: %s", call)
		}
		if !strings.HasPrefix(posted, marker+"\n") {
			t.Errorf("posted body must start with marker line, got prefix %q", posted[:min(len(posted), 60)])
		}
		if !strings.HasSuffix(posted, commentFooter) {
			t.Errorf("posted body must end with the verbatim footer")
		}
		if !strings.Contains(posted, "✅ safe") {
			t.Errorf("posted body must contain the review content")
		}
	})

	t.Run("missing review file errors without posting", func(t *testing.T) {
		fr := &fakeRunner{}
		if err := publishReviewComment(fr.run, t.TempDir(), "owner/repo", 1, marker); err == nil {
			t.Errorf("publishReviewComment: want error for missing review file")
		}
		if len(fr.calls) != 0 {
			t.Errorf("publishReviewComment: want no runner calls, got %d", len(fr.calls))
		}
	})

	t.Run("empty review file errors without posting", func(t *testing.T) {
		for _, content := range []string{"", "   \n\t\n"} {
			dir := t.TempDir()
			writeReview(t, dir, content)
			fr := &fakeRunner{}
			if err := publishReviewComment(fr.run, dir, "owner/repo", 1, marker); err == nil {
				t.Errorf("publishReviewComment(%q): want error for empty review", content)
			}
			if len(fr.calls) != 0 {
				t.Errorf("publishReviewComment(%q): want no runner calls, got %d", content, len(fr.calls))
			}
		}
	})

	t.Run("gh failure propagates", func(t *testing.T) {
		dir := t.TempDir()
		writeReview(t, dir, "review body")
		fr := &fakeRunner{respond: func(runnerCall) ([]byte, error) {
			return []byte("boom"), errors.New("exit 1")
		}}
		if err := publishReviewComment(fr.run, dir, "owner/repo", 1, marker); err == nil {
			t.Errorf("publishReviewComment: want error when gh pr comment fails")
		}
	})

	t.Run("extreme large review posts intact", func(t *testing.T) {
		dir := t.TempDir()
		large := strings.Repeat("y", 1<<20)
		writeReview(t, dir, large)

		var posted string
		fr := &fakeRunner{respond: func(call runnerCall) ([]byte, error) {
			data, err := os.ReadFile(call.args[len(call.args)-1])
			if err != nil {
				t.Fatalf("read body file: %v", err)
			}
			posted = string(data)
			return nil, nil
		}}
		if err := publishReviewComment(fr.run, dir, "owner/repo", 1, marker); err != nil {
			t.Fatalf("publishReviewComment: unexpected error: %v", err)
		}
		if !strings.Contains(posted, large) {
			t.Errorf("posted body must contain the full large review")
		}
	})
}

func TestBuildVerifyPrompt(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		prNumber int
		workDir  string
		logs     string
	}{
		{name: "typical", repo: "owner/repo", prNumber: 42, workDir: "/tmp/rar-42-abc", logs: "error: something broke"},
		{name: "empty logs", repo: "o/r", prNumber: 1, workDir: "/tmp/x", logs: ""},
		{name: "large pr number", repo: "x/y", prNumber: 99999, workDir: "/var/tmp/rar", logs: strings.Repeat("a", 1000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVerifyPrompt(tc.repo, tc.prNumber, tc.workDir, tc.logs, "")

			for _, want := range []string{
				tc.repo, tc.workDir,
				strconv.Itoa(tc.prNumber),
				"Hard rules",
				"amend",
				"force",
				"Fallback",
				".github/workflows", "package.json", "Makefile",
				"VERIFY_DONE",
				verifyResultFile,
				".npmrc",
				"${NODE_AUTH_TOKEN}",
				"performed by the calling tool",
				"every step",
				"formatter",
				"verified_commands",
				"re-runs each independently",
				"go mod download",
				"prunes unrelated unused modules",
				"one-shot headless session",
				"Do NOT run any command in the background",
				"foreground with an explicit timeout",
				"scoped to the packages affected by this PR",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("expected substring %q not found", want)
				}
			}

			// The verify subprocess must not be told to publish, and the old
			// push/post protocol must be gone.
			for _, bad := range []string{"PUSHED", "POSTED", "go build ./...", "go test ./..."} {
				if strings.Contains(got, bad) {
					t.Errorf("unexpected substring %q present", bad)
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

			if strings.Contains(got, "[Additional instructions]") {
				t.Errorf("empty customPrompt should not emit [Additional instructions] section")
			}
		})
	}
}

func TestBuildVerifyPromptWithCustomPrompt(t *testing.T) {
	cases := []struct {
		name   string
		custom string
	}{
		{"japanese", "コメントは日本語を使って"},
		{"multiline", "do X\ndo Y"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVerifyPrompt("owner/repo", 1, "/tmp/w", "log excerpt", tc.custom)

			if c := strings.Count(got, "[Additional instructions]"); c != 1 {
				t.Errorf("section header should appear once, got %d", c)
			}
			if !strings.Contains(got, tc.custom) {
				t.Errorf("custom prompt %q not embedded verbatim", tc.custom)
			}
			for _, want := range []string{"[Output protocol]", "VERIFY_DONE", "Hard rules"} {
				if !strings.Contains(got, want) {
					t.Errorf("invariant substring %q missing", want)
				}
			}
		})
	}
}

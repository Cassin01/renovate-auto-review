package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const commentFooter = "\n---\n👷 Generated with [renovate-auto-review](https://github.com/Cassin01/renovate-auto-review)\n"

// reviewCommentFile is the file the review subprocess writes its comment body
// to; the tool reads it, adds the marker and footer, and posts it.
const reviewCommentFile = "rar-review-comment.md"

const promptTemplate = `Target: %s PR #%d (Renovate auto-update)

You are an experienced reviewer. Follow the steps below to analyze the PR and write a review comment body to a file at the end. Do NOT post the comment yourself — the calling tool posts it after adding a duplicate-detection marker and a footer.

1. Run ` + "`gh pr view %d --repo %s`" + ` to get the PR overview and status
2. Run ` + "`gh pr diff %d --repo %s`" + ` to review the diff
3. Run ` + "`gh pr checks %d --repo %s`" + ` to check CI results
4. Decide whether the PR is safe to merge based on the above, and generate an English comment using the following template:

## Claude auto review
- **Merge verdict**: (one of: ✅ safe / ⚠️ needs review / ❌ do not merge)
- **Reason**: (2-4 lines of rationale)
- **CI status**: (list PASS/FAIL/PENDING)
- **Notes**: (only if any; otherwise write "none")

5. Write the comment body to ` + "`" + reviewCommentFile + "`" + ` in the working directory using the Write tool

%sImportant:
- The body MUST start with the ` + "`## Claude auto review`" + ` heading; do not add any marker or footer yourself — the calling tool adds them
- When uncertain, choose ⚠️ needs review; avoid asserting "safe" without confidence
- After writing the file, print only REVIEW_DONE on the final output line
- No other explanatory output is needed
`

func buildPrompt(repo string, prNumber int, customPrompt string) string {
	return fmt.Sprintf(promptTemplate,
		repo, prNumber,
		prNumber, repo,
		prNumber, repo,
		prNumber, repo,
		additionalInstructionsBlock(customPrompt),
	)
}

func additionalInstructionsBlock(s string) string {
	if s == "" {
		return ""
	}
	return "[Additional instructions]\n" + s + "\n\n"
}

// runClaudeStreaming starts cmd, renders its stream-json stdout to out as a
// human-readable progress trace, and returns the detection text (final result
// or concatenated assistant text). stdout is consumed fully before Wait;
// stderr is buffered and flushed afterwards so it never races with out.
func runClaudeStreaming(cmd *exec.Cmd, out io.Writer, red *redactor) (string, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	resultText, renderErr := renderClaudeStream(stdout, out, red)
	waitErr := cmd.Wait()
	if stderr.Len() > 0 {
		for line := range strings.SplitSeq(strings.TrimRight(stderr.String(), "\n"), "\n") {
			fmt.Fprintln(out, red.line(line))
		}
	}
	if waitErr != nil {
		return resultText, waitErr
	}
	return resultText, renderErr
}

func runClaudeAutoMode(claudeBin, repo string, prNumber int, marker, customPrompt, workDirBase string, out io.Writer) error {
	workDir, err := os.MkdirTemp(workDirBase, fmt.Sprintf("rar-review-%d-", prNumber))
	if err != nil {
		return fmt.Errorf("mkdir workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	prompt := buildPrompt(repo, prNumber, customPrompt)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools",
		"Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) Write",
		"-p", prompt,
	)
	cmd.Dir = workDir
	resultText, err := runClaudeStreaming(cmd, out, newRedactor())
	if err != nil {
		return fmt.Errorf("claude exec failed for PR #%d: %w", prNumber, err)
	}
	if !strings.Contains(resultText, "REVIEW_DONE") {
		return fmt.Errorf("claude did not report REVIEW_DONE for PR #%d", prNumber)
	}
	return publishReviewComment(defaultCommandRunner, workDir, repo, prNumber, marker)
}

// publishReviewComment reads the review body written by the claude subprocess,
// mechanically prepends the duplicate-detection marker and appends the footer,
// and posts the result. Marker and footer never depend on prompt compliance.
func publishReviewComment(run commandRunner, workDir, repo string, prNumber int, marker string) error {
	data, err := os.ReadFile(filepath.Join(workDir, reviewCommentFile))
	if err != nil {
		return fmt.Errorf("read review comment: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("review comment file %s is empty", reviewCommentFile)
	}
	body := buildReviewCommentBody(marker, string(data))
	if err := postCommentViaFile(run, workDir, repo, prNumber, body); err != nil {
		return fmt.Errorf("post review comment: %w", err)
	}
	return nil
}

func buildReviewCommentBody(marker, review string) string {
	return marker + "\n" + strings.TrimSpace(review) + "\n" + commentFooter
}

// verifyResultFile is the JSON contract file the verify subprocess writes into
// the working directory; the tool reads it to decide whether to publish.
const verifyResultFile = "rar-verify-result.json"

const verifyPromptTemplate = `Target: %s PR #%d (Renovate auto-update). The PR branch is already checked out in the working directory. Reproduce the failing CI locally, apply a minimal fix, and verify it. Do NOT commit, push, or comment — the calling tool does that.

[Hard rules — violating any aborts the task]
- Working directory is fixed to ` + "`%s`" + `. Do not ` + "`cd`" + ` outside it
- Do NOT run ` + "`git add`" + `, ` + "`git commit`" + `, ` + "`git push`" + `, or ` + "`gh pr comment`" + ` — these are disabled and performed by the calling tool
- Do not use ` + "`git commit --amend`" + ` / ` + "`git rebase`" + ` / ` + "`git reset --hard`" + ` / ` + "`git push --force`" + ` / ` + "`--force-with-lease`" + `
- Do not use ` + "`gh pr edit`" + `
- This is a one-shot headless session: ending your turn while a command is still running terminates the session immediately and discards the fix. Do NOT run any command in the background and do NOT end your turn to wait for one — run long commands in the foreground with an explicit timeout (up to 10 minutes) and wait for them to finish
- Do not attempt major breaking changes to dependencies (only minor type fixes, test adjustments, lockfile regeneration)
- Keep manifest/lockfile edits minimal. For Go, add only the ` + "`go.sum`" + `/` + "`go.mod`" + ` entries the build needs (e.g. ` + "`go mod download <module>`" + ` or ` + "`go get <pkg>@<version>`" + `); do NOT run a repo-wide ` + "`go mod tidy`" + ` that prunes unrelated unused modules — pruning collides with concurrent dependency PRs and causes merge conflicts. Touch only the require/checksum lines for the dependency this PR is about

[Secret handling — mandatory]
- A private registry token may be in the environment (e.g. ` + "`NODE_AUTH_TOKEN`" + ` / ` + "`NPM_TOKEN`" + ` / ` + "`NPM_CONFIG_*`" + `). It is already provided for installs — do not configure auth yourself
- Never ` + "`cat`" + `, echo, or print the contents of ` + "`.npmrc`" + `, ` + "`~/.npmrc`" + `, or any environment variable value
- Never write a token literal into any file; keep ` + "`${NODE_AUTH_TOKEN}`" + ` indirection intact and do not create credential files

[Steps]
1. Read the failure log excerpt below and identify the root cause:
---
%s
---
2. Determine the project's actual lint/test/build commands from its CI config and manifests — do NOT assume Go. Inspect ` + "`.github/workflows`" + `, ` + "`package.json`" + ` scripts, ` + "`Makefile`" + `, or equivalent. The failure log above shows only the FIRST failing step (CI stops on first error), so enumerate EVERY step of the failing job in order — not just the one in the log — because earlier steps must still pass and later steps may also be broken
3. Apply a minimal source fix, then re-run the failing job's full step sequence in order, and require every step to pass. A fix edit can break a previously-passing step — in particular, run the project's formatter and linter (e.g. ` + "`prettier --check`/`--write`" + `, ` + "`eslint`" + `, ` + "`stylelint`" + `, ` + "`gofmt`" + `) over the files you edited and leave them clean. You MUST observe every step of the failing job green locally before declaring the fix verified. If a step cannot finish in the foreground within the 10-minute command timeout (e.g. a full test suite), run the tests scoped to the packages affected by this PR instead — those scoped commands are what you record in verified_commands
4. Write a JSON file ` + "`%s`" + ` in the working directory:
   {"verdict":"fixed|unfixable|nochange","summary":"<1-2 lines>","failing_checks":["..."],"suspected_cause":"<1-2 lines>","verified_commands":["<cmd>", "..."]}
   - "fixed": the failing step now passes locally after your change
   - "nochange": no source change was needed
   - "unfixable": see Fallback below
   - "verified_commands" (REQUIRED when verdict is "fixed"): the exact shell commands you ran and observed exit 0, in execution order. The calling tool re-runs each independently and will refuse to publish unless all pass, so each MUST be self-contained and re-runnable from the working directory with no extra setup — embed any required env inline (e.g. ` + "`GRAPHQL_ENDPOINT=https://... npm run build`" + `). Record only read-only lint/build/test commands; never a ` + "`git`" + ` or ` + "`gh`" + ` write

[Fallback — set verdict "unfixable"]
- A major breaking change is required (e.g. an API-incompatible library upgrade)
- The fix requires API keys, secrets, or external network access that is not available
- The project's lint/test/build cannot be run locally at all (e.g. dependencies cannot be installed)
- Build or test still fails after 3 attempts

%s[Output protocol]
- After writing ` + "`%s`" + `, print ` + "`VERIFY_DONE`" + ` on the final line
- No other explanatory output is needed
`

func buildVerifyPrompt(repo string, prNumber int, workDir, logsExcerpt, customPrompt string) string {
	return fmt.Sprintf(verifyPromptTemplate,
		repo, prNumber,
		workDir,
		logsExcerpt,
		verifyResultFile,
		additionalInstructionsBlock(customPrompt),
		verifyResultFile,
	)
}

// verifyAllowedTools is fixAllowedTools minus every publish verb (git add/
// commit/push, gh pr comment). The verify subprocess is structurally unable to
// publish even if the prompt is subverted.
const verifyAllowedTools = "Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) " +
	"Bash(gh run view:*) Bash(gh api:*) " +
	"Bash(git status:*) Bash(git diff:*) Bash(git log:*) Bash(git fetch:*) " +
	"Bash(git checkout:*) Bash(git remote:*) Bash(git show:*) " +
	"Bash(go:*) Bash(make:*) Bash(npm:*) Bash(pnpm:*) Bash(yarn:*) Bash(mise:*) " +
	"Bash(ls:*) Bash(cat:*) Bash(rg:*) Bash(fd:*) " +
	"Read Write Edit Glob Grep"

// runClaudeVerify is the default claudeVerifier: it runs the constrained verify
// subprocess with the scoped env and returns the parsed verdict file.
func runClaudeVerify(claudeBin, repo string, prNumber int, workDir, logs, customPrompt string, env []string, out io.Writer) (verifyVerdict, error) {
	prompt := buildVerifyPrompt(repo, prNumber, workDir, logs, customPrompt)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools", verifyAllowedTools,
		"-p", prompt,
	)
	cmd.Dir = workDir
	cmd.Env = env
	resultText, err := runClaudeStreaming(cmd, out, newRedactor())
	if err != nil {
		return verifyVerdict{}, fmt.Errorf("claude verify exec failed for PR #%d: %w", prNumber, err)
	}
	if !strings.Contains(resultText, "VERIFY_DONE") {
		return verifyVerdict{}, fmt.Errorf("claude did not report VERIFY_DONE for PR #%d", prNumber)
	}
	return readVerifyResult(workDir)
}

// readVerifyResult reads, removes, and parses the verdict file written by the
// verify subprocess.
func readVerifyResult(workDir string) (verifyVerdict, error) {
	path := filepath.Join(workDir, verifyResultFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return verifyVerdict{}, fmt.Errorf("read verify result: %w", err)
	}
	_ = os.Remove(path)
	var v verifyVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		return verifyVerdict{}, fmt.Errorf("parse verify result: %w", err)
	}
	if v.Verdict == "" {
		return verifyVerdict{}, fmt.Errorf("verify result missing verdict")
	}
	return v, nil
}

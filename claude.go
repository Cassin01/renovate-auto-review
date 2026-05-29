package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const commentFooter = "\n---\n👷 Generated with [renovate-auto-review](https://github.com/Cassin01/renovate-auto-review)\n"

const promptTemplate = `Target: %s PR #%d (Renovate auto-update)

You are an experienced reviewer. Follow the steps below to analyze the PR and post a comment to GitHub at the end.

1. Run ` + "`gh pr view %d --repo %s`" + ` to get the PR overview and status
2. Run ` + "`gh pr diff %d --repo %s`" + ` to review the diff
3. Run ` + "`gh pr checks %d --repo %s`" + ` to check CI results
4. Decide whether the PR is safe to merge based on the above, and generate an English comment using the following template:

%s
## Claude auto review
- **Merge verdict**: (one of: ✅ safe / ⚠️ needs review / ❌ do not merge)
- **Reason**: (2-4 lines of rationale)
- **CI status**: (list PASS/FAIL/PENDING)
- **Notes**: (only if any; otherwise write "none")
` + commentFooter + `
5. Post the generated comment via ` + "`gh pr comment %d --repo %s --body \"<comment body>\"`" + `
   - For multi-line bodies, you may use ` + "`--body-file -`" + ` with a heredoc

%sImportant:
- The first line of the comment body MUST contain %s (duplicate-detection marker)
- The footer block (starting with ` + "`---`" + `) MUST be included verbatim at the end of the comment body
- When uncertain, choose ⚠️ needs review; avoid asserting "safe" without confidence
- After a successful post, print only POSTED on the final output line
- No other explanatory output is needed
`

func buildPrompt(repo string, prNumber int, marker, customPrompt string) string {
	return fmt.Sprintf(promptTemplate,
		repo, prNumber,
		prNumber, repo,
		prNumber, repo,
		prNumber, repo,
		marker,
		prNumber, repo,
		additionalInstructionsBlock(customPrompt),
		marker,
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
func runClaudeStreaming(cmd *exec.Cmd, out io.Writer) (string, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	resultText, renderErr := renderClaudeStream(stdout, out)
	waitErr := cmd.Wait()
	if stderr.Len() > 0 {
		fmt.Fprint(out, stderr.String())
	}
	if waitErr != nil {
		return resultText, waitErr
	}
	return resultText, renderErr
}

func runClaudeAutoMode(claudeBin, repo string, prNumber int, marker, customPrompt string, out io.Writer) error {
	prompt := buildPrompt(repo, prNumber, marker, customPrompt)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools",
		"Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) Bash(gh pr comment:*)",
		"-p", prompt,
	)
	resultText, err := runClaudeStreaming(cmd, out)
	if err != nil {
		return fmt.Errorf("claude exec failed for PR #%d: %w", prNumber, err)
	}
	if !strings.Contains(resultText, "POSTED") {
		return fmt.Errorf("claude did not report POSTED for PR #%d", prNumber)
	}
	return nil
}

const fixPromptTemplate = `Target: %s PR #%d (Renovate auto-update). Make a minimal local fix for the failing CI and push directly to the PR branch ` + "`%s`" + `.

[Hard rules — violating any aborts the task]
- Working directory is fixed to ` + "`%s`" + `. Do not ` + "`cd`" + ` outside it
- Do not use ` + "`git commit --amend`" + ` / ` + "`git rebase`" + ` / ` + "`git reset --hard`" + ` / ` + "`git push --force`" + ` / ` + "`--force-with-lease`" + `
- Do not use ` + "`gh pr edit`" + ` (never touch the rebase checkbox in the PR body)
- Keep existing Renovate commits intact; add exactly one new commit on top
- Do not attempt major breaking changes to dependencies (only minor type fixes, test adjustments, lockfile regeneration)

[Steps]
1. ` + "`gh pr checkout %d --repo %s`" + ` to check out the branch
2. Read the failure log excerpt below and identify the root cause:
---
%s
---
3. Apply a minimal source fix and verify locally with build/test (e.g. ` + "`go build ./...`" + ` / ` + "`go test ./...`" + `)
4. ` + "`git add`" + ` -> ` + "`git commit -m \"fix(ci): <summary> [claude-auto-fix]\"`" + `
5. ` + "`git push origin HEAD:%s`" + ` (no force-style options)
6. Post the following template via ` + "`gh pr comment %d --repo %s --body-file -`" + `:

%s
## Claude auto-fix
- Verdict: one of ✅ fix pushed / ⚠️ partial fix / ❌ unfixable
- Failing checks: <list names>
- Suspected cause: (2-3 lines)
- Fix details: (2-3 lines; only when pushed. If not pushed, write "none")
` + commentFooter + `
[Fallback]
If any of the following applies, **do not push** — post a ❌ unfixable comment only and exit:
- A major breaking change is required (e.g. an API-incompatible library upgrade)
- A compound change that lockfile regeneration alone cannot make tests pass
- The fix requires API keys, secrets, or external network access
- Build or test still fails after 3 attempts

%s[Output protocol]
- On successful push + comment post: print ` + "`PUSHED`" + ` on the final line
- On comment-only post (unfixable fallback) success: print ` + "`POSTED`" + ` on the final line
- The footer block (starting with ` + "`---`" + `) MUST be included verbatim at the end of the comment body
- No other explanatory output is needed
`

func buildFixPrompt(repo string, prNumber int, headRef, marker, workDir, logsExcerpt, customPrompt string) string {
	return fmt.Sprintf(fixPromptTemplate,
		repo, prNumber, headRef,
		workDir,
		prNumber, repo,
		logsExcerpt,
		headRef,
		prNumber, repo,
		marker,
		additionalInstructionsBlock(customPrompt),
	)
}

const fixAllowedTools = "Bash(gh pr view:*) Bash(gh pr diff:*) Bash(gh pr checks:*) " +
	"Bash(gh pr checkout:*) Bash(gh pr comment:*) Bash(gh run view:*) " +
	"Bash(gh repo clone:*) Bash(gh api:*) " +
	"Bash(git status:*) Bash(git diff:*) Bash(git log:*) Bash(git add:*) " +
	"Bash(git commit:*) Bash(git push:*) Bash(git fetch:*) Bash(git checkout:*) " +
	"Bash(git remote:*) Bash(git show:*) " +
	"Bash(go:*) Bash(make:*) Bash(npm:*) Bash(pnpm:*) Bash(yarn:*) Bash(mise:*) " +
	"Bash(ls:*) Bash(cat:*) Bash(rg:*) Bash(fd:*) " +
	"Read Write Edit Glob Grep"

func runClaudeFixMode(claudeBin, repo string, prNumber int, headRef, marker, workDir, logs, customPrompt string, out io.Writer) error {
	prompt := buildFixPrompt(repo, prNumber, headRef, marker, workDir, logs, customPrompt)
	cmd := exec.Command(
		claudeBin,
		"--permission-mode", "auto",
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--allowedTools", fixAllowedTools,
		"-p", prompt,
	)
	cmd.Dir = workDir
	resultText, err := runClaudeStreaming(cmd, out)
	if err != nil {
		return fmt.Errorf("claude fix exec failed for PR #%d: %w", prNumber, err)
	}
	if !strings.Contains(resultText, "POSTED") && !strings.Contains(resultText, "PUSHED") {
		return fmt.Errorf("claude did not report PUSHED or POSTED for PR #%d", prNumber)
	}
	return nil
}

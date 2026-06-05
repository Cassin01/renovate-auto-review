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
	resultText, err := runClaudeStreaming(cmd, out, newRedactor())
	if err != nil {
		return fmt.Errorf("claude exec failed for PR #%d: %w", prNumber, err)
	}
	if !strings.Contains(resultText, "POSTED") {
		return fmt.Errorf("claude did not report POSTED for PR #%d", prNumber)
	}
	return nil
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
- Do not attempt major breaking changes to dependencies (only minor type fixes, test adjustments, lockfile regeneration)

[Secret handling — mandatory]
- A private registry token may be in the environment (e.g. ` + "`NODE_AUTH_TOKEN`" + ` / ` + "`NPM_TOKEN`" + ` / ` + "`NPM_CONFIG_*`" + `). It is already provided for installs — do not configure auth yourself
- Never ` + "`cat`" + `, echo, or print the contents of ` + "`.npmrc`" + `, ` + "`~/.npmrc`" + `, or any environment variable value
- Never write a token literal into any file; keep ` + "`${NODE_AUTH_TOKEN}`" + ` indirection intact and do not create credential files

[Steps]
1. Read the failure log excerpt below and identify the root cause:
---
%s
---
2. Determine the project's actual lint/test/build commands from its CI config and manifests — do NOT assume Go. Inspect ` + "`.github/workflows`" + `, ` + "`package.json`" + ` scripts, ` + "`Makefile`" + `, or equivalent, and reproduce the failing job's commands locally
3. Apply a minimal source fix, then re-run those same commands locally until every one passes. You MUST observe a green local run mirroring the failed CI job before declaring the fix verified
4. Write a JSON file ` + "`%s`" + ` in the working directory:
   {"verdict":"fixed|unfixable|nochange","summary":"<1-2 lines>","failing_checks":["..."],"suspected_cause":"<1-2 lines>"}
   - "fixed": the failing step now passes locally after your change
   - "nochange": no source change was needed
   - "unfixable": see Fallback below

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

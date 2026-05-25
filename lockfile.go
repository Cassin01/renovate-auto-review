package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// lockfileEcosystem describes a language ecosystem whose lockfile-only
// Renovate PRs can be regenerated deterministically without invoking Claude.
type lockfileEcosystem struct {
	name      string
	manifests []string
	locks     []string
	// regenCmd is the ordered set of commands that regenerate the lockfile
	// from the manifest. Each entry is invoked from the repository root.
	regenCmd [][]string
	// verifyCmd mirrors the CI checks. A push is only attempted when every
	// step exits zero.
	verifyCmd [][]string
}

func goEcosystem() *lockfileEcosystem {
	return &lockfileEcosystem{
		name:      "go",
		manifests: []string{"go.mod"},
		locks:     []string{"go.sum"},
		regenCmd:  [][]string{{"go", "mod", "tidy"}},
		verifyCmd: [][]string{
			{"go", "build", "./..."},
			{"go", "test", "-race", "-count=1", "./..."},
		},
	}
}

func lockfileEcosystems() []*lockfileEcosystem {
	return []*lockfileEcosystem{goEcosystem()}
}

// detectLockfileEcosystem returns the matching ecosystem when every changed
// file is a manifest or lock file of exactly one ecosystem at the repo root.
// Returns nil when the PR touches source code, lives in a subdirectory, or
// mixes multiple ecosystems — all cases that need richer reasoning than a
// deterministic regen can provide.
func detectLockfileEcosystem(files []string) *lockfileEcosystem {
	if len(files) == 0 {
		return nil
	}
	for _, eco := range lockfileEcosystems() {
		allowed := make(map[string]struct{}, len(eco.manifests)+len(eco.locks))
		for _, n := range eco.manifests {
			allowed[n] = struct{}{}
		}
		for _, n := range eco.locks {
			allowed[n] = struct{}{}
		}
		matched := true
		for _, f := range files {
			if _, ok := allowed[f]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return eco
		}
	}
	return nil
}

// commandRunner is the seam tests use to avoid invoking real CLIs. The
// returned bytes hold combined stdout+stderr so verification failures can
// be surfaced in the PR comment.
type commandRunner func(dir, name string, args ...string) ([]byte, error)

func defaultCommandRunner(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

type lockfileFixResult struct {
	Pushed       bool
	NoDiff       bool
	VerifyOutput string
}

// runLockfileFix performs clone → regen → verify → commit → push → comment.
// On any failure before push, returns a non-nil error and a result with the
// captured verifier output so the caller can decide whether to fall through
// to a richer (Claude-based) recovery path.
//
// The function never force-pushes and never amends; if the upstream branch
// has moved, `git push` will fail and propagate up.
func runLockfileFix(run commandRunner, repo, marker, workDir string, pr PR, eco *lockfileEcosystem) (*lockfileFixResult, error) {
	res := &lockfileFixResult{}

	if out, err := run(workDir, "gh", "repo", "clone", repo, "."); err != nil {
		return res, fmt.Errorf("gh repo clone: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := run(workDir, "git", "fetch", "origin", pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git fetch %s: %w (output: %s)", pr.HeadRefName, err, strings.TrimSpace(string(out)))
	}
	if out, err := run(workDir, "git", "checkout", pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git checkout %s: %w (output: %s)", pr.HeadRefName, err, strings.TrimSpace(string(out)))
	}

	for _, cmd := range eco.regenCmd {
		if out, err := run(workDir, cmd[0], cmd[1:]...); err != nil {
			return res, fmt.Errorf("regen %q: %w (output: %s)", strings.Join(cmd, " "), err, strings.TrimSpace(string(out)))
		}
	}

	statusOut, err := run(workDir, "git", "status", "--porcelain")
	if err != nil {
		return res, fmt.Errorf("git status: %w", err)
	}
	if !statusHasTrackedChange(string(statusOut), eco) {
		res.NoDiff = true
		return res, nil
	}

	var verifyBuf strings.Builder
	for _, cmd := range eco.verifyCmd {
		out, err := run(workDir, cmd[0], cmd[1:]...)
		fmt.Fprintf(&verifyBuf, "$ %s\n%s\n", strings.Join(cmd, " "), string(out))
		if err != nil {
			res.VerifyOutput = verifyBuf.String()
			return res, fmt.Errorf("verify %q failed: %w", strings.Join(cmd, " "), err)
		}
	}
	res.VerifyOutput = verifyBuf.String()

	addArgs := append([]string{"add", "--"}, append(eco.manifests, eco.locks...)...)
	if out, err := run(workDir, "git", addArgs...); err != nil {
		return res, fmt.Errorf("git add: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	commitMsg := fmt.Sprintf("fix(deps): regenerate %s lockfile [auto-lockfile]", eco.name)
	if out, err := run(workDir, "git", "commit", "-m", commitMsg); err != nil {
		return res, fmt.Errorf("git commit: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := run(workDir, "git", "push", "origin", "HEAD:"+pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git push: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	res.Pushed = true

	body := buildLockfileCommentBody(marker, eco, true, "")
	if err := postCommentViaFile(run, workDir, repo, pr.Number, body); err != nil {
		return res, fmt.Errorf("post comment: %w", err)
	}
	return res, nil
}

// statusHasTrackedChange reports whether `git status --porcelain` output
// modified at least one of the ecosystem's manifest/lock files. Unrelated
// edits are ignored so we never push noise produced by the regen step.
func statusHasTrackedChange(porcelain string, eco *lockfileEcosystem) bool {
	tracked := make(map[string]struct{}, len(eco.manifests)+len(eco.locks))
	for _, n := range eco.manifests {
		tracked[n] = struct{}{}
	}
	for _, n := range eco.locks {
		tracked[n] = struct{}{}
	}
	for _, line := range strings.Split(strings.TrimRight(porcelain, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		if _, ok := tracked[path]; ok {
			return true
		}
	}
	return false
}

func buildLockfileCommentBody(marker string, eco *lockfileEcosystem, success bool, failureDetail string) string {
	var b strings.Builder
	fmt.Fprintln(&b, marker)
	fmt.Fprintln(&b, "## Auto-fix (lockfile)")
	verdict := "✅ fix pushed"
	if !success {
		verdict = "❌ verification failed (no push)"
	}
	fmt.Fprintf(&b, "- **Verdict**: %s\n", verdict)
	fmt.Fprintf(&b, "- **Ecosystem**: %s\n", eco.name)
	fmt.Fprintf(&b, "- **Regen**: `%s`\n", joinCommands(eco.regenCmd))
	fmt.Fprintf(&b, "- **Verify**: %s\n", inlineCommands(eco.verifyCmd))
	if !success && failureDetail != "" {
		fmt.Fprintln(&b, "")
		fmt.Fprintln(&b, "<details><summary>verify output</summary>")
		fmt.Fprintln(&b, "")
		fmt.Fprintln(&b, "```")
		fmt.Fprintln(&b, strings.TrimSpace(failureDetail))
		fmt.Fprintln(&b, "```")
		fmt.Fprintln(&b, "</details>")
	}
	b.WriteString(commentFooter)
	return b.String()
}

func joinCommands(cmds [][]string) string {
	parts := make([]string, 0, len(cmds))
	for _, c := range cmds {
		parts = append(parts, strings.Join(c, " "))
	}
	return strings.Join(parts, " && ")
}

func inlineCommands(cmds [][]string) string {
	parts := make([]string, 0, len(cmds))
	for _, c := range cmds {
		parts = append(parts, "`"+strings.Join(c, " ")+"`")
	}
	return strings.Join(parts, " + ")
}

// postCommentViaFile writes the body to a temp file inside workDir and
// invokes `gh pr comment --body-file`. Avoids stdin plumbing in the runner
// abstraction while preserving multi-line and unicode bodies verbatim.
func postCommentViaFile(run commandRunner, workDir, repo string, prNumber int, body string) error {
	f, err := os.CreateTemp(workDir, "lockfile-comment-*.md")
	if err != nil {
		return fmt.Errorf("create temp body file: %w", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if out, err := run(workDir, "gh", "pr", "comment", strconv.Itoa(prNumber), "--repo", repo, "--body-file", abs); err != nil {
		return fmt.Errorf("gh pr comment: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

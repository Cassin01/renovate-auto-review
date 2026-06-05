package main

import (
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

// knownRegistryTokenEnvNames are the exact env var names treated as private
// registry credentials: forwarded to the verify phase, stripped from the
// publish phase, and used (by value, read ephemerally) for stdout redaction
// and the pre-publish secret scan.
var knownRegistryTokenEnvNames = []string{
	"NODE_AUTH_TOKEN",
	"NPM_TOKEN",
	"NPM_AUTH_TOKEN",
}

// knownRegistryTokenEnvPrefixes match env var name prefixes treated as
// registry credentials. NPM_CONFIG_ covers the
// `NPM_CONFIG_//registry/:_authToken` form; YARN_/PNPM_ cover their analogues.
var knownRegistryTokenEnvPrefixes = []string{
	"NPM_CONFIG_",
	"YARN_",
	"PNPM_",
}

// verifyEnvExactAllow is the baseline of env var names forwarded to the verify
// subprocess so the toolchain, gh, and claude itself function.
var verifyEnvExactAllow = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
	"TMPDIR": {}, "TERM": {}, "PWD": {}, "LANG": {}, "NODE_EXTRA_CA_CERTS": {},
	"GITHUB_TOKEN": {},
	"HTTP_PROXY":   {}, "HTTPS_PROXY": {}, "NO_PROXY": {},
	"http_proxy": {}, "https_proxy": {}, "no_proxy": {},
}

// verifyEnvPrefixAllow forwards whole families needed by claude (ANTHROPIC_/
// CLAUDE_/AWS_ for Bedrock), gh (GH_/GITHUB_), and locale/TLS settings.
var verifyEnvPrefixAllow = []string{
	"LC_", "XDG_", "SSL_CERT", "ANTHROPIC_", "CLAUDE_", "AWS_", "GH_", "GITHUB_",
}

func envName(kv string) string {
	name, _, _ := strings.Cut(kv, "=")
	return name
}

func envValue(kv string) string {
	_, value, _ := strings.Cut(kv, "=")
	return value
}

func isRegistryTokenEnv(name string) bool {
	if slices.Contains(knownRegistryTokenEnvNames, name) {
		return true
	}
	for _, p := range knownRegistryTokenEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func verifyEnvAllowed(name string) bool {
	if isRegistryTokenEnv(name) {
		return true
	}
	if _, ok := verifyEnvExactAllow[name]; ok {
		return true
	}
	for _, p := range verifyEnvPrefixAllow {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// buildVerifyEnv returns the allowlisted subset of environ handed to the verify
// subprocess: tooling/gh/claude baseline plus registry-token vars so `npm ci`
// can resolve `${NODE_AUTH_TOKEN}`. Everything else is dropped.
func buildVerifyEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if verifyEnvAllowed(envName(kv)) {
			out = append(out, kv)
		}
	}
	return out
}

// buildPublishEnv returns environ with every registry-token var removed, so the
// commit/push/comment phase cannot read the token.
func buildPublishEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if isRegistryTokenEnv(envName(kv)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// newStrippedRunner is a commandRunner that runs with a fixed env. Used for the
// publish phase with buildPublishEnv so git/gh never see the registry token.
func newStrippedRunner(env []string) commandRunner {
	return func(dir, name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		return cmd.CombinedOutput()
	}
}

// verifyVerdict is the structured result the verify subprocess writes to
// rar-verify-result.json. Verdict is one of fixed | unfixable | nochange.
type verifyVerdict struct {
	Verdict        string   `json:"verdict"`
	Summary        string   `json:"summary"`
	FailingChecks  []string `json:"failing_checks"`
	SuspectedCause string   `json:"suspected_cause"`
}

// claudeVerifier is the seam for the verify subprocess. Tests inject a fake.
type claudeVerifier func(workDir, logs string, env []string, out io.Writer) (verifyVerdict, error)

type ciFixResult struct {
	Pushed    bool
	NoChange  bool
	Unfixable bool
	Verdict   string
	Summary   string
}

// runCIFix performs the two-phase, token-safe CI auto-fix:
//
//	Phase 0 (tool):    clone + checkout the PR branch.
//	Phase 1 (claude):  verify in a scoped env that cannot commit/push/comment.
//	Phase 2 (tool):    stage tracked changes only, scan for secrets, then
//	                   commit/push/comment under a token-stripped runner.
//
// run is the publish runner (token-stripped). verifyEnv is the scoped env for
// the verify subprocess. A push only happens for a "fixed" verdict with a
// secret-free staged diff.
func runCIFix(run commandRunner, verify claudeVerifier, repo, marker, workDir string, pr PR, logs string, verifyEnv []string, out io.Writer) (*ciFixResult, error) {
	res := &ciFixResult{}

	if o, err := run(workDir, "gh", "repo", "clone", repo, "."); err != nil {
		return res, fmt.Errorf("gh repo clone: %w (output: %s)", err, strings.TrimSpace(string(o)))
	}
	if o, err := run(workDir, "git", "fetch", "origin", pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git fetch %s: %w (output: %s)", pr.HeadRefName, err, strings.TrimSpace(string(o)))
	}
	if o, err := run(workDir, "git", "checkout", pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git checkout %s: %w (output: %s)", pr.HeadRefName, err, strings.TrimSpace(string(o)))
	}

	verdict, err := verify(workDir, logs, verifyEnv, out)
	if err != nil {
		return res, fmt.Errorf("verify: %w", err)
	}
	res.Verdict = verdict.Verdict
	res.Summary = verdict.Summary

	if verdict.Verdict != "fixed" {
		if verdict.Verdict == "unfixable" {
			res.Unfixable = true
		} else {
			res.NoChange = true
		}
		body := buildCIFixCommentBody(marker, verdict)
		return res, postCommentViaFile(run, workDir, repo, pr.Number, body)
	}

	statusOut, err := run(workDir, "git", "status", "--porcelain")
	if err != nil {
		return res, fmt.Errorf("git status: %w", err)
	}
	paths := trackedModifiedPaths(string(statusOut))
	if len(paths) == 0 {
		res.NoChange = true
		body := buildCIFixCommentBody(marker, verifyVerdict{
			Verdict: "nochange",
			Summary: "verify reported a fix but produced no tracked changes",
		})
		return res, postCommentViaFile(run, workDir, repo, pr.Number, body)
	}

	addArgs := append([]string{"add", "--"}, paths...)
	if o, err := run(workDir, "git", addArgs...); err != nil {
		return res, fmt.Errorf("git add: %w (output: %s)", err, strings.TrimSpace(string(o)))
	}

	red := newRedactor()
	diffOut, err := run(workDir, "git", "diff", "--cached")
	if err != nil {
		return res, fmt.Errorf("git diff --cached: %w", err)
	}
	body := buildCIFixCommentBody(marker, verdict)
	if hits := scanForSecrets(red, string(diffOut)); len(hits) > 0 {
		_ = postCommentViaFile(run, workDir, repo, pr.Number, buildSecretAbortCommentBody(marker, hits))
		return res, fmt.Errorf("aborted: potential secret in staged diff (%s)", strings.Join(hits, ", "))
	}
	if hits := scanForSecrets(red, body); len(hits) > 0 {
		_ = postCommentViaFile(run, workDir, repo, pr.Number, buildSecretAbortCommentBody(marker, hits))
		return res, fmt.Errorf("aborted: potential secret in comment body (%s)", strings.Join(hits, ", "))
	}

	commitMsg := fmt.Sprintf("fix(ci): %s [claude-auto-fix]", commitSummary(verdict.Summary))
	if o, err := run(workDir, "git", "commit", "-m", commitMsg); err != nil {
		return res, fmt.Errorf("git commit: %w (output: %s)", err, strings.TrimSpace(string(o)))
	}
	if o, err := run(workDir, "git", "push", "origin", "HEAD:"+pr.HeadRefName); err != nil {
		return res, fmt.Errorf("git push: %w (output: %s)", err, strings.TrimSpace(string(o)))
	}
	res.Pushed = true

	return res, postCommentViaFile(run, workDir, repo, pr.Number, body)
}

// trackedModifiedPaths returns paths of tracked, modified files from
// `git status --porcelain`, excluding untracked (`??`) entries. Excluding
// untracked files structurally prevents an accidental credential file (e.g. a
// freshly written `.npmrc` holding a literal token) from being committed.
func trackedModifiedPaths(porcelain string) []string {
	var paths []string
	for line := range strings.SplitSeq(strings.TrimRight(porcelain, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		if line[:2] == "??" {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// commitSummary collapses a verdict summary to a single short commit subject.
func commitSummary(summary string) string {
	s := strings.TrimSpace(summary)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "resolve failing CI"
	}
	if r := []rune(s); len(r) > 72 {
		s = strings.TrimSpace(string(r[:72]))
	}
	return s
}

func buildCIFixCommentBody(marker string, v verifyVerdict) string {
	var b strings.Builder
	fmt.Fprintln(&b, marker)
	fmt.Fprintln(&b, "## Claude auto-fix")
	verdict := "✅ fix pushed"
	switch v.Verdict {
	case "unfixable":
		verdict = "❌ unfixable (no push)"
	case "nochange":
		verdict = "⚠️ no change produced (no push)"
	}
	fmt.Fprintf(&b, "- **Verdict**: %s\n", verdict)
	if len(v.FailingChecks) > 0 {
		fmt.Fprintf(&b, "- **Failing checks**: %s\n", strings.Join(v.FailingChecks, ", "))
	}
	if v.SuspectedCause != "" {
		fmt.Fprintf(&b, "- **Suspected cause**: %s\n", v.SuspectedCause)
	}
	if v.Summary != "" {
		fmt.Fprintf(&b, "- **Details**: %s\n", v.Summary)
	}
	b.WriteString(commentFooter)
	return b.String()
}

// buildSecretAbortCommentBody reports a secret-scan abort. It lists matched
// pattern names only — never the matched value.
func buildSecretAbortCommentBody(marker string, hits []string) string {
	var b strings.Builder
	fmt.Fprintln(&b, marker)
	fmt.Fprintln(&b, "## Claude auto-fix")
	fmt.Fprintln(&b, "- **Verdict**: ❌ aborted — potential secret detected (no push)")
	fmt.Fprintf(&b, "- **Matched patterns**: %s\n", strings.Join(hits, ", "))
	b.WriteString(commentFooter)
	return b.String()
}

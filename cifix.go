package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
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
	// Go toolchain provisioning: without these the build/test re-run falls back
	// to the default GOPATH ($HOME/go), which may not be writable for a
	// go.mod-directed toolchain auto-download.
	"GOPATH": {}, "GOTOOLCHAIN": {}, "GOMODCACHE": {}, "GOCACHE": {}, "GOFLAGS": {},
	"BASH_DEFAULT_TIMEOUT_MS": {}, "BASH_MAX_TIMEOUT_MS": {},
}

// verifyBashTimeoutDefaults raise the claude Bash tool's command timeout to its
// 10-minute maximum. The verify subprocess runs `claude --print` (one-shot):
// with the 120s default, a full test suite gets auto-moved to the background
// and claude ends its turn to wait — which terminates the headless session
// before VERIFY_DONE is printed. Values already present in the environment
// take precedence.
var verifyBashTimeoutDefaults = []string{
	"BASH_DEFAULT_TIMEOUT_MS=600000",
	"BASH_MAX_TIMEOUT_MS=600000",
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
// can resolve `${NODE_AUTH_TOKEN}`. Everything else is dropped. Bash timeout
// defaults are appended unless the caller's environment already sets them.
func buildVerifyEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+len(verifyBashTimeoutDefaults))
	for _, kv := range environ {
		if verifyEnvAllowed(envName(kv)) {
			out = append(out, kv)
		}
	}
	for _, def := range verifyBashTimeoutDefaults {
		present := slices.ContainsFunc(out, func(kv string) bool {
			return envName(kv) == envName(def)
		})
		if !present {
			out = append(out, def)
		}
	}
	return out
}

// reverifyEnvDenyPrefixes are the credential families the verify env forwards
// only so claude and gh can run. The re-verification commands are plain
// lint/build/test re-runs and need neither.
var reverifyEnvDenyPrefixes = []string{"ANTHROPIC_", "CLAUDE_", "AWS_", "GH_", "GITHUB_"}

func reverifyEnvDenied(name string) bool {
	for _, p := range reverifyEnvDenyPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// buildReverifyEnv returns the verify env minus the claude/gh/AWS credential
// families. The registry-token vars stay so `npm ci` can still resolve
// `${NODE_AUTH_TOKEN}`.
func buildReverifyEnv(environ []string) []string {
	return slices.DeleteFunc(buildVerifyEnv(environ), func(kv string) bool {
		return reverifyEnvDenied(envName(kv))
	})
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
//
// VerifiedCommands are the exact shell commands the verify phase reports it ran
// and observed green. The publish phase re-runs each independently and refuses
// to push unless all pass, turning the self-reported "fixed" verdict into a
// machine-checked gate.
type verifyVerdict struct {
	Verdict          string   `json:"verdict"`
	Summary          string   `json:"summary"`
	FailingChecks    []string `json:"failing_checks"`
	SuspectedCause   string   `json:"suspected_cause"`
	VerifiedCommands []string `json:"verified_commands"`
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
//	Phase 1.5 (tool):  independently re-run the commands the verify phase
//	                   reported green; abort the publish unless all pass.
//	Phase 2 (tool):    stage tracked changes only, scan for secrets, then
//	                   commit/push/comment under a token-stripped runner.
//
// run is the publish runner (token-stripped). reverify runs the re-verification
// commands with buildReverifyEnv (registry token present, claude/gh/AWS
// credentials absent); each command must pass parseReverifyCommand and runs
// without a shell. verifyEnv is the scoped env for the verify subprocess. A
// push only happens for a "fixed" verdict whose reported commands all re-run
// green and whose staged diff is secret-free.
func runCIFix(run, reverify commandRunner, verify claudeVerifier, repo, marker, workDir string, pr PR, logs string, verifyEnv []string, out io.Writer) (*ciFixResult, error) {
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

	if failedCmd, rerr := reverifyCommands(reverify, workDir, verdict.VerifiedCommands); rerr != nil {
		red := newRedactor()
		body := buildReverifyAbortCommentBody(marker, failedCmd)
		if errors.Is(rerr, errEnvUnavailable) {
			body = buildReverifyUnavailableCommentBody(marker, failedCmd)
		}
		if hits := scanForSecrets(red, body); len(hits) > 0 {
			body = buildSecretAbortCommentBody(marker, hits)
		}
		_ = postCommentViaFile(run, workDir, repo, pr.Number, body)
		return res, fmt.Errorf("re-verification gate: %w", rerr)
	}

	statusOut, err := run(workDir, "git", "status", "--porcelain")
	if err != nil {
		return res, fmt.Errorf("git status: %w", err)
	}
	paths := stageablePaths(string(statusOut))
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

// credentialBasenames are filenames that commonly carry a literal credential.
// They are excluded from staging even when untracked, so a credential file
// accidentally written by the verify phase is never committed. This is defense
// in depth; the staged-diff secret scan independently catches token values.
var credentialBasenames = map[string]struct{}{
	".npmrc": {}, ".env": {}, ".netrc": {}, "_netrc": {},
	".git-credentials": {}, ".pypirc": {}, ".dockercfg": {}, "credentials": {},
}

// isCredentialFile reports whether p's basename is a known credential filename
// (including the `.env.*` family).
func isCredentialFile(p string) bool {
	base := path.Base(p)
	if _, ok := credentialBasenames[base]; ok {
		return true
	}
	return strings.HasPrefix(base, ".env.")
}

// stageablePaths returns the paths to stage from `git status --porcelain`:
// tracked modifications plus newly-created files. Because the auto-fix runs in
// a fresh clone, any untracked file present after the verify phase was created
// by that phase. Two classes of untracked entry are skipped: directories
// (porcelain collapses them to a single `dir/` line, so staging would pull in a
// non-gitignored tree like `node_modules/`) and credential-named files. The
// staged-diff secret scan remains the content gate for token values.
func stageablePaths(porcelain string) []string {
	var paths []string
	for line := range strings.SplitSeq(strings.TrimRight(porcelain, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		if p == "" {
			continue
		}
		if line[:2] == "??" && (strings.HasSuffix(p, "/") || isCredentialFile(p)) {
			continue
		}
		paths = append(paths, p)
	}
	return paths
}

// reverifyPrograms is the positive allowlist for re-verification commands: the
// toolchain the verify phase is pre-approved to run (verifyAllowedTools), each
// limited to the subcommands that lint/build/test. A nil list allows any
// arguments. Anything else — git, gh, curl, npx, publish/exec subcommands — is
// rejected before execution.
var reverifyPrograms = map[string][]string{
	"go":   {"build", "test", "vet"},
	"npm":  {"run", "run-script", "test", "ci"},
	"pnpm": {"run", "test", "install"},
	"yarn": {"run", "test", "install"},
	"mise": {"run"},
	"make": nil,
}

// describeReverifyAllowlist renders reverifyPrograms for the verify prompt, so
// the commands Claude is told to record are the ones the gate accepts.
func describeReverifyAllowlist() string {
	entries := make([]string, 0, len(reverifyPrograms))
	for _, prog := range slices.Sorted(maps.Keys(reverifyPrograms)) {
		if subs := reverifyPrograms[prog]; subs != nil {
			entries = append(entries, "`"+prog+" "+strings.Join(subs, "|")+"`")
		} else {
			entries = append(entries, "`"+prog+" [args]`")
		}
	}
	return strings.Join(entries, ", ")
}

// reverifyTokenRe admits no shell syntax. Commands run without a shell, so a
// command written for one (quotes, &&, pipes, $VAR) is rejected rather than
// silently executed with different meaning.
var reverifyTokenRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

var envAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=`)

// parseReverifyCommand validates cmd against the allowlist and returns the
// argv to hand to env(1): leading NAME=value assignments, then the program and
// its arguments.
func parseReverifyCommand(cmd string) ([]string, error) {
	argv := strings.Fields(cmd)
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	for _, tok := range argv {
		if !reverifyTokenRe.MatchString(tok) {
			return nil, errors.New("shell syntax is not allowed")
		}
	}
	i := 0
	for ; i < len(argv); i++ {
		m := envAssignRe.FindStringSubmatch(argv[i])
		if m == nil {
			break
		}
		if m[1] == "PATH" || reverifyEnvDenied(m[1]) {
			return nil, fmt.Errorf("assignment to %s is not allowed", m[1])
		}
	}
	if i == len(argv) {
		return nil, errors.New("no program to run")
	}
	subcommands, ok := reverifyPrograms[argv[i]]
	if !ok {
		return nil, fmt.Errorf("program %q is not allowed", argv[i])
	}
	if subcommands != nil && (i+1 == len(argv) || !slices.Contains(subcommands, argv[i+1])) {
		return nil, fmt.Errorf("%s: allowed subcommands are %s", argv[i], strings.Join(subcommands, ", "))
	}
	return argv, nil
}

// errEnvUnavailable marks a re-verification failure caused by an unreachable
// dependency (DNS/connection failure) rather than a defective fix. The caller
// uses it to post an environment-issue comment instead of a fix-rejection one.
var errEnvUnavailable = errors.New("re-verification environment unavailable")

// reverifyNetworkRetries is how many extra attempts a re-verification command
// gets after its first failure, but only when that failure looks like a
// transient network/DNS problem. The verify phase reached the same dependency
// moments earlier, so a single retry rescues most flakes without re-running a
// long build more than twice.
var reverifyNetworkRetries = 1

// reverifyRetryBackoff is the pause before a network-failure retry. Overridden
// to zero in tests.
var reverifyRetryBackoff = 3 * time.Second

// transientNetworkSignatures are substrings (matched case-insensitively) that
// mark a command failure as an unreachable-dependency problem: a build/test
// that fetches a remote GraphQL schema, downloads a module, or pulls an image
// can fail this way even when the source change is correct. Compile/test
// failures never match these, so they are not retried.
var transientNetworkSignatures = []string{
	"getaddrinfo",                          // Node DNS resolver
	"enotfound",                            // DNS: host not found
	"eai_again",                            // DNS: temporary failure
	"could not resolve host",               // curl/git
	"temporary failure in name resolution", // glibc resolver
	"econnrefused",                         // connection refused
	"econnreset",                           // connection reset by peer
	"etimedout",                            // socket timeout
	"network is unreachable",               // no route
	"connection timed out",                 // TCP connect timeout
	"tls handshake timeout",                // TLS stall
	"i/o timeout",                          // Go net timeout
	"dial tcp",                             // Go net dial failure
}

// isTransientNetworkFailure reports whether a failed command's combined output
// indicates an unreachable dependency rather than a real lint/build/test error.
func isTransientNetworkFailure(output string) bool {
	lower := strings.ToLower(output)
	for _, sig := range transientNetworkSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// reverifyCommands independently re-runs the commands the verify phase reported
// it observed green. It returns the offending command (for the abort comment)
// and an error when the contract is violated or any command fails. An empty
// list under a "fixed" verdict is itself a violation: a fix must have at least
// one re-runnable check to confirm.
func reverifyCommands(reverify commandRunner, workDir string, cmds []string) (string, error) {
	if len(cmds) == 0 {
		return "", fmt.Errorf("verify reported \"fixed\" but recorded no verified_commands to re-run")
	}
	argvs := make([][]string, len(cmds))
	for i, cmd := range cmds {
		argv, err := parseReverifyCommand(cmd)
		if err != nil {
			return cmd, fmt.Errorf("verified_commands entry rejected: %q: %w", cmd, err)
		}
		argvs[i] = argv
	}
	for i, argv := range argvs {
		if err := runReverifyCommand(reverify, workDir, cmds[i], argv); err != nil {
			return cmds[i], err
		}
	}
	return "", nil
}

// runReverifyCommand runs one re-verification command. A genuine lint/build/
// test failure is returned immediately. A failure that looks like a transient
// network/DNS problem is retried up to reverifyNetworkRetries times; if it
// still fails that way, the error wraps errEnvUnavailable so the caller reports
// it as an environment issue rather than a fix rejection.
func runReverifyCommand(reverify commandRunner, workDir, cmd string, argv []string) error {
	var lastOut string
	for attempt := 0; attempt <= reverifyNetworkRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(reverifyRetryBackoff)
		}
		o, err := reverify(workDir, "env", argv...)
		if err == nil {
			return nil
		}
		out := strings.TrimSpace(string(o))
		if !isTransientNetworkFailure(out) {
			return fmt.Errorf("command failed: %q: %w (output: %s)", cmd, err, out)
		}
		lastOut = out
	}
	return fmt.Errorf("%w: %q could not reach a required dependency after %d attempts (output: %s)",
		errEnvUnavailable, cmd, reverifyNetworkRetries+1, lastOut)
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

// buildReverifyAbortCommentBody reports that the tool's independent re-run of
// the verify phase's reported commands failed, so the fix was not published. It
// includes the failing command (token references stay as ${VAR} indirection)
// but never its output, which could echo a resolved secret.
func buildReverifyAbortCommentBody(marker, failedCmd string) string {
	var b strings.Builder
	fmt.Fprintln(&b, marker)
	fmt.Fprintln(&b, "## Claude auto-fix")
	fmt.Fprintln(&b, "- **Verdict**: ❌ aborted — local re-verification failed (no push)")
	if failedCmd != "" {
		fmt.Fprintf(&b, "- **Failed command**: `%s`\n", failedCmd)
	} else {
		fmt.Fprintln(&b, "- **Detail**: verify reported a fix but recorded no commands to re-run")
	}
	b.WriteString(commentFooter)
	return b.String()
}

// buildReverifyUnavailableCommentBody reports that the tool's independent
// re-run could not reach a required dependency (network/DNS), so the fix was
// not published. This is distinct from buildReverifyAbortCommentBody: the fix
// may well be correct — it simply could not be re-verified — so the wording
// avoids implying the change itself failed.
func buildReverifyUnavailableCommentBody(marker, failedCmd string) string {
	var b strings.Builder
	fmt.Fprintln(&b, marker)
	fmt.Fprintln(&b, "## Claude auto-fix")
	fmt.Fprintln(&b, "- **Verdict**: ⚠️ not re-verified — a required dependency was unreachable (no push)")
	if failedCmd != "" {
		fmt.Fprintf(&b, "- **Unreachable during**: `%s`\n", failedCmd)
	}
	fmt.Fprintln(&b, "- **Detail**: the fix could not be independently re-verified because a build/test dependency (a network/DNS endpoint) was unavailable. This is an environment issue, not a fix rejection — re-run once the dependency is reachable.")
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

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

const defaultMarker = "<!-- claude-auto-mergeable-check -->"

// Painters for the orchestrator's own output, initialized once from the
// streams' TTY state. The LLM stream is written uncolored via stream.go.
var (
	sout = newPainter(os.Stdout)
	serr = newPainter(os.Stderr)
)

func main() {
	log.SetOutput(&lineColorWriter{w: os.Stderr, code: ansiRed, enabled: colorEnabled(os.Stderr)})

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: %s [flags] <owner/name>\n\nflags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	cfgPath := flag.String("config", "", "path to config.toml (default: $XDG_CONFIG_HOME/renovate-auto-review/config.toml)")
	dryRun := flag.Bool("dry-run", false, "list target Renovate PRs but do not invoke claude")
	prNum := flag.Int("pr", 0, "review only this PR number (default: all open Renovate PRs)")
	flag.Parse()

	repo, err := parseRepoArg(flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, serr.failure(fmt.Sprintf("error: %v", err)))
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(2)
	}

	if *prNum < 0 {
		fmt.Fprintln(os.Stderr, serr.failure(fmt.Sprintf("error: -pr must be a positive PR number, got %d", *prNum)))
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(2)
	}

	path := *cfgPath
	if path == "" {
		p, err := defaultConfigPath()
		if err != nil {
			log.Fatalf("resolve default config path: %v", err)
		}
		path = p
	}

	cfg, err := loadConfig(path)
	if err != nil {
		if nf, ok := errors.AsType[*configNotFoundError](err); ok {
			fmt.Fprintln(os.Stderr, serr.failure(fmt.Sprintf("error: %s", err)))
			fmt.Fprintf(os.Stderr, "\nplease create %s with the following template:\n\n%s\n",
				nf.Path, sampleConfig)
			os.Exit(1)
		}
		log.Fatalf("load config: %v", err)
	}

	if err := checkOwnerAllowed(repo, cfg.AllowedOwners); err != nil {
		fmt.Fprintln(os.Stderr, serr.failure(fmt.Sprintf("error: %v", err)))
		os.Exit(1)
	}

	if err := runAll(cfg, []string{repo}, *dryRun, *prNum); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func parseRepoArg(args []string) (string, error) {
	switch len(args) {
	case 0:
		return "", errors.New("missing target repository (expected owner/name as the last argument)")
	case 1:
		// ok
	default:
		return "", fmt.Errorf("only one repository can be specified, got %d", len(args))
	}
	r := args[0]
	if !strings.Contains(r, "/") || strings.HasPrefix(r, "/") || strings.HasSuffix(r, "/") {
		return "", fmt.Errorf("repository %q is not in owner/name format", r)
	}
	return r, nil
}

func runAll(cfg *Config, repos []string, dryRun bool, prNumber int) error {
	var claudeBin string
	if !dryRun {
		bin, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude CLI not found in PATH: %w", err)
		}
		claudeBin = bin
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found in PATH: %w", err)
	}

	fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("config: %s\nrepos: %d, limit: %d",
		cfg.SourcePath, len(repos), cfg.Limit)))

	var totalProcessed, totalSkipped, totalFailed int
	for _, repo := range repos {
		fmt.Fprintf(os.Stdout, "\n%s\n", sout.header(fmt.Sprintf("###### %s ######", repo)))
		p, s, f := processRepo(claudeBin, repo, defaultMarker, cfg, dryRun, prNumber)
		totalProcessed += p
		totalSkipped += s
		totalFailed += f
	}

	fmt.Fprintf(os.Stdout, "\n%s\n", sout.header(fmt.Sprintf("=== TOTAL: processed=%d / skipped=%d / errors=%d ===",
		totalProcessed, totalSkipped, totalFailed)))
	if totalFailed > 0 {
		os.Exit(1)
	}
	return nil
}

func processRepo(claudeBin, repo, marker string, cfg *Config, dryRun bool, prNumber int) (processed, skipped, failed int) {
	var prs []PR
	if prNumber > 0 {
		pr, err := fetchPR(repo, prNumber)
		if err != nil {
			log.Printf("%s: fetch PR #%d: %v", repo, prNumber, err)
			return 0, 0, 1
		}
		prs = []PR{pr}
	} else {
		listed, err := listOpenPRs(repo, cfg.Limit)
		if err != nil {
			log.Printf("%s: list PRs: %v", repo, err)
			return 0, 0, 1
		}
		prs = listed
	}

	var targets []PR
	for _, pr := range prs {
		if isRenovatePR(pr) {
			targets = append(targets, pr)
		}
	}

	if prNumber > 0 {
		fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("Target PR #%d: renovate=%t", prNumber, len(targets) > 0)))
		if len(targets) == 0 {
			fmt.Fprintln(os.Stdout, sout.warn(fmt.Sprintf("#%d is not a Renovate PR; skipping", prNumber)))
		}
	} else {
		fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("Open PRs: %d, Renovate PRs: %d", len(prs), len(targets))))
	}
	for _, pr := range targets {
		fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("  - #%d  mergeable=%-9s  %s", pr.Number, pr.Mergeable, pr.Title)))
	}

	if dryRun {
		fmt.Fprintln(os.Stdout, sout.info("[dry-run] claude is not invoked."))
		return 0, 0, 0
	}

	for _, pr := range targets {
		fmt.Fprintf(os.Stdout, "\n%s\n", sout.header(fmt.Sprintf("=== %s PR #%d: %s ===", repo, pr.Number, pr.Title)))

		comments, commits, err := fetchPRReviewState(repo, pr.Number)
		if err != nil {
			log.Printf("%s #%d: review state lookup failed: %v", repo, pr.Number, err)
			failed++
			continue
		}

		checks, checksErr := fetchPRChecks(repo, pr.Number)
		if checksErr != nil {
			log.Printf("%s #%d: fetch checks: %v (proceeding without CI status)", repo, pr.Number, checksErr)
		}
		ciFailing := checksErr == nil && hasFailingChecks(checks)

		actionRan := false
		conflicting := pr.Mergeable == mergeableConflicting

		switch {
		case conflicting:
			fmt.Fprintln(os.Stdout, sout.warn("review skip: PR is conflicting"))
		case ciFailing:
			fmt.Fprintln(os.Stdout, sout.warn("review skip: CI is failing"))
		case shouldSkipReview(comments, commits, marker):
			fmt.Fprintln(os.Stdout, sout.warn("review skip: marker present and no new commits since"))
		default:
			fmt.Fprintln(os.Stdout, sout.delim("--- claude output ---"))
			err := runClaudeAutoMode(claudeBin, repo, pr.Number, marker, cfg.CustomPrompt, os.Stdout)
			fmt.Fprintln(os.Stdout, sout.delim("--- end ---"))
			if err != nil {
				log.Printf("%s #%d: %v", repo, pr.Number, err)
				failed++
			} else {
				actionRan = true
			}
		}

		rebaseRequested := false
		if shouldRequestRebase(pr, cfg, comments, commits, marker) {
			if err := requestRebase(repo, marker, cfg, pr); err != nil {
				log.Printf("%s #%d: request-rebase: %v", repo, pr.Number, err)
				failed++
			} else {
				fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("requested Renovate rebase (label %q)", cfg.RebaseLabel)))
				actionRan = true
				rebaseRequested = true
			}
		}

		if !rebaseRequested && ciFailing && (cfg.AutoFixCI || hasAutoFixLabel(pr, cfg.AutoFixLabel)) {
			handled, ran, err := tryFixLockfileForPR(repo, marker, cfg, pr)
			if err != nil {
				log.Printf("%s #%d: lockfile-fix: %v", repo, pr.Number, err)
			}
			if ran {
				actionRan = true
			}
			if !handled {
				ran, err := tryFixCI(claudeBin, repo, marker, cfg, pr, checks)
				if err != nil {
					log.Printf("%s #%d: auto-fix: %v", repo, pr.Number, err)
					failed++
				}
				if ran {
					actionRan = true
				}
			}
		}

		if actionRan {
			processed++
		} else {
			skipped++
		}
	}
	return processed, skipped, failed
}

// tryFixLockfileForPR runs the deterministic lockfile-only fix path when the
// PR exclusively modifies manifest/lock files of a supported ecosystem.
//
// Return values:
//   - handled=true means the lockfile path took responsibility for this PR
//     and the caller MUST skip Claude. This includes both push success and
//     deliberate decisions like "regen produced no diff, but a Claude retry
//     would produce the same no-op."
//   - handled=false means the PR is out of scope for this path; the caller
//     should fall through to the Claude-based recovery.
//   - ran=true means an externally-visible action occurred (push + comment).
func tryFixLockfileForPR(repo, marker string, cfg *Config, pr PR) (handled, ran bool, err error) {
	files, err := fetchPRFiles(repo, pr.Number)
	if err != nil {
		return false, false, fmt.Errorf("fetch pr files: %w", err)
	}
	eco := detectLockfileEcosystem(files)
	if eco == nil {
		return false, false, nil
	}

	workDir, err := os.MkdirTemp(cfg.WorkDir, fmt.Sprintf("rar-lock-%d-", pr.Number))
	if err != nil {
		return true, false, fmt.Errorf("mkdir workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("lockfile-fix: ecosystem=%s workDir=%s files=%v", eco.name, workDir, files)))

	res, runErr := runLockfileFix(defaultCommandRunner, repo, marker, workDir, pr, eco)
	if res != nil && res.VerifyOutput != "" {
		fmt.Fprintln(os.Stdout, sout.delim("--- lockfile verify output ---"))
		fmt.Fprintln(os.Stdout, res.VerifyOutput)
		fmt.Fprintln(os.Stdout, sout.delim("--- end ---"))
	}
	switch {
	case runErr != nil:
		// Fall back to the Claude path so a richer reasoning loop has a
		// chance to address the underlying breakage.
		return false, false, runErr
	case res.NoDiff:
		// A no-op regen means the CI failure is not a lockfile-drift
		// problem. Delegate to Claude.
		fmt.Fprintln(os.Stdout, sout.warn("lockfile-fix: regen produced no diff; falling through to Claude"))
		return false, false, nil
	case res.Pushed:
		return true, true, nil
	default:
		return false, false, nil
	}
}

// tryFixCI returns ran=true only when Claude fix mode was actually invoked.
// Early exits (no Actions log) and pre-invocation errors return ran=false so
// the caller can distinguish "no-op" from "attempted". The caller is
// responsible for ensuring checks contains failing entries.
func tryFixCI(claudeBin, repo, marker string, cfg *Config, pr PR, checks []CheckRun) (bool, error) {
	runIDs := pickFailedRunIDs(checks)
	var logs strings.Builder
	// Distinguish "no run was actionable" from "an actionable run's logs could
	// not be retrieved" so the skip message does not misattribute a log-
	// availability problem to run state.
	logUnavailable := false
	for _, id := range runIDs {
		// The rollup that produced runIDs can lag the run's actual state: a
		// re-run may have since turned green, or the failure may be a
		// cancellation rather than a fixable error. Re-validate before
		// attempting a log fetch that would otherwise fail or fix nothing.
		st, err := fetchRunState(repo, id)
		if err != nil {
			fmt.Fprintln(os.Stderr, serr.warn(fmt.Sprintf("warn: run %s state lookup failed: %v; skipping", id, err)))
			logUnavailable = true
			continue
		}
		if !runIsForHead(st.HeadSha, pr.HeadRefOid) {
			fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf(
				"auto-fix: run %s is for a superseded commit (run head %s, PR head %s); its failure may already be fixed — skipping",
				id, shortSHA(st.HeadSha), shortSHA(pr.HeadRefOid))))
			continue
		}
		if !strings.EqualFold(st.Status, "completed") || !isGenuineCIFailure(st.Conclusion) {
			fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf(
				"auto-fix: run %s not an actionable failure (status=%s conclusion=%s); skipping",
				id, st.Status, st.Conclusion)))
			continue
		}
		runLog, err := fetchFailedRunLog(repo, id)
		if err != nil {
			fmt.Fprintln(os.Stderr, serr.warn(fmt.Sprintf("warn: fetch run %s log: %v", id, err)))
			logUnavailable = true
			continue
		}
		fmt.Fprintf(&logs, "===== run %s =====\n%s\n", id, runLog)
	}
	if logs.Len() == 0 {
		if logUnavailable {
			fmt.Fprintln(os.Stdout, sout.warn("auto-fix: failing run is actionable but its logs are unavailable from GitHub (archive incomplete or state lookup failed). Skipping."))
		} else {
			fmt.Fprintln(os.Stdout, sout.warn("auto-fix: no actionable failing Actions run (re-run succeeded, cancelled, or renovate-side status). Skipping."))
		}
		return false, nil
	}

	workDir, err := os.MkdirTemp(cfg.WorkDir, fmt.Sprintf("rar-%d-", pr.Number))
	if err != nil {
		return false, fmt.Errorf("mkdir workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	fmt.Fprintln(os.Stdout, sout.info(fmt.Sprintf("auto-fix: workDir=%s, failing checks=%d", workDir, len(runIDs))))

	verifier := func(wd, lg string, env []string, w io.Writer) (verifyVerdict, error) {
		return runClaudeVerify(claudeBin, repo, pr.Number, wd, lg, cfg.CustomPrompt, env, w)
	}
	publishRunner := newStrippedRunner(buildPublishEnv(os.Environ()))
	// The re-verification runner carries the scoped verify env (registry token
	// present) so lint/build/test reproduce the verify phase; it never performs
	// git/gh writes, so the token cannot leak through it.
	reverifyRunner := newStrippedRunner(buildVerifyEnv(os.Environ()))

	fmt.Fprintln(os.Stdout, sout.delim("--- claude verify output ---"))
	res, err := runCIFix(publishRunner, reverifyRunner, verifier, repo, marker, workDir, pr, logs.String(), buildVerifyEnv(os.Environ()), os.Stdout)
	fmt.Fprintln(os.Stdout, sout.delim("--- end ---"))
	if res != nil {
		switch {
		case res.Pushed:
			fmt.Fprintln(os.Stdout, sout.success("auto-fix: fix pushed"))
		case res.Unfixable:
			fmt.Fprintln(os.Stdout, sout.warn("auto-fix: unfixable (no push)"))
		case res.NoChange:
			fmt.Fprintln(os.Stdout, sout.warn("auto-fix: no change produced (no push)"))
		}
	}
	return true, err
}

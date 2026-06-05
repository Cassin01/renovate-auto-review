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

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: %s [flags] <owner/name>\n\nflags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	cfgPath := flag.String("config", "", "path to config.toml (default: $XDG_CONFIG_HOME/renovate-auto-review/config.toml)")
	dryRun := flag.Bool("dry-run", false, "list target Renovate PRs but do not invoke claude")
	flag.Parse()

	repo, err := parseRepoArg(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
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
			fmt.Fprintf(os.Stderr, "error: %s\n\nplease create %s with the following template:\n\n%s\n",
				err, nf.Path, sampleConfig)
			os.Exit(1)
		}
		log.Fatalf("load config: %v", err)
	}

	if err := checkOwnerAllowed(repo, cfg.AllowedOwners); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := runAll(cfg, []string{repo}, *dryRun); err != nil {
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

func runAll(cfg *Config, repos []string, dryRun bool) error {
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

	fmt.Fprintf(os.Stdout, "config: %s\nrepos: %d, limit: %d\n",
		cfg.SourcePath, len(repos), cfg.Limit)

	var totalProcessed, totalSkipped, totalFailed int
	for _, repo := range repos {
		fmt.Fprintf(os.Stdout, "\n###### %s ######\n", repo)
		p, s, f := processRepo(claudeBin, repo, defaultMarker, cfg, dryRun)
		totalProcessed += p
		totalSkipped += s
		totalFailed += f
	}

	fmt.Fprintf(os.Stdout, "\n=== TOTAL: processed=%d / skipped=%d / errors=%d ===\n",
		totalProcessed, totalSkipped, totalFailed)
	if totalFailed > 0 {
		os.Exit(1)
	}
	return nil
}

func processRepo(claudeBin, repo, marker string, cfg *Config, dryRun bool) (processed, skipped, failed int) {
	prs, err := listOpenPRs(repo, cfg.Limit)
	if err != nil {
		log.Printf("%s: list PRs: %v", repo, err)
		return 0, 0, 1
	}

	var targets []PR
	for _, pr := range prs {
		if isRenovatePR(pr) {
			targets = append(targets, pr)
		}
	}

	fmt.Fprintf(os.Stdout, "Open PRs: %d, Renovate PRs: %d\n", len(prs), len(targets))
	for _, pr := range targets {
		fmt.Fprintf(os.Stdout, "  - #%d  mergeable=%-9s  %s\n", pr.Number, pr.Mergeable, pr.Title)
	}

	if dryRun {
		fmt.Fprintln(os.Stdout, "[dry-run] claude is not invoked.")
		return 0, 0, 0
	}

	for _, pr := range targets {
		fmt.Fprintf(os.Stdout, "\n=== %s PR #%d: %s ===\n", repo, pr.Number, pr.Title)

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

		switch {
		case ciFailing:
			fmt.Fprintln(os.Stdout, "review skip: CI is failing")
		case shouldSkipReview(comments, commits, marker):
			fmt.Fprintln(os.Stdout, "review skip: marker present and no new commits since")
		default:
			fmt.Fprintln(os.Stdout, "--- claude output ---")
			err := runClaudeAutoMode(claudeBin, repo, pr.Number, marker, cfg.CustomPrompt, os.Stdout)
			fmt.Fprintln(os.Stdout, "--- end ---")
			if err != nil {
				log.Printf("%s #%d: %v", repo, pr.Number, err)
				failed++
			} else {
				actionRan = true
			}
		}

		if ciFailing && (cfg.AutoFixCI || hasAutoFixLabel(pr, cfg.AutoFixLabel)) {
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
	fmt.Fprintf(os.Stdout, "lockfile-fix: ecosystem=%s workDir=%s files=%v\n", eco.name, workDir, files)

	res, runErr := runLockfileFix(defaultCommandRunner, repo, marker, workDir, pr, eco)
	if res != nil && res.VerifyOutput != "" {
		fmt.Fprintln(os.Stdout, "--- lockfile verify output ---")
		fmt.Fprintln(os.Stdout, res.VerifyOutput)
		fmt.Fprintln(os.Stdout, "--- end ---")
	}
	switch {
	case runErr != nil:
		// Fall back to the Claude path so a richer reasoning loop has a
		// chance to address the underlying breakage.
		return false, false, runErr
	case res.NoDiff:
		// A no-op regen means the CI failure is not a lockfile-drift
		// problem. Delegate to Claude.
		fmt.Fprintln(os.Stdout, "lockfile-fix: regen produced no diff; falling through to Claude")
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
	for _, id := range runIDs {
		log, err := fetchFailedRunLog(repo, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: fetch run %s log: %v\n", id, err)
			continue
		}
		fmt.Fprintf(&logs, "===== run %s =====\n%s\n", id, log)
	}
	if logs.Len() == 0 {
		fmt.Fprintln(os.Stdout, "auto-fix: failing checks present but no Actions log available (e.g. renovate-side status). Skipping.")
		return false, nil
	}

	workDir, err := os.MkdirTemp(cfg.WorkDir, fmt.Sprintf("rar-%d-", pr.Number))
	if err != nil {
		return false, fmt.Errorf("mkdir workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	fmt.Fprintf(os.Stdout, "auto-fix: workDir=%s, failing checks=%d\n", workDir, len(runIDs))

	verifier := func(wd, lg string, env []string, w io.Writer) (verifyVerdict, error) {
		return runClaudeVerify(claudeBin, repo, pr.Number, wd, lg, cfg.CustomPrompt, env, w)
	}
	publishRunner := newStrippedRunner(buildPublishEnv(os.Environ()))

	fmt.Fprintln(os.Stdout, "--- claude verify output ---")
	res, err := runCIFix(publishRunner, verifier, repo, marker, workDir, pr, logs.String(), buildVerifyEnv(os.Environ()), os.Stdout)
	fmt.Fprintln(os.Stdout, "--- end ---")
	if res != nil {
		switch {
		case res.Pushed:
			fmt.Fprintln(os.Stdout, "auto-fix: fix pushed")
		case res.Unfixable:
			fmt.Fprintln(os.Stdout, "auto-fix: unfixable (no push)")
		case res.NoChange:
			fmt.Fprintln(os.Stdout, "auto-fix: no change produced (no push)")
		}
	}
	return true, err
}

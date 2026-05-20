package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
)

const defaultMarker = "<!-- claude-auto-mergeable-check -->"

func main() {
	cfgPath := flag.String("config", "", "path to config.toml (default: $XDG_CONFIG_HOME/renovate-auto-review/config.toml)")
	dryRun := flag.Bool("dry-run", false, "list target Renovate PRs but do not invoke claude")
	flag.Parse()

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
		var nf *configNotFoundError
		if errors.As(err, &nf) {
			fmt.Fprintf(os.Stderr, "error: %s\n\nplease create %s with the following template:\n\n%s\n",
				err, nf.Path, sampleConfig)
			os.Exit(1)
		}
		log.Fatalf("load config: %v", err)
	}

	if err := runAll(cfg, *dryRun); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func runAll(cfg *Config, dryRun bool) error {
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
		cfg.SourcePath, len(cfg.Repos), cfg.Limit)

	var totalProcessed, totalSkipped, totalFailed int
	for _, repo := range cfg.Repos {
		fmt.Fprintf(os.Stdout, "\n###### %s ######\n", repo)
		p, s, f := processRepo(claudeBin, repo, defaultMarker, cfg.Limit, dryRun)
		totalProcessed += p
		totalSkipped += s
		totalFailed += f
	}

	fmt.Fprintf(os.Stdout, "\n=== TOTAL: 処理=%d 件 / スキップ=%d 件 / エラー=%d 件 ===\n",
		totalProcessed, totalSkipped, totalFailed)
	if totalFailed > 0 {
		os.Exit(1)
	}
	return nil
}

func processRepo(claudeBin, repo, marker string, limit int, dryRun bool) (processed, skipped, failed int) {
	prs, err := listOpenPRs(repo, limit)
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
		if shouldSkipReview(comments, commits, marker) {
			fmt.Fprintln(os.Stdout, "skip: marker present and no new commits since")
			skipped++
			continue
		}

		res, err := runClaudeAutoMode(claudeBin, repo, pr.Number, marker)
		if res != nil && res.Stdout != "" {
			fmt.Fprintln(os.Stdout, "--- claude output ---")
			fmt.Fprintln(os.Stdout, res.Stdout)
			fmt.Fprintln(os.Stdout, "--- end ---")
		}
		if err != nil {
			log.Printf("%s #%d: %v", repo, pr.Number, err)
			failed++
			continue
		}
		processed++
	}
	return processed, skipped, failed
}

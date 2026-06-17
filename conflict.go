package main

import (
	"fmt"
	"strings"
)

// mergeableConflicting is the gh `mergeable` value for a PR whose branch
// conflicts with its base. gh also reports MERGEABLE and UNKNOWN, neither of
// which triggers a rebase request.
const mergeableConflicting = "CONFLICTING"

// shouldRequestRebase reports whether the tool should hand a conflicting PR back
// to Renovate by attaching the rebase label. It fires only when the feature is
// enabled, the PR is CONFLICTING, the rebase label is not already present (a
// pending request Renovate has not acted on yet), and the marker/commit state
// shows no rebase was already requested since the last commit.
func shouldRequestRebase(pr PR, cfg *Config, comments []Comment, commits []Commit, marker string) bool {
	if !cfg.AutoRebaseConflicts {
		return false
	}
	if pr.Mergeable != mergeableConflicting {
		return false
	}
	if prHasLabel(pr, cfg.RebaseLabel) {
		return false
	}
	return !shouldSkipReview(comments, commits, marker)
}

// requestRebase attaches the rebase label and posts a marker comment recording
// the request so subsequent runs stay idempotent.
func requestRebase(repo, marker string, cfg *Config, pr PR) error {
	if err := addPRLabel(repo, pr.Number, cfg.RebaseLabel); err != nil {
		return err
	}
	body := buildRebaseRequestCommentBody(marker, cfg.RebaseLabel)
	return postCommentViaFile(defaultCommandRunner, cfg.WorkDir, repo, pr.Number, body)
}

func buildRebaseRequestCommentBody(marker, rebaseLabel string) string {
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n## Renovate rebase requested\n")
	fmt.Fprintf(&b, "- This PR conflicts with its base branch. The `%s` label was attached so Renovate rebases/regenerates the branch and resolves the conflict.\n", rebaseLabel)
	b.WriteString(commentFooter)
	return b.String()
}

package main

import (
	"fmt"
	"strings"
)

// mergeableConflicting is the gh `mergeable` value for a PR whose branch
// conflicts with its base. gh also reports MERGEABLE and UNKNOWN, neither of
// which triggers a rebase request.
const mergeableConflicting = "CONFLICTING"

// rebaseRequestMarker is the idempotency key for rebase requests. It is
// deliberately distinct from the review marker: a conflict is produced by the
// base branch moving, not by a new PR commit, so gating on the review marker —
// which review and auto-fix comments also carry — suppressed the request for
// any PR the tool had already commented on.
const rebaseRequestMarker = "<!-- claude-auto-rebase-request -->"

// shouldRequestRebase reports whether the tool should hand a conflicting PR back
// to Renovate by attaching the rebase label. It fires only when the feature is
// enabled, the PR is CONFLICTING, the rebase label is not already present (a
// pending request Renovate has not acted on yet), and no rebase request of our
// own is still awaiting a new commit on the branch.
func shouldRequestRebase(pr PR, cfg *Config, comments []Comment, commits []Commit) bool {
	if !cfg.AutoRebaseConflicts {
		return false
	}
	if pr.Mergeable != mergeableConflicting {
		return false
	}
	if prHasLabel(pr, cfg.RebaseLabel) {
		return false
	}
	// A prior request with no branch commit since it means Renovate has not
	// acted yet (or refused); re-requesting would only duplicate the comment.
	// Once Renovate rebases, the new head commit reopens the gate.
	return !shouldSkipReview(comments, commits, rebaseRequestMarker)
}

// requestRebase attaches the rebase label and posts a marker comment recording
// the request so subsequent runs stay idempotent.
func requestRebase(repo string, cfg *Config, pr PR) error {
	if err := addPRLabel(repo, pr.Number, cfg.RebaseLabel); err != nil {
		return err
	}
	body := buildRebaseRequestCommentBody(cfg.RebaseLabel)
	return postCommentViaFile(defaultCommandRunner, cfg.WorkDir, repo, pr.Number, body)
}

func buildRebaseRequestCommentBody(rebaseLabel string) string {
	var b strings.Builder
	b.WriteString(rebaseRequestMarker)
	b.WriteString("\n## Renovate rebase requested\n")
	fmt.Fprintf(&b, "- This PR conflicts with its base branch. The `%s` label was attached so Renovate rebases/regenerates the branch and resolves the conflict.\n", rebaseLabel)
	b.WriteString(commentFooter)
	return b.String()
}

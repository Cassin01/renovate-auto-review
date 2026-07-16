package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Author struct {
	IsBot bool   `json:"is_bot"`
	Login string `json:"login"`
}

type Label struct {
	Name string `json:"name"`
}

type PR struct {
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	Author      Author  `json:"author"`
	Labels      []Label `json:"labels"`
	Mergeable   string  `json:"mergeable"`
	HeadRefName string  `json:"headRefName"`
	HeadRefOid  string  `json:"headRefOid"`
	URL         string  `json:"url"`
}

type Comment struct {
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type Commit struct {
	CommittedDate time.Time `json:"committedDate"`
}

const renovateLogin = "app/renovate"
const renovateLabel = "renovate"

func listOpenPRs(repo string, limit int) ([]PR, error) {
	out, err := exec.Command(
		"gh", "pr", "list",
		"--repo", repo,
		"--state", "open",
		"--json", "number,title,author,labels,mergeable,headRefName,headRefOid,url",
		"--limit", strconv.Itoa(limit),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w (stderr: %s)", err, stderrOf(err))
	}
	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("decode pr list: %w", err)
	}
	return prs, nil
}

// fetchPR fetches a single PR by number, returning the same fields as
// listOpenPRs. Used when the caller targets one specific PR via -pr, bypassing
// the cfg.Limit page size that bounds listOpenPRs.
func fetchPR(repo string, prNumber int) (PR, error) {
	out, err := exec.Command(
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--repo", repo,
		"--json", "number,title,author,labels,mergeable,headRefName,headRefOid,url",
	).Output()
	if err != nil {
		return PR{}, fmt.Errorf("gh pr view %d: %w (stderr: %s)", prNumber, err, stderrOf(err))
	}
	return decodePR(out)
}

func decodePR(raw []byte) (PR, error) {
	var pr PR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return PR{}, fmt.Errorf("decode pr view: %w", err)
	}
	return pr, nil
}

func isRenovatePR(pr PR) bool {
	if !pr.Author.IsBot || pr.Author.Login != renovateLogin {
		return false
	}
	for _, l := range pr.Labels {
		if l.Name == renovateLabel {
			return true
		}
	}
	return false
}

func fetchPRReviewState(repo string, prNumber int) ([]Comment, []Commit, error) {
	out, err := exec.Command(
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--repo", repo,
		"--json", "comments,commits",
	).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("gh pr view comments,commits: %w (stderr: %s)", err, stderrOf(err))
	}
	var data struct {
		Comments []Comment `json:"comments"`
		Commits  []Commit  `json:"commits"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, nil, fmt.Errorf("decode pr view: %w", err)
	}
	return data.Comments, data.Commits, nil
}

// shouldSkipReview reports whether the PR can be skipped: a marker-bearing
// comment exists and no commit has been added after it.
func shouldSkipReview(comments []Comment, commits []Commit, marker string) bool {
	var lastMarkerAt time.Time
	found := false
	for _, c := range comments {
		if strings.Contains(c.Body, marker) && c.CreatedAt.After(lastMarkerAt) {
			lastMarkerAt = c.CreatedAt
			found = true
		}
	}
	if !found {
		return false
	}
	var lastCommitAt time.Time
	for _, c := range commits {
		if c.CommittedDate.After(lastCommitAt) {
			lastCommitAt = c.CommittedDate
		}
	}
	return !lastCommitAt.After(lastMarkerAt)
}

func fetchPRFiles(repo string, prNumber int) ([]string, error) {
	out, err := exec.Command(
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--repo", repo,
		"--json", "files",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view files: %w (stderr: %s)", err, stderrOf(err))
	}
	return decodePRFiles(out)
}

func decodePRFiles(raw []byte) ([]string, error) {
	var data struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode pr view files: %w", err)
	}
	paths := make([]string, 0, len(data.Files))
	for _, f := range data.Files {
		paths = append(paths, f.Path)
	}
	return paths, nil
}

type CheckRun struct {
	Name         string `json:"name"`
	Conclusion   string `json:"conclusion"`
	Status       string `json:"status"`
	DetailsURL   string `json:"detailsUrl"`
	WorkflowName string `json:"workflowName"`
}

func fetchPRChecks(repo string, prNumber int) ([]CheckRun, error) {
	out, err := exec.Command(
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--repo", repo,
		"--json", "statusCheckRollup",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view statusCheckRollup: %w (stderr: %s)", err, stderrOf(err))
	}
	var data struct {
		StatusCheckRollup []CheckRun `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("decode statusCheckRollup: %w", err)
	}
	return data.StatusCheckRollup, nil
}

// hasFailingChecks reports whether any completed check ended in a non-success
// state. Pending or in-progress runs are ignored.
func hasFailingChecks(checks []CheckRun) bool {
	for _, c := range checks {
		if c.Status != "" && c.Status != "COMPLETED" {
			continue
		}
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return true
		}
	}
	return false
}

var runIDRegexp = regexp.MustCompile(`/actions/runs/(\d+)`)

// pickFailedRunIDs returns deduplicated GitHub Actions run IDs extracted from
// DetailsURL of failed checks. Non-Actions URLs (e.g. Renovate status contexts
// with empty targetUrl) are ignored.
func pickFailedRunIDs(checks []CheckRun) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, c := range checks {
		if c.Status != "" && c.Status != "COMPLETED" {
			continue
		}
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		default:
			continue
		}
		m := runIDRegexp.FindStringSubmatch(c.DetailsURL)
		if len(m) < 2 {
			continue
		}
		if _, ok := seen[m[1]]; ok {
			continue
		}
		seen[m[1]] = struct{}{}
		ids = append(ids, m[1])
	}
	return ids
}

func hasAutoFixLabel(pr PR, label string) bool {
	return prHasLabel(pr, label)
}

// prHasLabel reports whether pr carries the given label. An empty name never
// matches.
func prHasLabel(pr PR, name string) bool {
	if name == "" {
		return false
	}
	for _, l := range pr.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// addPRLabel attaches a label to a PR. It is the orchestrator-driven trigger
// for Renovate's rebase (Claude's allowedTools still excludes gh pr edit).
func addPRLabel(repo string, prNumber int, label string) error {
	out, err := exec.Command(
		"gh", "pr", "edit", strconv.Itoa(prNumber),
		"--repo", repo,
		"--add-label", label,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr edit --add-label: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RunState is the current status/conclusion of a GitHub Actions run, fetched
// fresh at fix time to re-validate the possibly-stale statusCheckRollup.
// HeadSha is the commit the run executed against; it is compared with the PR's
// current head to reject a superseded run whose failure a later push may have
// already fixed.
type RunState struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSha    string `json:"head_sha"`
}

// fetchRunState returns the run's current status and conclusion. It uses the
// REST API directly rather than `gh run view`, which resolves the run's
// workflow and returns HTTP 404 when that workflow file has since been deleted.
func fetchRunState(repo, runID string) (RunState, error) {
	out, err := exec.Command(
		"gh", "api",
		fmt.Sprintf("repos/%s/actions/runs/%s", repo, runID),
	).Output()
	if err != nil {
		return RunState{}, fmt.Errorf("gh api run %s: %w (stderr: %s)", runID, err, stderrOf(err))
	}
	var s RunState
	if err := json.Unmarshal(out, &s); err != nil {
		return RunState{}, fmt.Errorf("decode run state: %w", err)
	}
	return s, nil
}

// isGenuineCIFailure reports whether a run's current conclusion represents a
// real, fixable failure — excluding cancellations, stale/superseded runs, and
// successes from a later re-run attempt. Intentionally narrower than the
// rollup-level classifier (hasFailingChecks), which keeps CANCELLED for the
// conservative review-skip gate.
func isGenuineCIFailure(conclusion string) bool {
	switch strings.ToUpper(conclusion) {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	}
	return false
}

// runIsForHead reports whether a failed run should still be trusted as the PR's
// current failure. The statusCheckRollup can lag behind a just-pushed commit
// and surface a run from an earlier commit whose failure a later push (e.g. a
// prior auto-fix) has already resolved. Reproducing such a superseded run wastes
// a fix cycle and yields a misleading "nochange". When both SHAs are known and
// differ, the run is superseded and not actionable. If either SHA is unknown,
// the comparison is inconclusive and the run is not blocked on this basis.
func runIsForHead(runHeadSha, prHeadOid string) bool {
	if runHeadSha == "" || prHeadOid == "" {
		return true
	}
	return runHeadSha == prHeadOid
}

// shortSHA abbreviates a commit SHA for log messages, leaving shorter or empty
// values untouched.
func shortSHA(sha string) string {
	if len(sha) <= 9 {
		return sha
	}
	return sha[:9]
}

const maxFailedLogBytes = 50_000

func fetchFailedRunLog(repo, runID string) (string, error) {
	out, err := exec.Command(
		"gh", "run", "view", runID,
		"--repo", repo,
		"--log-failed",
	).Output()
	if err != nil {
		if partialFailedLog(out, err) {
			return tailBytes(string(out), maxFailedLogBytes), nil
		}
		return "", fmt.Errorf("gh run view %s --log-failed: %w (stderr: %s)", runID, err, stderrOf(err))
	}
	return tailBytes(string(out), maxFailedLogBytes), nil
}

// partialFailedLog reports whether a non-zero `gh run view --log-failed` still
// produced usable logs. When a failed job's log is missing from the run archive
// (a known gap on self-hosted runners) gh exits non-zero with "log not found:
// <jobID>" yet still streams the other failed jobs' logs to stdout. Those
// remain a valid basis for auto-fix, so they are salvaged rather than discarded.
func partialFailedLog(stdout []byte, err error) bool {
	return len(bytes.TrimSpace(stdout)) > 0 && strings.Contains(stderrOf(err), "log not found")
}

func tailBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "... (truncated) ...\n" + s[len(s)-max:]
}

func stderrOf(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

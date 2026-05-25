package main

import (
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
		"--json", "number,title,author,labels,mergeable,headRefName,url",
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
	if label == "" {
		return false
	}
	for _, l := range pr.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
}

const maxFailedLogBytes = 50_000

func fetchFailedRunLog(repo, runID string) (string, error) {
	out, err := exec.Command(
		"gh", "run", "view", runID,
		"--repo", repo,
		"--log-failed",
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh run view %s --log-failed: %w (stderr: %s)", runID, err, stderrOf(err))
	}
	return tailBytes(string(out), maxFailedLogBytes), nil
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

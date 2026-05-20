package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
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

func stderrOf(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

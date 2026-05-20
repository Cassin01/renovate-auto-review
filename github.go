package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

func hasExistingClaudeComment(repo string, prNumber int, marker string) (bool, error) {
	out, err := exec.Command(
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--repo", repo,
		"--json", "comments",
		"--jq", ".comments[].body",
	).Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view comments: %w (stderr: %s)", err, stderrOf(err))
	}
	return strings.Contains(string(out), marker), nil
}

func stderrOf(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDetectLockfileEcosystem(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string // ecosystem name, "" when none
	}{
		{"empty", nil, ""},
		{"go full set", []string{"go.mod", "go.sum"}, "go"},
		{"go sum only", []string{"go.sum"}, "go"},
		{"go mod only", []string{"go.mod"}, "go"},
		{"go with source change rejected", []string{"go.mod", "go.sum", "main.go"}, ""},
		{"go in subdir rejected (multi-module out of scope)", []string{"api/go.mod", "api/go.sum"}, ""},
		{"unknown ecosystem rejected", []string{"Cargo.toml", "Cargo.lock"}, ""},
		{"mixed ecosystems rejected", []string{"go.mod", "package.json"}, ""},
		{"go with doc change rejected", []string{"go.mod", "go.sum", "README.md"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectLockfileEcosystem(tc.files)
			gotName := ""
			if got != nil {
				gotName = got.name
			}
			if gotName != tc.want {
				t.Errorf("detectLockfileEcosystem(%v): want %q, got %q", tc.files, tc.want, gotName)
			}
		})
	}
}

func TestBuildLockfileCommentBody(t *testing.T) {
	const marker = "<!-- m -->"
	eco := &lockfileEcosystem{
		name:      "go",
		regenCmd:  [][]string{{"go", "mod", "tidy"}},
		verifyCmd: [][]string{{"go", "build", "./..."}, {"go", "test", "./..."}},
	}

	body := buildLockfileCommentBody(marker, eco, true, "")
	for _, want := range []string{
		marker,
		"## Auto-fix (lockfile)",
		"✅ fix pushed",
		"go",
		"go mod tidy",
		"go build ./...",
		"go test ./...",
		"github.com/Cassin01/renovate-auto-review",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("success body missing %q\nbody:\n%s", want, body)
		}
	}

	fail := buildLockfileCommentBody(marker, eco, false, "tests failed: package foo: undefined Bar")
	for _, want := range []string{
		marker,
		"❌ verification failed",
		"tests failed: package foo: undefined Bar",
	} {
		if !strings.Contains(fail, want) {
			t.Errorf("failure body missing %q\nbody:\n%s", want, fail)
		}
	}
	if strings.Contains(fail, "✅") {
		t.Errorf("failure body should not contain ✅")
	}
}

// fakeRunner records calls and returns scripted responses.
type fakeRunner struct {
	calls   []runnerCall
	respond func(call runnerCall) ([]byte, error)
}

type runnerCall struct {
	dir  string
	name string
	args []string
}

func (c runnerCall) String() string {
	return fmt.Sprintf("%s %s", c.name, strings.Join(c.args, " "))
}

func (f *fakeRunner) run(dir, name string, args ...string) ([]byte, error) {
	call := runnerCall{dir: dir, name: name, args: append([]string(nil), args...)}
	f.calls = append(f.calls, call)
	if f.respond == nil {
		return nil, nil
	}
	return f.respond(call)
}

// argsMatch returns true when call.args starts with the given prefix.
func argsMatch(call runnerCall, name string, argPrefix ...string) bool {
	if call.name != name {
		return false
	}
	if len(call.args) < len(argPrefix) {
		return false
	}
	for i, p := range argPrefix {
		if call.args[i] != p {
			return false
		}
	}
	return true
}

// happyPathResponder is the default scripted runner for a successful
// lockfile fix. Individual tests can wrap it to inject failures.
func happyPathResponder(call runnerCall) ([]byte, error) {
	switch {
	case argsMatch(call, "gh", "repo", "clone"):
		return nil, nil
	case argsMatch(call, "git", "fetch"):
		return nil, nil
	case argsMatch(call, "git", "checkout"):
		return nil, nil
	case argsMatch(call, "go", "mod", "tidy"):
		return nil, nil
	case argsMatch(call, "git", "status", "--porcelain"):
		return []byte(" M go.sum\n"), nil
	case argsMatch(call, "go", "build"):
		return nil, nil
	case argsMatch(call, "go", "test"):
		return nil, nil
	case argsMatch(call, "git", "add"):
		return nil, nil
	case argsMatch(call, "git", "commit"):
		return nil, nil
	case argsMatch(call, "git", "push"):
		return nil, nil
	case argsMatch(call, "gh", "pr", "comment"):
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected call: %s", call)
}

func TestTryFixLockfileHappyPath(t *testing.T) {
	fr := &fakeRunner{respond: happyPathResponder}
	pr := PR{Number: 42, HeadRefName: "renovate/foo"}
	eco := goEcosystem()
	res, err := runLockfileFix(fr.run, "owner/repo", "<!-- m -->", t.TempDir(), pr, eco)
	if err != nil {
		t.Fatalf("runLockfileFix: %v", err)
	}
	if !res.Pushed {
		t.Errorf("expected Pushed=true")
	}

	// Verify the call order includes the critical steps in the right sequence.
	wantSequence := [][]string{
		{"gh", "repo", "clone"},
		{"git", "fetch"},
		{"git", "checkout"},
		{"go", "mod", "tidy"},
		{"git", "status", "--porcelain"},
		{"go", "build"},
		{"go", "test"},
		{"git", "add"},
		{"git", "commit"},
		{"git", "push"},
		{"gh", "pr", "comment"},
	}
	cursor := 0
	for _, want := range wantSequence {
		found := false
		for ; cursor < len(fr.calls); cursor++ {
			if argsMatch(fr.calls[cursor], want[0], want[1:]...) {
				found = true
				cursor++
				break
			}
		}
		if !found {
			t.Errorf("expected call %v not found at/after cursor; calls=%v", want, fr.calls)
		}
	}
}

func TestTryFixLockfileNoDiffAfterRegen(t *testing.T) {
	// If `go mod tidy` produces no change, the CI failure is not a lockfile
	// drift issue. The fix path must NOT push and must report ran=false so
	// the caller can fall through to the Claude-based path.
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "git", "status", "--porcelain") {
			return []byte(""), nil
		}
		return happyPathResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 1, HeadRefName: "renovate/x"}
	res, err := runLockfileFix(fr.run, "o/r", "<!-- m -->", t.TempDir(), pr, goEcosystem())
	if err != nil {
		t.Fatalf("runLockfileFix: %v", err)
	}
	if res.Pushed {
		t.Errorf("expected Pushed=false when regen produced no diff")
	}
	if res.NoDiff != true {
		t.Errorf("expected NoDiff=true")
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") {
			t.Errorf("must not push when no diff: %v", c)
		}
		if argsMatch(c, "git", "commit") {
			t.Errorf("must not commit when no diff: %v", c)
		}
	}
}

func TestTryFixLockfileVerifyFailureDoesNotPush(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "go", "build") {
			return []byte("./main.go:10: undefined: foo\n"), errors.New("exit status 1")
		}
		return happyPathResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 9, HeadRefName: "renovate/y"}
	res, err := runLockfileFix(fr.run, "o/r", "<!-- m -->", t.TempDir(), pr, goEcosystem())
	if err == nil {
		t.Fatalf("expected verify error, got nil; res=%+v", res)
	}
	if res.Pushed {
		t.Errorf("must not push when verify fails")
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") {
			t.Errorf("must not push when verify fails: %v", c)
		}
	}
	if res.VerifyOutput == "" {
		t.Errorf("expected VerifyOutput to capture failure stderr")
	}
}

func TestTryFixLockfileCloneFailureAborts(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "gh", "repo", "clone") {
			return []byte("clone failed"), errors.New("exit status 1")
		}
		return happyPathResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 1, HeadRefName: "renovate/x"}
	res, err := runLockfileFix(fr.run, "o/r", "<!-- m -->", t.TempDir(), pr, goEcosystem())
	if err == nil {
		t.Fatalf("expected clone error, got nil; res=%+v", res)
	}
	// Only the clone attempt and nothing past it.
	for _, c := range fr.calls {
		if argsMatch(c, "go", "mod", "tidy") || argsMatch(c, "git", "push") {
			t.Errorf("must not progress past clone failure: %v", c)
		}
	}
}

func TestRunLockfileFixCommitMessageHasMarker(t *testing.T) {
	var commitMsg string
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "git", "commit", "-m") && len(call.args) >= 3 {
			commitMsg = call.args[2]
		}
		return happyPathResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 42, HeadRefName: "renovate/foo"}
	_, err := runLockfileFix(fr.run, "o/r", "<!-- m -->", t.TempDir(), pr, goEcosystem())
	if err != nil {
		t.Fatalf("runLockfileFix: %v", err)
	}
	for _, want := range []string{"fix(deps)", "[auto-lockfile]"} {
		if !strings.Contains(commitMsg, want) {
			t.Errorf("commit message %q missing %q", commitMsg, want)
		}
	}
}

func TestGoEcosystemShape(t *testing.T) {
	// Guard against accidental removal of test command in verify, which is
	// the primary defence against "lockfile pushed but tests fail in CI".
	eco := goEcosystem()
	if eco.name != "go" {
		t.Errorf("name = %q, want %q", eco.name, "go")
	}
	if !reflect.DeepEqual(eco.regenCmd, [][]string{{"go", "mod", "tidy"}}) {
		t.Errorf("regenCmd = %v", eco.regenCmd)
	}
	foundTest := false
	for _, cmd := range eco.verifyCmd {
		if len(cmd) >= 2 && cmd[0] == "go" && cmd[1] == "test" {
			foundTest = true
		}
	}
	if !foundTest {
		t.Errorf("verifyCmd must include `go test`; got %v", eco.verifyCmd)
	}
}

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func envNames(kvs []string) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[envName(kv)] = envValue(kv)
	}
	return m
}

func TestBuildVerifyEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"ANTHROPIC_API_KEY=sk-xxx",
		"GH_TOKEN=gho_xxx",
		"LC_ALL=C",
		"NODE_AUTH_TOKEN=tok1",
		"NPM_TOKEN=tok2",
		"NPM_CONFIG_//npm.pkg.github.com/:_authToken=tok3",
		"NPM_CONFIG_REGISTRY=https://registry.example",
		"MY_RANDOM_SECRET=nope",
		"AWS_SESSION_TOKEN=bedrock",
		"GOPATH=/home/u/go/1.25.4",
		"GOTOOLCHAIN=auto",
		"GOMODCACHE=/home/u/go/1.25.4/pkg/mod",
		"GOCACHE=/home/u/.cache/go-build",
		"GOFLAGS=-mod=mod",
	}
	got := envNames(buildVerifyEnv(in))

	for _, name := range []string{
		"PATH", "HOME", "ANTHROPIC_API_KEY", "GH_TOKEN", "LC_ALL",
		"NODE_AUTH_TOKEN", "NPM_TOKEN",
		"NPM_CONFIG_//npm.pkg.github.com/:_authToken", "NPM_CONFIG_REGISTRY",
		"AWS_SESSION_TOKEN",
		"GOPATH", "GOTOOLCHAIN", "GOMODCACHE", "GOCACHE", "GOFLAGS",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("buildVerifyEnv: want %q forwarded, missing", name)
		}
	}
	if _, ok := got["MY_RANDOM_SECRET"]; ok {
		t.Errorf("buildVerifyEnv: unrelated secret MY_RANDOM_SECRET must be dropped")
	}
}

func TestBuildPublishEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"MY_RANDOM_SECRET=keep",
		"NODE_AUTH_TOKEN=tok1",
		"NPM_TOKEN=tok2",
		"NPM_CONFIG_//npm.pkg.github.com/:_authToken=tok3",
	}
	got := envNames(buildPublishEnv(in))

	for _, name := range []string{"PATH", "HOME", "MY_RANDOM_SECRET"} {
		if _, ok := got[name]; !ok {
			t.Errorf("buildPublishEnv: want %q retained, missing", name)
		}
	}
	for _, name := range []string{"NODE_AUTH_TOKEN", "NPM_TOKEN", "NPM_CONFIG_//npm.pkg.github.com/:_authToken"} {
		if _, ok := got[name]; ok {
			t.Errorf("buildPublishEnv: registry token %q must be stripped", name)
		}
	}
}

func TestTrackedModifiedPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single modified", " M src/index.ts\n", []string{"src/index.ts"}},
		{"untracked excluded", "?? .npmrc\n M src/index.ts\n", []string{"src/index.ts"}},
		{"added staged", "A  src/new.ts\n", []string{"src/new.ts"}},
		{"rename", "R  old.ts -> new.ts\n", []string{"new.ts"}},
		{"only untracked", "?? .npmrc\n?? node_modules/\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trackedModifiedPaths(tc.in)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("trackedModifiedPaths(%q): want %v, got %v", tc.in, tc.want, got)
			}
		})
	}
}

func fixedVerifier(verdict verifyVerdict) claudeVerifier {
	return func(workDir, logs string, env []string, out io.Writer) (verifyVerdict, error) {
		return verdict, nil
	}
}

// okReverifyRunner is a re-verification runner whose every command succeeds.
func okReverifyRunner() *fakeRunner {
	return &fakeRunner{respond: func(call runnerCall) ([]byte, error) {
		return nil, nil
	}}
}

// fixedVerdict is a "fixed" verdict carrying a minimal non-empty
// verified_commands list so it clears the re-verification gate.
func fixedVerdict() verifyVerdict {
	return verifyVerdict{Verdict: "fixed", VerifiedCommands: []string{"npm run lint"}}
}

func ciHappyResponder(call runnerCall) ([]byte, error) {
	switch {
	case argsMatch(call, "gh", "repo", "clone"):
		return nil, nil
	case argsMatch(call, "git", "fetch"):
		return nil, nil
	case argsMatch(call, "git", "checkout"):
		return nil, nil
	case argsMatch(call, "git", "status", "--porcelain"):
		return []byte(" M src/index.ts\n"), nil
	case argsMatch(call, "git", "add"):
		return nil, nil
	case argsMatch(call, "git", "diff", "--cached"):
		return []byte("+const x = 1\n"), nil
	case argsMatch(call, "git", "commit"):
		return nil, nil
	case argsMatch(call, "git", "push"):
		return nil, nil
	case argsMatch(call, "gh", "pr", "comment"):
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected call: %s", call)
}

func TestRunCIFixHappyPath(t *testing.T) {
	fr := &fakeRunner{respond: ciHappyResponder}
	rv := okReverifyRunner()
	pr := PR{Number: 42, HeadRefName: "renovate/foo"}
	res, err := runCIFix(fr.run, rv.run, fixedVerifier(verifyVerdict{Verdict: "fixed", Summary: "rename dup mutation", VerifiedCommands: []string{"npm run lint", "npm run build"}}),
		"owner/repo", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err != nil {
		t.Fatalf("runCIFix: %v", err)
	}
	if !res.Pushed {
		t.Errorf("expected Pushed=true, got %+v", res)
	}

	// Every reported command must be independently re-run before publishing.
	for _, want := range []string{"npm run lint", "npm run build"} {
		if !hasCall(rv.calls, "sh", "-c", want) {
			t.Errorf("re-verification must run %q via sh -c; calls=%v", want, rv.calls)
		}
	}

	wantSequence := [][]string{
		{"gh", "repo", "clone"},
		{"git", "fetch"},
		{"git", "checkout"},
		{"git", "status", "--porcelain"},
		{"git", "add"},
		{"git", "diff", "--cached"},
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

	// The publish runner must never invoke a package manager (token surface).
	for _, c := range fr.calls {
		for _, pm := range []string{"npm", "pnpm", "yarn", "mise"} {
			if c.name == pm {
				t.Errorf("publish runner must not run %q: %v", pm, c)
			}
		}
	}
}

func TestRunCIFixStagesTrackedOnly(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "git", "status", "--porcelain") {
			return []byte("?? .npmrc\n M src/index.ts\n"), nil
		}
		return ciHappyResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 1, HeadRefName: "renovate/x"}
	_, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(fixedVerdict()),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err != nil {
		t.Fatalf("runCIFix: %v", err)
	}
	var addCall *runnerCall
	for i := range fr.calls {
		if argsMatch(fr.calls[i], "git", "add") {
			addCall = &fr.calls[i]
			break
		}
	}
	if addCall == nil {
		t.Fatalf("no git add call recorded")
	}
	joined := strings.Join(addCall.args, " ")
	if !strings.Contains(joined, "src/index.ts") {
		t.Errorf("git add must stage tracked path src/index.ts; got %q", joined)
	}
	if strings.Contains(joined, ".npmrc") {
		t.Errorf("git add must NOT stage untracked .npmrc; got %q", joined)
	}
}

func TestRunCIFixSecretInDiffAborts(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "git", "diff", "--cached") {
			return []byte("+//npm.pkg.github.com/:_authToken=LEAKEDVALUE\n"), nil
		}
		return ciHappyResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 9, HeadRefName: "renovate/y"}
	res, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(fixedVerdict()),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected abort error, got nil; res=%+v", res)
	}
	if res.Pushed {
		t.Errorf("must not push when a secret is detected")
	}
	var commentBody string
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") {
			t.Errorf("must not push when a secret is detected: %v", c)
		}
		if argsMatch(c, "git", "commit") {
			t.Errorf("must not commit when a secret is detected: %v", c)
		}
	}
	// An abort comment is posted; its body lives in a temp file, but the error
	// must never echo the leaked value.
	if strings.Contains(err.Error(), "LEAKEDVALUE") {
		t.Errorf("error must not contain the secret value: %v", err)
	}
	_ = commentBody
}

func TestRunCIFixUnfixableNoPush(t *testing.T) {
	fr := &fakeRunner{respond: ciHappyResponder}
	pr := PR{Number: 3, HeadRefName: "renovate/z"}
	res, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(verifyVerdict{Verdict: "unfixable", Summary: "major bump"}),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err != nil {
		t.Fatalf("runCIFix: %v", err)
	}
	if !res.Unfixable {
		t.Errorf("expected Unfixable=true, got %+v", res)
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") || argsMatch(c, "git", "commit") || argsMatch(c, "git", "add") {
			t.Errorf("unfixable must not publish: %v", c)
		}
	}
	if !hasCall(fr.calls, "gh", "pr", "comment") {
		t.Errorf("unfixable must still post a comment")
	}
}

func TestRunCIFixNoChangeNoPush(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "git", "status", "--porcelain") {
			return []byte(""), nil
		}
		return ciHappyResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 4, HeadRefName: "renovate/w"}
	res, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(fixedVerdict()),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err != nil {
		t.Fatalf("runCIFix: %v", err)
	}
	if res.Pushed {
		t.Errorf("must not push when verify produced no tracked change")
	}
	if !res.NoChange {
		t.Errorf("expected NoChange=true, got %+v", res)
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") {
			t.Errorf("must not push on no-change: %v", c)
		}
	}
}

func TestRunCIFixCloneFailureAborts(t *testing.T) {
	respond := func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "gh", "repo", "clone") {
			return []byte("clone failed"), errors.New("exit status 1")
		}
		return ciHappyResponder(call)
	}
	fr := &fakeRunner{respond: respond}
	pr := PR{Number: 1, HeadRefName: "renovate/x"}
	_, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(fixedVerdict()),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected clone error")
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") {
			t.Errorf("must not progress past clone failure: %v", c)
		}
	}
}

func TestRunCIFixForwardsScopedEnvToVerify(t *testing.T) {
	var capturedEnv []string
	verify := func(workDir, logs string, env []string, out io.Writer) (verifyVerdict, error) {
		capturedEnv = env
		return verifyVerdict{Verdict: "nochange"}, nil
	}
	fr := &fakeRunner{respond: ciHappyResponder}
	pr := PR{Number: 7, HeadRefName: "renovate/foo"}
	scoped := buildVerifyEnv([]string{"PATH=/x", "NODE_AUTH_TOKEN=tok"})
	_, err := runCIFix(fr.run, okReverifyRunner().run, verify, "o/r", "<!-- m -->", t.TempDir(), pr, "logs", scoped, io.Discard)
	if err != nil {
		t.Fatalf("runCIFix: %v", err)
	}
	if envNames(capturedEnv)["NODE_AUTH_TOKEN"] != "tok" {
		t.Errorf("verify did not receive the scoped env with NODE_AUTH_TOKEN; got %v", capturedEnv)
	}
}

func TestBuildCIFixCommentBody(t *testing.T) {
	const marker = "<!-- m -->"
	fixed := buildCIFixCommentBody(marker, verifyVerdict{Verdict: "fixed", Summary: "renamed dup mutation", FailingChecks: []string{"lint_frontend"}})
	for _, want := range []string{marker, "## Claude auto-fix", "✅ fix pushed", "lint_frontend", "renamed dup mutation", "github.com/Cassin01/renovate-auto-review"} {
		if !strings.Contains(fixed, want) {
			t.Errorf("fixed body missing %q\n%s", want, fixed)
		}
	}
	un := buildCIFixCommentBody(marker, verifyVerdict{Verdict: "unfixable"})
	if !strings.Contains(un, "❌ unfixable") {
		t.Errorf("unfixable body missing verdict\n%s", un)
	}
	if strings.Contains(un, "✅") {
		t.Errorf("unfixable body must not contain ✅")
	}
}

func TestRunCIFixReverifyFailureNoPush(t *testing.T) {
	fr := &fakeRunner{respond: ciHappyResponder}
	rv := &fakeRunner{respond: func(call runnerCall) ([]byte, error) {
		if argsMatch(call, "sh", "-c", "npm run vue-tsc") {
			return []byte("error TS2589"), errors.New("exit status 2")
		}
		return nil, nil
	}}
	pr := PR{Number: 50, HeadRefName: "renovate/x"}
	res, err := runCIFix(fr.run, rv.run, fixedVerifier(verifyVerdict{Verdict: "fixed", VerifiedCommands: []string{"npm run vue-tsc"}}),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected re-verification abort error, got nil; res=%+v", res)
	}
	if res.Pushed {
		t.Errorf("must not push when re-verification fails")
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") || argsMatch(c, "git", "commit") || argsMatch(c, "git", "add") {
			t.Errorf("must not publish when re-verification fails: %v", c)
		}
	}
	if !hasCall(fr.calls, "gh", "pr", "comment") {
		t.Errorf("re-verification failure must post a comment")
	}
}

func TestRunCIFixFixedWithoutVerifiedCommandsAborts(t *testing.T) {
	fr := &fakeRunner{respond: ciHappyResponder}
	pr := PR{Number: 51, HeadRefName: "renovate/x"}
	res, err := runCIFix(fr.run, okReverifyRunner().run, fixedVerifier(verifyVerdict{Verdict: "fixed"}),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected abort when fixed verdict carries no verified_commands")
	}
	if res.Pushed {
		t.Errorf("must not push a fixed verdict without verified_commands")
	}
	for _, c := range fr.calls {
		if argsMatch(c, "git", "push") || argsMatch(c, "git", "commit") {
			t.Errorf("must not publish without verified_commands: %v", c)
		}
	}
}

func TestRunCIFixReverifyRejectsPublishVerb(t *testing.T) {
	fr := &fakeRunner{respond: ciHappyResponder}
	rv := okReverifyRunner()
	pr := PR{Number: 52, HeadRefName: "renovate/x"}
	res, err := runCIFix(fr.run, rv.run, fixedVerifier(verifyVerdict{Verdict: "fixed", VerifiedCommands: []string{"git push origin HEAD"}}),
		"o/r", "<!-- m -->", t.TempDir(), pr, "logs", nil, io.Discard)
	if err == nil {
		t.Fatalf("expected abort when verified_commands contains a publish verb")
	}
	if res.Pushed {
		t.Errorf("must not push")
	}
	// The forbidden command must be rejected before any execution.
	for _, c := range rv.calls {
		if argsMatch(c, "sh", "-c", "git push origin HEAD") {
			t.Errorf("forbidden publish verb must not be executed: %v", c)
		}
	}
}

func hasCall(calls []runnerCall, name string, argPrefix ...string) bool {
	for _, c := range calls {
		if argsMatch(c, name, argPrefix...) {
			return true
		}
	}
	return false
}

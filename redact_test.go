package main

import (
	"strings"
	"testing"
)

func TestRedactorLinePatterns(t *testing.T) {
	r := &redactor{}
	cases := []struct {
		name   string
		in     string
		secret string // substring that must be gone
	}{
		{"github classic", "token ghp_ABCDEFGHIJKLMNOPQRSTUVWX here", "ghp_ABCDEFGHIJKLMNOPQRSTUVWX"},
		{"github oauth", "x gho_ABCDEFGHIJKLMNOPQRSTUVWX y", "gho_ABCDEFGHIJKLMNOPQRSTUVWX"},
		{"github pat", "github_pat_11ABCDEFGHIJKLMNOPQRST z", "github_pat_11ABCDEFGHIJKLMNOPQRST"},
		{"npm authtoken url", "//npm.pkg.github.com/:_authToken=abc123XYZ", "abc123XYZ"},
		{"npm authtoken bare", "_authToken=plainSecretValue", "plainSecretValue"},
		{"npm token", "npm_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "npm_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"},
		{"bearer", "Authorization: Bearer abc.def-ghijkl", "abc.def-ghijkl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.line(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Errorf("RedactorLine: secret %q still present in %q", tc.secret, got)
			}
			if !strings.Contains(got, redactionPlaceholder) {
				t.Errorf("RedactorLine: want %q in output, got %q", redactionPlaceholder, got)
			}
		})
	}
}

func TestRedactorLineEnvLiteral(t *testing.T) {
	t.Setenv("NODE_AUTH_TOKEN", "s3cr3t-literal-value")
	r := newRedactor()
	got := r.line("installing with token s3cr3t-literal-value now")
	if strings.Contains(got, "s3cr3t-literal-value") {
		t.Errorf("RedactorLine: env literal still present in %q", got)
	}
	if !strings.Contains(got, redactionPlaceholder) {
		t.Errorf("RedactorLine: want %q in output, got %q", redactionPlaceholder, got)
	}
}

func TestRedactorLineNonSecretAndSentinels(t *testing.T) {
	r := &redactor{}
	for _, s := range []string{
		"$ npm ci",
		"← ok example 0.12s",
		"PUSHED",
		"POSTED",
		"VERIFY_DONE",
		"bump foo to 1.2.3",
	} {
		if got := r.line(s); got != s {
			t.Errorf("RedactorLine: non-secret %q altered to %q", s, got)
		}
	}
}

func TestRedactorLineNil(t *testing.T) {
	var r *redactor
	in := "token ghp_ABCDEFGHIJKLMNOPQRSTUVWX"
	if got := r.line(in); strings.Contains(got, "ghp_ABCDEFGHIJKLMNOPQRSTUVWX") {
		t.Errorf("RedactorLine(nil): pattern redaction must still apply, got %q", got)
	}
}

func TestScanForSecrets(t *testing.T) {
	r := &redactor{}
	if hits := scanForSecrets(r, "clean diff line\n+const x = 1\n"); len(hits) != 0 {
		t.Errorf("ScanForSecrets: clean input should yield no hits, got %v", hits)
	}
	hits := scanForSecrets(r, "+ //registry/:_authToken=LEAKED")
	if len(hits) == 0 {
		t.Errorf("ScanForSecrets: expected a hit for leaked authToken")
	}
	for _, h := range hits {
		if strings.Contains(h, "LEAKED") {
			t.Errorf("ScanForSecrets: hit names must not contain the secret value, got %q", h)
		}
	}
}

package main

import (
	"os"
	"regexp"
	"strings"
)

const redactionPlaceholder = "[REDACTED]"

type secretPattern struct {
	name string
	re   *regexp.Regexp
}

// secretPatterns match credential-shaped substrings independently of any known
// env value. Used both for stdout redaction and the pre-publish secret scan.
var secretPatterns = []secretPattern{
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"github-pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"npm-authtoken-url", regexp.MustCompile(`//[^\s]*:_authToken=\S+`)},
	{"npm-authtoken", regexp.MustCompile(`(?i)_authToken=\S+`)},
	{"npm-token", regexp.MustCompile(`npm_[A-Za-z0-9]{20,}`)},
	{"bearer", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9.\-]{8,}`)},
}

// redactor scrubs secrets from text before it is printed. literals are the
// runtime values of known registry-token env vars, read ephemerally and held
// only in memory for the process lifetime — never persisted.
type redactor struct {
	literals []string
}

// isTokenValueEnv reports whether a registry env var's value is itself a
// credential (vs. a non-secret like NPM_CONFIG_REGISTRY, which is a URL).
func isTokenValueEnv(name string) bool {
	if !isRegistryTokenEnv(name) {
		return false
	}
	u := strings.ToUpper(name)
	return strings.Contains(u, "TOKEN") ||
		strings.Contains(u, "AUTH") ||
		strings.Contains(u, "PASSWORD") ||
		strings.Contains(u, "SECRET")
}

func newRedactor() *redactor {
	r := &redactor{}
	for _, kv := range os.Environ() {
		if !isTokenValueEnv(envName(kv)) {
			continue
		}
		if v := envValue(kv); v != "" {
			r.literals = append(r.literals, v)
		}
	}
	return r
}

// line returns s with every known literal and credential-shaped substring
// replaced by the placeholder. Nil-safe so callers may pass a zero redactor.
func (r *redactor) line(s string) string {
	if r != nil {
		for _, lit := range r.literals {
			s = strings.ReplaceAll(s, lit, redactionPlaceholder)
		}
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, redactionPlaceholder)
	}
	return s
}

// scanForSecrets returns the names of the literals/patterns that match s, never
// the matched values. A non-empty result aborts a publish.
func scanForSecrets(r *redactor, s string) []string {
	var hits []string
	if r != nil {
		for _, lit := range r.literals {
			if lit != "" && strings.Contains(s, lit) {
				hits = append(hits, "registry-token-env-value")
				break
			}
		}
	}
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			hits = append(hits, p.name)
		}
	}
	return hits
}

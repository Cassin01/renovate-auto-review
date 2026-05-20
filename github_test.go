package main

import (
	"errors"
	"os/exec"
	"testing"
)

func TestIsRenovatePR(t *testing.T) {
	cases := []struct {
		name string
		pr   PR
		want bool
	}{
		{
			name: "bot + renovate label",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: true,
		},
		{
			name: "bot + multiple labels including renovate",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: "deps"}, {Name: renovateLabel}, {Name: "security"}},
			},
			want: true,
		},
		{
			name: "bot but no renovate label",
			pr: PR{
				Author: Author{IsBot: true, Login: renovateLogin},
				Labels: []Label{{Name: "other"}},
			},
			want: false,
		},
		{
			name: "renovate label but not bot",
			pr: PR{
				Author: Author{IsBot: false, Login: renovateLogin},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: false,
		},
		{
			name: "bot but wrong login",
			pr: PR{
				Author: Author{IsBot: true, Login: "app/dependabot"},
				Labels: []Label{{Name: renovateLabel}},
			},
			want: false,
		},
		{
			name: "zero value",
			pr:   PR{},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRenovatePR(tc.pr); got != tc.want {
				t.Errorf("isRenovatePR() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStderrOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"generic error", errors.New("boom"), ""},
		{"exit error with stderr", &exec.ExitError{Stderr: []byte("  oops\n")}, "oops"},
		{"exit error with empty stderr", &exec.ExitError{Stderr: nil}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stderrOf(tc.err); got != tc.want {
				t.Errorf("stderrOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

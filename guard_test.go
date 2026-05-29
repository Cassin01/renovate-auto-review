package main

import (
	"errors"
	"testing"
)

func TestCheckOwnerAllowed(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		allowed []string
		wantErr bool
	}{
		{
			name:    "empty allowlist permits any owner",
			repo:    "someone/else",
			allowed: nil,
			wantErr: false,
		},
		{
			name:    "owner present in allowlist",
			repo:    "Cassin01/renovate-auto-review",
			allowed: []string{"Cassin01", "my-org"},
			wantErr: false,
		},
		{
			name:    "owner absent from allowlist",
			repo:    "someone-else/repo",
			allowed: []string{"Cassin01", "my-org"},
			wantErr: true,
		},
		{
			name:    "owner match is case-insensitive",
			repo:    "cassin01/repo",
			allowed: []string{"Cassin01"},
			wantErr: false,
		},
		{
			name:    "allowlist entry casing ignored",
			repo:    "MY-ORG/repo",
			allowed: []string{"my-org"},
			wantErr: false,
		},
		{
			name:    "blank allowlist entries are ignored",
			repo:    "someone/repo",
			allowed: []string{"", "  "},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkOwnerAllowed(tt.repo, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkOwnerAllowed(%q, %v): want err=%v, got %v", tt.repo, tt.allowed, tt.wantErr, err)
			}
		})
	}
}

func TestCheckOwnerAllowedErrorType(t *testing.T) {
	err := checkOwnerAllowed("intruder/repo", []string{"Cassin01"})
	var nae *ownerNotAllowedError
	if !errors.As(err, &nae) {
		t.Fatalf("checkOwnerAllowed: want *ownerNotAllowedError, got %T (%v)", err, err)
	}
	if nae.Owner != "intruder" {
		t.Errorf("ownerNotAllowedError.Owner: want %q, got %q", "intruder", nae.Owner)
	}
}

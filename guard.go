package main

import (
	"fmt"
	"strings"
)

// ownerNotAllowedError is returned when the target repository's owner is not
// present in the configured allowlist.
type ownerNotAllowedError struct {
	Owner   string
	Allowed []string
}

func (e *ownerNotAllowedError) Error() string {
	return fmt.Sprintf("owner %q is not in allowed_owners %v", e.Owner, e.Allowed)
}

// checkOwnerAllowed reports whether the owner part of repo ("owner/name") is
// permitted by the allowlist. An empty allowlist (after dropping blank entries)
// disables the guard and permits any owner. Matching is case-insensitive.
func checkOwnerAllowed(repo string, allowed []string) error {
	owners := make([]string, 0, len(allowed))
	for _, a := range allowed {
		if a = strings.TrimSpace(a); a != "" {
			owners = append(owners, a)
		}
	}
	if len(owners) == 0 {
		return nil
	}
	owner, _, _ := strings.Cut(repo, "/")
	for _, a := range owners {
		if strings.EqualFold(owner, a) {
			return nil
		}
	}
	return &ownerNotAllowedError{Owner: owner, Allowed: owners}
}

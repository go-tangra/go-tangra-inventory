//go:build !windows

package collector

import "github.com/go-tangra/go-tangra-inventory/v4/internal/store"

// collectCurrentUser (token-based) is a Windows-only extra.
func collectCurrentUser() (store.UserAccount, bool) { return store.UserAccount{}, false }

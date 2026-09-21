//go:build !windows

package collector

import "github.com/go-freya/freya/services/inventory/internal/store"

// collectCurrentUser (token-based) is a Windows-only extra.
func collectCurrentUser() (store.UserAccount, bool) { return store.UserAccount{}, false }

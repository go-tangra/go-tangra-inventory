//go:build windows

package collector

import (
	"github.com/go-freya/freya/services/inventory/internal/store"
	"golang.org/x/sys/windows"
)

// collectCurrentUser resolves the account behind the process token. It reports
// ok false when the token cannot be read.
func collectCurrentUser() (store.UserAccount, bool) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return store.UserAccount{}, false
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return store.UserAccount{}, false
	}
	account, _, _, err := user.User.Sid.LookupAccount("")
	if err != nil || account == "" {
		return store.UserAccount{}, false
	}
	return store.UserAccount{Name: account}, true
}

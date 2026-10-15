// Package docs embeds the inventory's help pages (docs/help). The service serves
// them to the gateway as one bundle (helpdocs.BundlePath), which renders and
// indexes them for the portal's help center. Authoring guide: go-tangra
// docs/help-docs.md; CI lints the tree with tangra-docs.
package docs

import (
	"embed"
	"io/fs"
)

//go:embed all:help
var files embed.FS

// Help is the docs/help tree.
func Help() fs.FS {
	sub, err := fs.Sub(files, "help")
	if err != nil {
		panic(err)
	}
	return sub
}

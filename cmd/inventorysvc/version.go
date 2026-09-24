package main

import (
	"fmt"
	"os"
)

// version is stamped at build time (-ldflags "-X main.version=..."); "dev" for
// local builds. `<binary> version` prints it and exits.
var version = "dev"

func init() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		os.Exit(0)
	}
}

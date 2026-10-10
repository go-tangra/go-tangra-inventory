package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentmodules"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

func TestModuleWriter(t *testing.T) {
	c := config.DefaultAgent().Modules
	c.Enabled = false
	if moduleWriter(c, agentmodules.Deps{}) != nil {
		t.Fatal("disabled: writer opened")
	}
	base := t.TempDir()
	c.Enabled = true
	c.Directory = filepath.Join(base, "modules")
	c.Owner, c.Group = strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	w := moduleWriter(c, agentmodules.Deps{RootUID: os.Getuid()})
	if runtime.GOOS != "linux" {
		if w != nil {
			t.Fatal("writer on an unsupported platform")
		}
		return
	}
	if w == nil {
		t.Fatal("linux: no writer")
	}
	// Not owned by the expected root uid: refused at recovery.
	if moduleWriter(c, agentmodules.Deps{RootUID: os.Getuid() + 1}) != nil {
		t.Fatal("unsafe directory accepted")
	}
	file := filepath.Join(base, "file")
	_ = os.WriteFile(file, nil, 0o600)
	c.Directory = filepath.Join(file, "modules")
	if moduleWriter(c, agentmodules.Deps{}) != nil {
		t.Fatal("unusable directory accepted")
	}
}

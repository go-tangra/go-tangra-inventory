package main

import (
	"os"
	"runtime"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

func TestTeleportJoiner(t *testing.T) {
	c := config.DefaultAgent().Teleport
	j := teleportJoiner(c)
	_, sysErr := os.Stat("/usr/bin/systemctl")
	_, sysErr2 := os.Stat("/bin/systemctl")
	want := runtime.GOOS == "linux" && (sysErr == nil || sysErr2 == nil)
	if (j != nil) != want {
		t.Fatalf("joiner = %v, want present %v", j, want)
	}
	c.Enabled = false
	if (teleportJoiner(c) != nil) != want {
		t.Fatal("disabled config drops the installer")
	}
}

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

func TestCertificateStore(t *testing.T) {
	c := config.DefaultAgent().Certificates
	c.Enabled = false
	if certificateStore(c, agentcerts.Deps{}) != nil {
		t.Fatal("disabled: store opened")
	}
	base := t.TempDir()
	c.Enabled = true
	c.Directory = filepath.Join(base, "certs")
	c.Owner, c.Group = strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	st := certificateStore(c, agentcerts.Deps{RootUID: os.Getuid()})
	if runtime.GOOS != "linux" {
		if st != nil {
			t.Fatal("store on an unsupported platform")
		}
		return
	}
	if st == nil {
		t.Fatal("linux: no store")
	}
	// Not owned by the expected root uid: refused at recovery.
	if certificateStore(c, agentcerts.Deps{RootUID: os.Getuid() + 1}) != nil {
		t.Fatal("unsafe store accepted")
	}
	// Unusable directory (a file).
	file := filepath.Join(base, "file")
	_ = os.WriteFile(file, nil, 0o600)
	c.Directory = filepath.Join(file, "certs")
	if certificateStore(c, agentcerts.Deps{}) != nil {
		t.Fatal("unusable directory accepted")
	}
}

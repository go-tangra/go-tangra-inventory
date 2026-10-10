//go:build linux

package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentmodules"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

// bundleClient serves one real zip bundle.
type bundleClient struct {
	fakeModuleClient
	data []byte
}

func (b *bundleClient) Fetch(_ context.Context, id string) (*invv1.ModuleBundleHeader, []byte, error) {
	sum := sha256.Sum256(b.data)
	return &invv1.ModuleBundleHeader{ItemId: id, Module: "sms-gw", Version: "1.2.0", Size: int64(len(b.data)),
		Sha256: hex.EncodeToString(sum[:])}, append([]byte(nil), b.data...), nil
}

// TestModuleDeliveryOnDisk (US1 independent test, agent half): a MODULE
// command ends with the tree under <dir>/sms-gw (.env 0600) and an
// installed report; a second delivery of the module is already_installed.
func TestModuleDeliveryOnDisk(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"sms-gw/compose.yaml": "services: {}\n", "sms-gw/.env": "JOIN_TOKEN=t\n"} {
		fw, _ := zw.Create(name)
		_, _ = fw.Write([]byte(body))
	}
	_ = zw.Close()
	dir := filepath.Join(t.TempDir(), "modules")
	mc := config.DefaultAgent().Modules
	mc.Directory, mc.Owner, mc.Group = dir, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	w, err := agentmodules.NewOS(agentmodules.ConfigFrom(mc), agentmodules.Deps{RootUID: os.Getuid(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := moduleDaemon(nil)
	cl := &bundleClient{data: buf.Bytes()}
	d.WithModules(w)
	d.newModuleClient = func(string, string) ModuleClient { return cl }
	d.handleCommand(context.Background(), moduleCmd("item-1", 1))
	drainModules(d)
	if _, reps := cl.snapshot(); len(reps) != 1 || reps[0].GetState() != "installed" {
		t.Fatalf("reports = %v", reps)
	}
	fi, err := os.Stat(filepath.Join(dir, "sms-gw", ".env"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env: %v %v", fi, err)
	}
	d.handleCommand(context.Background(), moduleCmd("item-2", 1))
	drainModules(d)
	if _, reps := cl.snapshot(); len(reps) != 2 || reps[1].GetReason() != "already_installed" {
		t.Fatalf("second = %v", reps)
	}
}

//go:build linux

package collector

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

// physicalDisks reads the block devices from sysfs.
func physicalDisks(ctx context.Context) (agentfacts.BlockDevices, agentfacts.DiskResolver) {
	src := agentfacts.TreeSource{FS: os.DirFS("/"), DevPath: func(dev string) (string, error) {
		return filepath.EvalSymlinks(filepath.Join("/sys/block", filepath.Base(dev)))
	}}
	return linuxDisks(ctx, src, diskBudget)
}

// linuxDisks enumerates the disks of src within budget and builds the
// filesystem resolver over the same tree.
func linuxDisks(ctx context.Context, src agentfacts.TreeSource, budget time.Duration) (agentfacts.BlockDevices, agentfacts.DiskResolver) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	res := agentfacts.ReadBlockDevices(ctx, src)
	names := make([]string, 0, len(res.Disks))
	for _, d := range res.Disks {
		names = append(names, d.Name)
	}
	return res, agentfacts.NewDiskResolver(src, names)
}

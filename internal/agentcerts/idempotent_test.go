package agentcerts

import (
	"io/fs"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestUnchangedWritesNothing: the same bundle again is "unchanged" with zero
// filesystem writes and no hook (FR-016).
func TestUnchangedWritesNothing(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Hook = "/usr/local/sbin/reload" })
	b := h.ca.bundle(t, 0xa1)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	runs := h.hooks.runCount()
	h.fs.resetWrites()
	res := h.mustInstall(b.request(h.ca, "www"), store.DeliveryUnchanged)
	if h.fs.writeCount() != 0 || h.hooks.runCount() != runs || res.HookExitCode != HookNotRun || res.Fingerprint == "" || res.Serial != "a1" {
		t.Fatalf("unchanged: %+v writes=%d runs=%d", res, h.fs.writeCount(), h.hooks.runCount())
	}
}

// TestTamperedFilesAreReinstalled: content changes or missing files lead to
// a new generation (and the hook).
func TestTamperedFilesAreReinstalled(t *testing.T) {
	for _, c := range []struct {
		name   string
		tamper func(h *harness, gen string)
	}{
		{"cert content", func(h *harness, gen string) { h.fs.node(gen + "/" + fileCert).data = []byte("x") }},
		{"chain missing", func(h *harness, gen string) { delete(h.fs.nodes, gen+"/"+fileChain) }},
		{"fullchain is a symlink", func(h *harness, gen string) {
			h.fs.nodes[gen+"/"+fileFullChain] = &memNode{mode: fs.ModeSymlink, target: "/etc/passwd"}
		}},
		{"key missing", func(h *harness, gen string) { delete(h.fs.nodes, gen+"/"+fileKey) }},
		{"metadata missing", func(h *harness, _ string) { delete(h.fs.nodes, "renewal/www.json") }},
		{"metadata of another generation", func(h *harness, _ string) {
			m := h.st.readMeta("www")
			m.Generation = "20000101T000000Z-1"
			_ = h.st.writeMeta(*m, 0, 33)
		}},
		{"metadata of another name", func(h *harness, _ string) {
			h.fs.node("renewal/www.json").data = []byte(`{"name":"api"}`)
		}},
		{"metadata garbage", func(h *harness, _ string) { h.fs.node("renewal/www.json").data = []byte("{") }},
	} {
		h := newHarness(t, func(c *Config) { c.Hook = "/usr/local/sbin/reload" })
		b := h.ca.bundle(t, 0xa1)
		h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
		old := h.fs.liveGen(t, "www")
		c.tamper(h, genPath("www", old))
		runs := h.hooks.runCount()
		h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
		if h.fs.liveGen(t, "www") == old || h.hooks.runCount() != runs+1 {
			t.Fatalf("%s: not reinstalled", c.name)
		}
	}
}

// TestDriftFixedInPlace: owner/mode drift alone is corrected without a new
// generation or hook, and reported unchanged.
func TestDriftFixedInPlace(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Hook = "/usr/local/sbin/reload" })
	b := h.ca.bundle(t, 0xa1)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	gen := genPath("www", h.fs.liveGen(t, "www"))
	h.fs.node(gen + "/" + fileKey).mode = 0o644
	h.fs.node(gen + "/" + fileCert).gid = 0
	h.fs.node(gen).mode = fs.ModeDir | 0o755
	h.fs.node("renewal/www.json").uid = 5
	h.fs.node("archive").gid = 0
	runs := h.hooks.runCount()
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryUnchanged)
	if h.fs.node(gen+"/"+fileKey).mode != 0o640 || h.fs.node(gen+"/"+fileCert).gid != 33 || h.fs.node(gen).mode.Perm() != 0o750 ||
		h.fs.node("renewal/www.json").uid != 0 || h.fs.node("archive").gid != 33 || h.hooks.runCount() != runs {
		t.Fatal("drift not fixed in place")
	}
	if genPath("www", h.fs.liveGen(t, "www")) != gen {
		t.Fatal("new generation for drift")
	}
}

// TestUnchangedCertificateOnly: a key-less bundle is unchanged when the
// carried key still matches, and reinstalled when it is gone or foreign.
func TestUnchangedCertificateOnly(t *testing.T) {
	h := newHarness(t, nil)
	a := h.ca.bundle(t, 0xa1)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	req := a.request(h.ca, "www")
	req.KeyPEM, req.HasKey = nil, false
	h.mustInstall(req, store.DeliveryUnchanged)

	gen := genPath("www", h.fs.liveGen(t, "www"))
	_, foreign := newKey(t)
	h.fs.node(gen + "/" + fileKey).data = foreign
	// Foreign key in place: reinstall refuses to carry it over.
	if res := h.install(req); res.Reason != store.ReasonKeyMismatch {
		t.Fatalf("foreign key: %+v", res)
	}
	delete(h.fs.nodes, gen+"/"+fileKey)
	h.mustInstall(req, store.DeliveryInstalled)
	if h.st.readMeta("www").HasKey || h.fs.liveFile(t, "www", fileKey) != nil {
		t.Fatal("key appeared")
	}
	// A key file that appears in a key-less generation means "not intact".
	gen = genPath("www", h.fs.liveGen(t, "www"))
	h.fs.nodes[gen+"/"+fileKey] = &memNode{mode: 0o600, data: a.key}
	h.mustInstall(req, store.DeliveryInstalled)
}

// TestUnchangedDriftFailures: a failing correction is a write failure.
func TestUnchangedDriftFailures(t *testing.T) {
	for _, key := range []string{"Lchown GEN", "Chmod GEN/privkey.pem", "Lchown renewal/www.json"} {
		h := newHarness(t, nil)
		b := h.ca.bundle(t, 0xa1)
		h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
		gen := genPath("www", h.fs.liveGen(t, "www"))
		h.fs.node(gen).gid = 0
		h.fs.node(gen + "/" + fileKey).mode = 0o600
		h.fs.node("renewal/www.json").uid = 9
		h.fs.setFail(replaceGen(key, gen), errBoom)
		if res := h.install(b.request(h.ca, "www")); res.Reason != store.ReasonWriteFailed || res.Detail != "check installed files" {
			t.Fatalf("%s: %+v", key, res)
		}
	}
}

func replaceGen(key, gen string) string {
	out := ""
	for i := 0; i < len(key); i++ {
		if i+3 <= len(key) && key[i:i+3] == "GEN" {
			out += gen
			i += 2
			continue
		}
		out += string(key[i])
	}
	return out
}

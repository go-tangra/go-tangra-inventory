package agentteleport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testVersion = "17.4.2"
	testToken   = "s3cr3t-join-t0ken"
	testUUID    = "0b6b5f4e-9a3c-4d1e-8f2a-1c2d3e4f5a6b"
	testPin     = "sha256:" + "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
)

func material() Material {
	return Material{ItemID: "item-1", Token: []byte(testToken), ProxyAddr: "teleport.example.com:443", CAPins: []string{testPin},
		Version: testVersion, Labels: map[string]string{"tangra.io/os": "Debian 12", "tangra.io/host-id": "h-1"}}
}

type entry struct {
	name string
	typ  byte
	data string
}

func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o755, Size: int64(len(e.data))}
		if e.typ != tar.TypeReg {
			h.Size, h.Linkname = 0, "/etc/shadow"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return b.Bytes()
}

func goodArchive(t *testing.T) []byte {
	return tarGz(t, entry{"teleport/README.md", tar.TypeReg, "readme"}, entry{"teleport/tctl", tar.TypeReg, "tctl"},
		entry{"teleport/teleport", tar.TypeReg, "#!teleport-binary"})
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeHTTP serves the archive and its checksum file by URL.
type fakeHTTP struct {
	mu     sync.Mutex
	files  map[string]resp
	urls   []string
	onFile func(url string)
}

type resp struct {
	status int
	body   io.Reader
	err    error
}

func (f *fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.urls = append(f.urls, r.URL.String())
	x, ok := f.files[r.URL.String()]
	hook := f.onFile
	f.mu.Unlock()
	if hook != nil {
		hook(r.URL.String())
	}
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if x.err != nil {
		return nil, x.err
	}
	return &http.Response{StatusCode: x.status, Body: io.NopCloser(x.body)}, nil
}

// errReader fails after its data.
type errReader struct{ data []byte }

func (e *errReader) Read(p []byte) (int, error) {
	if len(e.data) == 0 {
		return 0, errors.New("connection reset")
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}

// fakeSys answers systemctl calls; fail maps "verb unit" to an error and
// onCall may change the filesystem.
type fakeSys struct {
	mu     sync.Mutex
	calls  []string
	fail   map[string]bool
	active func() bool
	onCall func(call string)
}

func (f *fakeSys) Run(_ context.Context, args ...string) error {
	call := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fail := f.fail[call]
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall(call)
	}
	if call == "is-active --quiet "+UnitName && f.active != nil {
		if f.active() {
			return nil
		}
		return errors.New("exit status 3")
	}
	if fail {
		return errors.New("exit status 1")
	}
	return nil
}

func (f *fakeSys) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

type kit struct {
	in    *Installer
	root  string
	http  *fakeHTTP
	sys   *fakeSys
	now   time.Time
	slept int
}

const testBase = "https://cdn.test"

// newKit is a host with systemd, nothing of Teleport installed, a CDN
// serving a good archive and a unit that becomes active and writes the
// host uuid on start.
func newKit(t *testing.T) *kit {
	t.Helper()
	root := t.TempDir()
	mkdir(t, root, "run/systemd/system")
	archive := goodArchive(t)
	url := testBase + "/" + archiveName(testVersion, "amd64")
	k := &kit{root: root, now: time.Unix(1_700_000_000, 0)}
	k.http = &fakeHTTP{files: map[string]resp{}}
	k.serve(url, archive, sum(archive)+"  "+archiveName(testVersion, "amd64")+"\n")
	k.sys = &fakeSys{fail: map[string]bool{"is-active --quiet " + UnitName: true, "is-enabled --quiet " + UnitName: true}}
	started := false
	k.sys.onCall = func(call string) {
		if call == "enable --now "+UnitName {
			started = true
			write(t, root, HostUUIDRel, testUUID+"\n")
		}
	}
	k.sys.active = func() bool { return started }
	k.in = New(Deps{Root: root, Arch: "amd64", BaseURL: testBase + "/", HTTP: k.http, Systemctl: k.sys,
		Hostname: func() (string, error) { return "node-1.example.com", nil },
		Now:      func() time.Time { return k.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			k.slept++
			k.now = k.now.Add(d)
			return ctx.Err()
		}}, 15*time.Minute)
	return k
}

func (k *kit) serve(url string, archive []byte, sumFile string) {
	k.http.files[url] = resp{status: 200, body: bytes.NewReader(archive)}
	k.http.files[url+".sha256"] = resp{status: 200, body: strings.NewReader(sumFile)}
}

func (k *kit) join(t *testing.T) Result {
	t.Helper()
	return k.in.Join(context.Background(), material(), func() bool { return true })
}

func mkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, root, rel, data string) {
	t.Helper()
	mkdir(t, root, filepath.Dir(rel))
	if err := os.WriteFile(filepath.Join(root, rel), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noToken fails if the token is anywhere below root or in r.
func noToken(t *testing.T, root string, r Result) {
	t.Helper()
	if strings.Contains(r.Detail, testToken) || strings.Contains(r.Reason, testToken) {
		t.Fatalf("token in result %+v", r)
	}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(testToken)) {
				t.Errorf("token left in %s", p)
			}
		}
		return nil
	})
}

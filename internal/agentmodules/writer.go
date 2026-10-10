package agentmodules

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ErrUnsupported is returned by NewOS on platforms other than Linux; such
// agents never announce module.v1.
var ErrUnsupported = errors.New("agentmodules: module delivery is supported on Linux only")

// errUnsafe reports a modules directory that is not a root-owned directory
// closed to group and others.
var errUnsafe = errors.New("agentmodules: unsafe modules directory")

// Config is the local module configuration (contracts/agent-config.md).
// Nothing in it is ever taken from the server.
type Config struct {
	Dir          string // absolute modules directory
	Owner, Group string // owner and group of written files (names or numeric ids)
	Hook         string // absolute deploy hook path; "" = none
	HookTimeout  time.Duration
}

// ConfigFrom maps the validated agent configuration.
func ConfigFrom(c config.AgentModules) Config {
	return Config{Dir: c.Directory, Owner: c.Owner, Group: c.Group, Hook: c.DeployHook, HookTimeout: c.HookTimeout()}
}

// Deps are the writer's collaborators (the certificate store's filesystem,
// accounts and hook runner). RootUID is the owner the modules directory and
// the hook must have (0; tests use their own uid).
type Deps struct {
	FS       agentcerts.FS
	Accounts agentcerts.Accounts
	Hooks    agentcerts.HookRunner
	RootUID  int
	Rand     io.Reader // staging name randomness (crypto/rand)
	Logf     func(format string, args ...any)
}

// Request is one fetched bundle: the header fields of FetchModuleBundle and
// the bytes. Only Module selects anything on disk; it is validated again.
type Request struct {
	ItemID  string
	Module  string
	Version string
	Size    int64
	SHA256  string
	Data    []byte
}

// Result is what the agent reports (ReportModuleRequest). HookExitCode is
// -1 when no hook ran, 256 on timeout. Detail never holds hook output.
type Result struct {
	State        string
	Reason       string
	HookExitCode int
	Detail       string
}

// Writer writes delivered modules under the configured directory, one at a
// time (including the hook).
type Writer struct {
	cfg   Config
	fs    agentcerts.FS
	acc   agentcerts.Accounts
	hooks agentcerts.HookRunner
	root  int
	rand  io.Reader
	logf  func(format string, args ...any)
	mu    sync.Mutex
}

// New builds a writer over the given dependencies.
func New(cfg Config, d Deps) *Writer {
	w := &Writer{cfg: cfg, fs: d.FS, acc: d.Accounts, hooks: d.Hooks, root: d.RootUID, rand: d.Rand, logf: d.Logf}
	if w.rand == nil {
		w.rand = rand.Reader
	}
	if w.logf == nil {
		w.logf = log.Printf
	}
	return w
}

var (
	moduleRE  = regexp.MustCompile(store.ModuleNameRE)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// hookPath is the fixed PATH of the hook environment (as for certificates).
const hookPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// stagingPrefix starts the name of a module tree being written.
const stagingPrefix = ".staging-"

// failure is a reported failure: a closed reason and a short detail.
type failure struct {
	reason string
	detail string
}

func fail(reason, detail string) *failure { return &failure{reason: reason, detail: detail} }

// ioFailure maps a filesystem error: no space → disk_full, anything else →
// write_failed with the step that failed.
func ioFailure(step string, err error) *failure {
	if errors.Is(err, syscall.ENOSPC) {
		return fail(store.ReasonDiskFull, step)
	}
	return fail(store.ReasonWriteFailed, step)
}

// Install checks the bundle, writes the module tree and runs the local hook.
func (w *Writer) Install(ctx context.Context, req Request) Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	res, f := w.install(ctx, req)
	if f != nil {
		res = Result{State: store.DeliveryFailed, Reason: f.reason, HookExitCode: agentcerts.HookNotRun, Detail: f.detail}
		w.logf("modules: item %s module %q failed: %s (%s)", req.ItemID, req.Module, f.reason, f.detail)
		return res
	}
	w.logf("modules: item %s module %s %s %s (hook exit %d)", req.ItemID, req.Module, req.Version, res.State, res.HookExitCode)
	return res
}

func (w *Writer) install(ctx context.Context, req Request) (Result, *failure) {
	if !moduleRE.MatchString(req.Module) {
		return Result{}, fail(store.ReasonInvalidModule, "")
	}
	if !versionRE.MatchString(req.Version) || len(req.Version) > store.MaxModuleVersionLen {
		return Result{}, fail(store.ReasonInvalidBundle, "invalid version")
	}
	if req.Size > store.MaxModuleBundleBytes || len(req.Data) > store.MaxModuleBundleBytes {
		return Result{}, fail(store.ReasonTooLarge, "bundle over 12 MiB")
	}
	sum := sha256.Sum256(req.Data)
	if int64(len(req.Data)) != req.Size || hex.EncodeToString(sum[:]) != req.SHA256 {
		return Result{}, fail(store.ReasonChecksumMismatch, "")
	}
	entries, f := parseArchive(req.Data, req.Module)
	if f != nil {
		return Result{}, f
	}
	uid, gid, f := w.owner()
	if f != nil {
		return Result{}, f
	}
	if err := w.checkDir(); err != nil {
		return Result{}, ioFailure("unsafe_directory", err)
	}
	if _, err := w.fs.Lstat(req.Module); err == nil {
		return Result{}, fail(store.ReasonAlreadyInstalled, "")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Result{}, ioFailure("inspect module directory", err)
	}
	var unpacked uint64
	for _, e := range entries {
		if e.file != nil {
			unpacked += e.file.UncompressedSize64
		}
	}
	if free, err := w.fs.Free(); err == nil && free < 2*unpacked {
		return Result{}, fail(store.ReasonDiskFull, "insufficient free space")
	}
	staging, err := w.stagingName(req.Module)
	if err != nil {
		return Result{}, ioFailure("staging name", err)
	}
	if f := w.stage(staging, entries, uid, gid); f != nil {
		_ = w.fs.RemoveAll(staging)
		return Result{}, f
	}
	if err := w.fs.Rename(staging, req.Module); err != nil {
		_ = w.fs.RemoveAll(staging)
		if errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.ENOTEMPTY) {
			return Result{}, fail(store.ReasonAlreadyInstalled, "")
		}
		return Result{}, ioFailure("move into place", err)
	}
	if err := w.fs.SyncDir("."); err != nil {
		w.logf("modules: module %s: directory not synced: %v", req.Module, err)
	}
	return w.runHook(ctx, req), nil
}

// owner resolves the configured owner and group.
func (w *Writer) owner() (uid, gid int, f *failure) {
	uid, err := resolveID(w.cfg.Owner, w.acc.LookupUser)
	if err != nil {
		return 0, 0, fail(store.ReasonWriteFailed, "unknown owner")
	}
	gid, err = resolveID(w.cfg.Group, w.acc.LookupGroup)
	if err != nil {
		return 0, 0, fail(store.ReasonWriteFailed, "unknown group")
	}
	return uid, gid, nil
}

// resolveID accepts a numeric id or looks the name up.
func resolveID(v string, lookup func(string) (int, error)) (int, error) {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return n, nil
	}
	return lookup(v)
}

// checkDir requires the modules directory to be a directory owned by root
// and closed to group and others: nobody else may create or rename entries
// in it.
func (w *Writer) checkDir() error {
	fi, err := w.fs.Lstat(".")
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.UID != w.root || fi.Mode.Perm()&0o022 != 0 {
		return errUnsafe
	}
	return nil
}

// stagingName is .staging-<module>-<16 hex>.
func (w *Writer) stagingName(module string) (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(w.rand, b[:]); err != nil {
		return "", err
	}
	return stagingPrefix + module + "-" + hex.EncodeToString(b[:]), nil
}

// stage writes the tree below staging: directories with their mode and
// owner, then every file created exclusively without following links, with
// its mode and owner set before the bytes are written. The extracted bytes
// are zeroed after each write.
func (w *Writer) stage(staging string, entries []entry, uid, gid int) *failure {
	var unpacked int64
	for _, e := range entries {
		rel := path.Join(staging, e.rel)
		if e.dir { // the first entry is the module directory (staging) itself
			if err := w.fs.Mkdir(rel, modePrivateDir); err != nil {
				return ioFailure("create directory", err)
			}
			continue
		}
		data, f := readEntry(e, MaxUnpacked-unpacked)
		if f != nil {
			return f
		}
		unpacked += int64(len(data))
		err := w.fs.WriteFile(rel, data, e.perm, uid, gid)
		clear(data)
		if err != nil {
			return ioFailure("write file", err)
		}
	}
	// Directory modes and owners last (deepest first), the module directory
	// itself at the end: until then the tree is root-only.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !e.dir {
			continue
		}
		rel := path.Join(staging, e.rel)
		if err := w.fs.Chmod(rel, e.perm); err != nil {
			return ioFailure("set directory mode", err)
		}
		if err := w.fs.Lchown(rel, uid, gid); err != nil {
			return ioFailure("set directory owner", err)
		}
	}
	return nil
}

// readEntry extracts one file (at most budget bytes; the zip reader checks
// the size and CRC it declares).
func readEntry(e entry, budget int64) ([]byte, *failure) {
	rc, err := e.file.Open()
	if err != nil {
		return nil, fail(store.ReasonInvalidBundle, "unreadable entry")
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, budget+1))
	if err != nil {
		clear(data)
		return nil, fail(store.ReasonInvalidBundle, "corrupt entry")
	}
	if int64(len(data)) > budget {
		clear(data)
		return nil, fail(store.ReasonTooLarge, "unpacked size over the limit")
	}
	return data, nil
}

// runHook runs the locally configured hook in the module directory with
// PATH, LANG and the MODULE_* variables only; its output stays in the local
// log.
func (w *Writer) runHook(ctx context.Context, req Request) Result {
	res := Result{State: store.DeliveryInstalled, HookExitCode: agentcerts.HookNotRun}
	if w.cfg.Hook == "" {
		return res
	}
	if why := agentcerts.CheckHook(w.hooks, w.cfg.Hook, w.root); why != "" {
		w.logf("modules: module %s: deploy hook %s refused: %s", req.Module, w.cfg.Hook, why)
		return Result{State: store.DeliveryHookFailed, Reason: store.ReasonHookRefused, HookExitCode: agentcerts.HookNotRun, Detail: why}
	}
	dir := path.Join(w.cfg.Dir, req.Module)
	out := w.hooks.Run(ctx, agentcerts.HookSpec{Path: w.cfg.Hook, Dir: dir, Timeout: w.cfg.HookTimeout,
		Env: []string{"PATH=" + hookPath, "LANG=C.UTF-8", "MODULE_NAME=" + req.Module, "MODULE_VERSION=" + req.Version, "MODULE_DIR=" + dir}})
	code, reason, detail := agentcerts.HookVerdict(out, w.cfg.HookTimeout, func(format string, args ...any) {
		w.logf("modules: module %s: "+format, append([]any{req.Module}, args...)...)
	})
	res.HookExitCode = code
	if reason != "" {
		res.State, res.Reason, res.Detail = store.DeliveryHookFailed, reason, detail
	}
	return res
}

// Recover removes module trees left in staging by an interrupted write
// and refuses an unsafe modules directory.
func (w *Writer) Recover() error {
	if err := w.checkDir(); err != nil {
		return err
	}
	names, err := w.fs.ReadDir(".")
	if err != nil {
		return err
	}
	for _, n := range names {
		if strings.HasPrefix(n, stagingPrefix) {
			if err := w.fs.RemoveAll(n); err != nil {
				return err
			}
			w.logf("modules: removed interrupted write %s", n)
		}
	}
	return nil
}

//go:build linux

package collector

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const cmdTimeout = 20 * time.Second

// collectPrograms enumerates installed packages via dpkg (Debian/Ubuntu) and
// falls back to rpm (RHEL/SUSE). Empty when neither tool is available.
func collectPrograms() []store.Program {
	if progs := dpkgPrograms(); len(progs) > 0 {
		return progs
	}
	return rpmPrograms()
}

func dpkgPrograms() []store.Program {
	out, err := runCmd("dpkg-query", "-W", "-f=${Package}\t${Version}\t${Maintainer}\t${Installed-Size}\n")
	if err != nil {
		return nil
	}
	var progs []store.Program
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		p := store.Program{Name: f[0], Version: f[1]}
		if len(f) >= 3 {
			p.Publisher = f[2]
		}
		if len(f) >= 4 {
			if kb, e := strconv.ParseUint(strings.TrimSpace(f[3]), 10, 64); e == nil {
				p.SizeBytes = kb * 1024 // dpkg Installed-Size is in KiB
			}
		}
		progs = append(progs, p)
	}
	return progs
}

func rpmPrograms() []store.Program {
	out, err := runCmd("rpm", "-qa", "--queryformat", "%{NAME}\t%{VERSION}-%{RELEASE}\t%{VENDOR}\t%{SIZE}\n")
	if err != nil {
		return nil
	}
	var progs []store.Program
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		p := store.Program{Name: f[0], Version: f[1]}
		if len(f) >= 3 {
			p.Publisher = f[2]
		}
		if len(f) >= 4 {
			if b, e := strconv.ParseUint(strings.TrimSpace(f[3]), 10, 64); e == nil {
				p.SizeBytes = b
			}
		}
		progs = append(progs, p)
	}
	return progs
}

// collectServices enumerates systemd service units. Empty when systemctl is
// unavailable (e.g. a non-systemd host).
func collectServices() []store.Service {
	out, err := runCmd("systemctl", "list-units", "--type=service", "--all", "--no-legend", "--no-pager", "--plain")
	if err != nil {
		return nil
	}
	var svcs []store.Service
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ".service")
		s := store.Service{
			Name:        name,
			State:       fields[3], // SUB: running/dead/exited/...
			DisplayName: strings.Join(fields[4:], " "),
		}
		if fields[1] != "" { // LOAD column doubles as a coarse start-mode hint
			s.StartMode = fields[1]
		}
		svcs = append(svcs, s)
	}
	return svcs
}

// collectUsers reads local accounts from /etc/passwd, keeping root and regular
// login users (uid 0 or >= 1000). root is flagged as admin.
func collectUsers() []store.UserAccount {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil
	}
	defer f.Close()

	var users []store.UserAccount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if uid != 0 && uid < 1000 {
			continue // system account
		}
		users = append(users, store.UserAccount{
			Name:    fields[0],
			IsAdmin: uid == 0,
		})
	}
	return users
}

func runCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- fixed command names, no user input
	if err != nil {
		return "", err
	}
	return string(out), nil
}

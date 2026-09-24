//go:build windows

package collector

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"golang.org/x/sys/windows/registry"
)

const cmdTimeout = 30 * time.Second

// collectPrograms reads installed applications from the registry uninstall keys,
// covering both native (64-bit) and 32-bit (WOW6432Node) views.
func collectPrograms() []store.Program {
	var progs []store.Program
	roots := []struct {
		hive registry.Key
		path string
		flag uint32
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.WOW64_64KEY},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, registry.WOW64_32KEY},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, 0},
	}
	seen := make(map[string]struct{})
	for _, r := range roots {
		progs = appendUninstallKeys(progs, seen, r.hive, r.path, r.flag)
	}
	return progs
}

func appendUninstallKeys(progs []store.Program, seen map[string]struct{}, hive registry.Key, path string, flag uint32) []store.Program {
	k, err := registry.OpenKey(hive, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|flag)
	if err != nil {
		return progs
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return progs
	}
	for _, name := range names {
		sk, err := registry.OpenKey(hive, path+`\`+name, registry.QUERY_VALUE|flag)
		if err != nil {
			continue
		}
		display, _, _ := sk.GetStringValue("DisplayName")
		if strings.TrimSpace(display) == "" {
			sk.Close()
			continue
		}
		if _, dup := seen[display]; dup {
			sk.Close()
			continue
		}
		seen[display] = struct{}{}

		p := store.Program{Name: display}
		p.Version, _, _ = sk.GetStringValue("DisplayVersion")
		p.Publisher, _, _ = sk.GetStringValue("Publisher")
		p.InstallDate, _, _ = sk.GetStringValue("InstallDate")
		p.InstallLocation, _, _ = sk.GetStringValue("InstallLocation")
		if kb, _, err := sk.GetIntegerValue("EstimatedSize"); err == nil {
			p.SizeBytes = kb * 1024 // EstimatedSize is in KiB
		}
		progs = append(progs, p)
		sk.Close()
	}
	return progs
}

// collectServices enumerates Windows services via `sc query`, mapping the
// SERVICE_NAME and STATE fields.
func collectServices() []store.Service {
	out, err := runCmd("sc", "query", "type=", "service", "state=", "all")
	if err != nil {
		return nil
	}
	var svcs []store.Service
	var cur *store.Service
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "SERVICE_NAME:"):
			if cur != nil {
				svcs = append(svcs, *cur)
			}
			cur = &store.Service{Name: strings.TrimSpace(strings.TrimPrefix(line, "SERVICE_NAME:"))}
		case strings.HasPrefix(line, "DISPLAY_NAME:") && cur != nil:
			cur.DisplayName = strings.TrimSpace(strings.TrimPrefix(line, "DISPLAY_NAME:"))
		case strings.HasPrefix(line, "STATE") && cur != nil:
			cur.State = scStateWord(line)
		}
	}
	if cur != nil {
		svcs = append(svcs, *cur)
	}
	return svcs
}

// scStateWord extracts the textual state (e.g. RUNNING, STOPPED) from an sc
// STATE line like "STATE : 4 RUNNING".
func scStateWord(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// collectUsers enumerates local user accounts via PowerShell Get-LocalUser and
// flags membership of the local Administrators group.
func collectUsers() []store.UserAccount {
	out, err := runCmd("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-LocalUser | ForEach-Object { $_.Name }")
	if err != nil {
		return nil
	}
	admins := localAdminNames()
	var users []store.UserAccount
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" {
			continue
		}
		_, isAdmin := admins[strings.ToLower(name)]
		users = append(users, store.UserAccount{Name: name, IsAdmin: isAdmin})
	}
	return users
}

func localAdminNames() map[string]struct{} {
	admins := make(map[string]struct{})
	out, err := runCmd("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-LocalGroupMember -Group Administrators | ForEach-Object { $_.Name }")
	if err != nil {
		return admins
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name == "" {
			continue
		}
		// Names come back as DOMAIN\User; key on the account part.
		if i := strings.LastIndex(name, `\`); i >= 0 {
			name = name[i+1:]
		}
		admins[strings.ToLower(name)] = struct{}{}
	}
	return admins
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

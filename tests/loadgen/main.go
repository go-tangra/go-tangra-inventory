// Command loadgen drives the inventory ingest edge with synthetic enrolled
// hosts that submit realistic host reports (interfaces with addresses and
// flags, primary addresses, virtualization, BMC, Proxmox guests, pending
// updates). It exists to measure the IPAM host sync end to end (feature 020,
// SC-006: 1000 hosts in one tenant). It needs one single-use enrollment token
// per synthetic host (mint them in the tenant under test), one per line.
//
//	go run ./tests/loadgen -ingest inventory:9977 -ca-file ca.pem -tokens tokens.txt -rounds 3 -change 0.1
//
// It is a test tool: it never runs in the service and holds the issued agent
// credentials in memory only.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func main() {
	var (
		ingest      = flag.String("ingest", "", "ingest endpoint host:port")
		tokens      = flag.String("tokens", "", "file with one enrollment token per line (one synthetic host each)")
		insecure    = flag.Bool("insecure", false, "plaintext ingest (development stack only)")
		caFile      = flag.String("ca-file", "", "PEM CA bundle of the ingest certificate")
		serverName  = flag.String("server-name", "", "name verified in the ingest certificate")
		rounds      = flag.Int("rounds", 1, "submissions per host")
		change      = flag.Float64("change", 0.1, "fraction of hosts whose primary address changes each later round")
		concurrency = flag.Int("concurrency", 16, "parallel agents")
		seed        = flag.Uint64("seed", 20, "generator seed (stable host identities)")
	)
	flag.Parse()
	if *ingest == "" || *tokens == "" || *rounds < 1 || *concurrency < 1 {
		flag.Usage()
		os.Exit(2)
	}
	toks, err := readTokens(*tokens)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
	s := sender.New(*ingest, sender.Options{Insecure: *insecure, CAFile: *caFile, ServerName: *serverName})
	res := run(context.Background(), s, toks, *rounds, *change, *concurrency, *seed)
	fmt.Println(res.summary())
	if res.errors > 0 {
		os.Exit(1)
	}
}

func readTokens(path string) ([]string, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied token file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, t)
		}
	}
	return out, sc.Err()
}

// agentAPI is the part of the sender the generator drives.
type agentAPI interface {
	Enroll(ctx context.Context, token string, ident store.Identity, version string) (string, string, error)
	Submit(ctx context.Context, agentID, credential string, inv store.Inventory) (string, error)
}

type result struct {
	mu        sync.Mutex
	submitted int
	errors    int
	latencies []time.Duration
	elapsed   time.Duration
}

func (r *result) add(d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.errors++
		return
	}
	r.submitted++
	r.latencies = append(r.latencies, d)
}

func (r *result) summary() string {
	sort.Slice(r.latencies, func(i, j int) bool { return r.latencies[i] < r.latencies[j] })
	pct := func(p float64) time.Duration {
		if len(r.latencies) == 0 {
			return 0
		}
		return r.latencies[int(p*float64(len(r.latencies)-1))]
	}
	return fmt.Sprintf("submitted=%d errors=%d elapsed=%s p50=%s p95=%s", r.submitted, r.errors,
		r.elapsed.Round(time.Millisecond), pct(0.5), pct(0.95))
}

func run(ctx context.Context, api agentAPI, tokens []string, rounds int, change float64, concurrency int, seed uint64) *result {
	res := &result{}
	start := time.Now()
	type agent struct{ id, cred string }
	agents := make([]agent, len(tokens))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				inv := Synthesize(i, 0, seed, change)
				id, cred, err := api.Enroll(ctx, tokens[i], inv.Identity, inv.AgentVersion)
				if err != nil {
					res.add(0, err)
					continue
				}
				agents[i] = agent{id, cred}
			}
		}()
	}
	for i := range tokens {
		work <- i
	}
	close(work)
	wg.Wait()

	for round := 0; round < rounds; round++ {
		work = make(chan int)
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range work {
					if agents[i].id == "" {
						continue
					}
					t0 := time.Now()
					_, err := api.Submit(ctx, agents[i].id, agents[i].cred, Synthesize(i, round, seed, change))
					res.add(time.Since(t0), err)
				}
			}()
		}
		for i := range tokens {
			work <- i
		}
		close(work)
		wg.Wait()
	}
	res.elapsed = time.Since(start)
	return res
}

// Synthesize builds host i's report for a round. Identities are stable per
// seed; from round 1 on a `change` fraction of hosts moves its primary
// address to a new host number in the same subnet.
func Synthesize(i, round int, seed uint64, change float64) store.Inventory {
	rnd := rand.New(rand.NewPCG(seed, uint64(i))) // #nosec G404 -- synthetic test data
	subnet := i / 200
	hostOctet := i%200 + 10
	if round > 0 && rand.New(rand.NewPCG(seed+uint64(round), uint64(i))).Float64() < change { // #nosec G404 -- synthetic test data
		hostOctet = 210 + (i+round)%40
	}
	primary := fmt.Sprintf("10.%d.%d.%d", 20+subnet/250, subnet%250, hostOctet)
	mac := func(n int) string {
		return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", byte(seed), byte(i>>16), byte(i>>8), byte(i), byte(n))
	}
	inv := store.Inventory{
		Identity:     store.Identity{Hostname: fmt.Sprintf("loadgen-%05d", i), HardwareUUID: fmt.Sprintf("00000000-0000-4000-8000-%012x", seed<<32|uint64(i))},
		CollectedAt:  time.Now().UTC(),
		AgentVersion: "loadgen",
		OS:           store.OSInfo{Name: "ubuntu", Version: "24.04", Arch: "amd64", Family: "linux"},
		System:       store.SystemInfo{Manufacturer: "Loadgen", ProductName: "Synthetic", SerialNumber: fmt.Sprintf("LG%08d", i)},
		PrimaryIPv4:  primary,
		PrimaryIPv6:  fmt.Sprintf("fd00:%x::%x", subnet, hostOctet),
		Networks: []store.NetIface{
			{Name: "eno1", MAC: mac(1), Type: store.IfaceEthernet, SpeedBps: 1e9, Up: true, DefaultRoute: true, Gateway: fmt.Sprintf("10.%d.%d.1", 20+subnet/250, subnet%250),
				IPAddresses: []string{primary + "/24"},
				Addresses: []store.IfAddress{
					{Address: primary, PrefixLength: 24, Family: "ipv4", Scope: "global"},
					{Address: fmt.Sprintf("fd00:%x::%x", subnet, hostOctet), PrefixLength: 64, Family: "ipv6", Scope: "global"},
					{Address: fmt.Sprintf("fe80::%x", i+1), PrefixLength: 64, Family: "ipv6", Scope: "link"},
				}},
			{Name: "eno2", MAC: mac(2), Type: store.IfaceEthernet, SpeedBps: 1e10, Up: rnd.IntN(2) == 0},
			{Name: "docker0", MAC: mac(3), Type: store.IfaceBridge, Up: true, Addresses: []store.IfAddress{{Address: "172.17.0.1", PrefixLength: 16, Family: "ipv4", Scope: "global"}}},
		},
		Virtualization: store.Virtualization{Role: store.RolePhysical, Source: "dmi"},
		Bmc: &store.Bmc{Address: fmt.Sprintf("10.%d.%d.%d", 120+subnet/250, subnet%250, i%200+10), PrefixLength: 24, IPSource: "static",
			Ports: []store.BmcPort{{Channel: 1, MAC: mac(9)}}},
		UpdateState: store.UpdateState{PackageManager: "apt", Status: store.UpdateAvailable, RebootRequired: store.TriFalse,
			AutomaticUpdates: store.TriTrue, SecurityClassified: true, CheckedAt: time.Now().UTC()},
	}
	if i%10 == 0 { // every tenth host is a Proxmox node with five guests
		for g := 0; g < 5; g++ {
			inv.HypervisorGuests = append(inv.HypervisorGuests, store.HypervisorGuest{ID: fmt.Sprint(100 + g), Name: fmt.Sprintf("guest-%d-%d", i, g),
				Kind: "vm", Platform: "proxmox", MACs: []string{fmt.Sprintf("bc:24:11:%02x:%02x:%02x", byte(i>>8), byte(i), byte(g))}})
		}
	}
	for p := 0; p < 200; p++ {
		prog := store.Program{Name: fmt.Sprintf("pkg-%03d", p), Version: "1.0." + fmt.Sprint(p)}
		if p < 20 {
			prog.AvailableVersion, prog.SecurityUpdate = "1.1."+fmt.Sprint(p), p%5 == 0
			inv.UpdateState.PendingCount++
			if prog.SecurityUpdate {
				inv.UpdateState.SecurityCount++
			}
		}
		inv.Programs = append(inv.Programs, prog)
	}
	return inv
}

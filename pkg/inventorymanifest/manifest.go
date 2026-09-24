// Package inventorymanifest declares what the inventory module registers with the
// application gateway: routes derived from the embedded OpenAPI document, the
// API permissions, the CASL abilities and the navigation entries. The inventory
// gRPC surface is service-to-service (and the ingest edge is off-mesh); neither
// is proxied by the gateway.
package inventorymanifest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/api/openapi"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"
)

// Module identity.
const (
	Module       = "inventory"
	DisplayName  = "Inventory"
	Version      = "1.0.0"
	RemotePrefix = "/ui"
)

// OpenAPI operation extensions.
const (
	PermissionExtension = "x-freya-permission"
	PublicExtension     = "x-freya-public"
	BodyLimitExtension  = "x-freya-max-body-bytes"
	TimeoutExtension    = "x-freya-timeout-seconds"
)

// Permissions the module registers.
var Permissions = []gatewayclient.Permission{
	{Resource: "inventory", Action: "read", Description: "List and read hosts, snapshots and the live stream"},
	{Resource: "inventory", Action: "write", Description: "Contribute inventory data (manual snapshots, imports)"},
	{Resource: "hosts", Action: "manage", Description: "Tag, retire and delete hosts"},
	{Resource: "agents", Action: "manage", Description: "Enroll, refresh, list and revoke endpoint agents"},
	{Resource: "snapshots", Action: "read", Description: "Read snapshot history, diffs and change records"},
	{Resource: "snapshots", Action: "manage", Description: "Delete snapshots"},
	{Resource: "stats", Action: "read", Description: "Read inventory statistics"},
	{Resource: "backup", Action: "manage", Description: "Export and import tenant inventory data"},
}

// Grants maps built-in role slugs to the permissions they hold.
var Grants = map[string][]string{
	"owner":    PermissionRefs(),
	"admin":    PermissionRefs(),
	"member":   {"inventory:read", "inventory:write", "snapshots:read", "stats:read"},
	"auditor":  {"inventory:read", "snapshots:read", "stats:read"},
	"operator": {"inventory:read", "inventory:write", "hosts:manage", "agents:manage", "snapshots:read", "snapshots:manage", "stats:read"},
}

// Methods proxied by the gateway: none (inventory gRPC is service to service and
// the ingest edge is off-mesh).
var Methods []gatewayclient.Method

// Abilities are the CASL rules bound to the permissions.
var Abilities = []gatewayclient.Ability{
	{Action: []string{"read", "create", "update", "delete"}, Subject: []string{"InventoryHost"}, Requires: "inventory:read"},
	{Action: []string{"read", "create", "update", "delete"}, Subject: []string{"InventorySnapshot"}, Requires: "snapshots:read"},
	{Action: []string{"manage"}, Subject: []string{"InventoryAgent"}, Requires: "agents:manage"},
	{Action: []string{"read"}, Subject: []string{"InventoryStats"}, Requires: "stats:read"},
}

// Nav lists the navigation contributions.
var Nav = []gatewayclient.NavEntry{
	{Title: "Hosts", Path: "/inventory", Icon: "mdi-monitor-multiple", Order: 600, Requires: "inventory:read"},
	{Title: "Agents", Path: "/inventory/agents", Icon: "mdi-lan-connect", Order: 610, Requires: "agents:manage"},
	{Title: "Dashboard", Path: "/inventory/dashboard", Icon: "mdi-view-dashboard-outline", Order: 620, Requires: "stats:read"},
}

// PermissionRefs lists "resource:action" for every declared permission.
func PermissionRefs() []string {
	out := make([]string, 0, len(Permissions))
	for _, p := range Permissions {
		out = append(out, p.Resource+":"+p.Action)
	}
	return out
}

// Routes derives the gateway routes from the OpenAPI document.
func Routes(doc *openapi3.T) ([]gatewayclient.Route, error) {
	known := map[string]bool{}
	for _, p := range PermissionRefs() {
		known[p] = true
	}
	var routes []gatewayclient.Route
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			r := gatewayclient.Route{Method: strings.ToUpper(m), Path: p}
			perm, _ := op.Extensions[PermissionExtension].(string)
			public, _ := op.Extensions[PublicExtension].(bool)
			switch {
			case public:
				r.Public = true
			case perm == "":
				return nil, fmt.Errorf("inventorymanifest: %s %s declares no permission", r.Method, p)
			case !known[perm]:
				return nil, fmt.Errorf("inventorymanifest: %s %s uses undeclared permission %q", r.Method, p, perm)
			default:
				r.Permission = perm
			}
			if v, ok := op.Extensions[BodyLimitExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 {
					return nil, fmt.Errorf("inventorymanifest: %s %s has a bad body limit", r.Method, p)
				}
				r.MaxBodyBytes = uint64(n)
			}
			if v, ok := op.Extensions[TimeoutExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 || n > 600 {
					return nil, fmt.Errorf("inventorymanifest: %s %s has a bad timeout", r.Method, p)
				}
				r.Timeout = time.Duration(n) * time.Second
			}
			routes = append(routes, r)
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Method+" "+routes[i].Path < routes[j].Method+" "+routes[j].Path })
	return routes, nil
}

// Load parses the embedded document.
func Load() (*openapi3.T, error) {
	return openapi3.NewLoader().LoadFromData(openapi.Inventory)
}

// Manifest builds the gateway manifest from the embedded OpenAPI document.
func Manifest() (gatewayclient.Manifest, error) {
	doc, err := Load()
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	routes, err := Routes(doc)
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	return gatewayclient.Manifest{
		Module: Module, DisplayName: DisplayName, Version: Version,
		Prefixes:    []string{"/api/inventory"},
		Routes:      routes,
		Methods:     Methods,
		Permissions: Permissions,
		Abilities:   Abilities,
		Exposes:     []string{"./routes", "./nav"},
		Nav:         Nav,
	}, nil
}

// SeedRequest builds the auth registration request: every module permission plus
// the built-in role grants.
func SeedRequest() *authv1.RegisterPermissionsRequest {
	req := &authv1.RegisterPermissionsRequest{}
	for _, p := range Permissions {
		req.Permissions = append(req.Permissions, &authv1.PermissionDef{Resource: p.Resource, Action: p.Action, Description: p.Description})
	}
	for _, slug := range []string{"owner", "admin", "member", "auditor", "operator"} {
		req.BuiltinGrants = append(req.BuiltinGrants, &authv1.BuiltinGrant{Role: slug, Permissions: Grants[slug]})
	}
	return req
}

// SeedPermissions registers the module's permissions with the auth service and
// grants them to the built-in roles (idempotent). The gateway registers the
// permissions from the manifest for routing; only the inventory module knows the
// role grants, so it pushes them to auth here.
func SeedPermissions(ctx context.Context, cc grpc.ClientConnInterface) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := authv1.NewAuthorizationClient(cc).RegisterPermissions(ctx, SeedRequest())
	return err
}

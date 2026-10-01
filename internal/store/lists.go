package store

import "github.com/go-tangra/go-tangra/v4/listquery"

// List definitions of the inventory tables (specs/032-server-side-tables in
// go-tangra, contracts/sortable-fields.md). Sort fields map to constant SQL
// expressions only; the memstore and the in-memory lists (agent fleet,
// auto-enrollment keys) sort the same public names in Go.
var (
	// HostList pages inventory_hosts (GET /hosts): hostname order by default.
	// Every sort column is NOT NULL (text ones DEFAULT ''), so NotNull lets
	// the (tenant_id, <expr>, id) indexes serve both directions.
	HostList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"hostname":     {Expr: "hostname", Text: true, NotNull: true},
			"os_name":      {Expr: "os_name", Text: true, NotNull: true},
			"manufacturer": {Expr: "manufacturer", Text: true, NotNull: true},
			"status":       {Expr: "status", NotNull: true},
			"last_seen":    {Expr: "last_seen", DefaultDir: listquery.Desc, NotNull: true},
			"created_at":   {Expr: "created_at", DefaultDir: listquery.Desc, NotNull: true},
		},
		Default: "hostname", TieBreak: "id",
	}
	// SnapshotList pages a host's inventory_snapshots: newest first.
	SnapshotList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"collected_at": {Expr: "collected_at", DefaultDir: listquery.Desc, NotNull: true},
		},
		Default: "collected_at", TieBreak: "id",
	}
	// ChangeList pages a host's inventory_changes: newest first; kind is the
	// change type (added / removed / modified).
	ChangeList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"detected_at": {Expr: "detected_at", DefaultDir: listquery.Desc, NotNull: true},
			"kind":        {Expr: "change_type", NotNull: true},
		},
		Default: "detected_at", TieBreak: "id",
	}
	// FleetList pages the agent fleet view (built in Go): hostname order.
	FleetList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"hostname":  {Expr: "hostname", Text: true},
			"version":   {Expr: "version"},
			"state":     {Expr: "upgrade_state"},
			"last_seen": {Expr: "last_seen", DefaultDir: listquery.Desc},
		},
		Default: "hostname", TieBreak: "agent_id",
	}
	// CertItemList pages certificate delivery items (feature 033,
	// GET /certificate-deliveries): newest first.
	CertItemList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"created_at": {Expr: "created_at", DefaultDir: listquery.Desc, NotNull: true},
			"updated_at": {Expr: "updated_at", DefaultDir: listquery.Desc, NotNull: true},
			"state":      {Expr: "state", NotNull: true},
			"name":       {Expr: "name", Text: true, NotNull: true},
		},
		Default: "created_at", TieBreak: "id",
	}
	// AutoEnrollKeyList pages a tenant's auto-enrollment keys (in Go).
	AutoEnrollKeyList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"name":       {Expr: "name", Text: true, NotNull: true},
			"created_at": {Expr: "created_at", DefaultDir: listquery.Desc, NotNull: true},
		},
		Default: "name", TieBreak: "id",
	}
)

// ListRequest completes r with the Spec's defaults (a zero Request from an
// internal caller pages with the defaults); an invalid hand-built Request
// falls back to the defaults entirely.
func ListRequest(r listquery.Request, s listquery.Spec) listquery.Request {
	out, err := listquery.New(r.Page, r.PageSize, r.Sort, r.Order, s)
	if err != nil {
		out, _ = listquery.New(0, 0, "", "", s)
	}
	return out
}

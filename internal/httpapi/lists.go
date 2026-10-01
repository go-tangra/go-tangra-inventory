package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// List contract helpers (go-tangra specs/032-server-side-tables,
// contracts/http-list.md).

// parseList reads page, page_size, sort and order against spec. An invalid
// value (or legacy cursor/limit mixed with them) is answered with
// validation_failed naming the parameter, never its value.
func parseList(w http.ResponseWriter, r *http.Request, spec listquery.Spec) (listquery.Request, bool) {
	req, err := listquery.Parse(r.URL.Query(), spec)
	var le *listquery.Error
	if errors.As(err, &le) {
		writeParamError(w, le.Param)
		return req, false
	}
	return req, true
}

// writeParamError answers 422 validation_failed with detail.param.
func writeParamError(w http.ResponseWriter, param string) {
	WriteDetail(w, ErrValidation, map[string]any{"param": param})
}

// hostFilter reads the GET /hosts filters. last_seen_from / last_seen_to
// are RFC 3339 instants; an invalid one is reported by its parameter name.
func hostFilter(q url.Values) (store.HostFilter, string) {
	f := store.HostFilter{
		Hostname:     q.Get("hostname"),
		OSName:       q.Get("os_name"),
		Manufacturer: q.Get("manufacturer"),
		Status:       q.Get("status"),
		Tag:          q.Get("tag"),
	}
	if utf8.RuneCountInString(f.Hostname) > store.MaxSearchLen {
		return f, "hostname"
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"last_seen_from", &f.LastSeenFrom}, {"last_seen_to", &f.LastSeenTo}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return f, p.name
		}
		t = t.UTC()
		*p.dst = &t
	}
	return f, ""
}

// legacyDefaultLimit is the page size of a legacy request without a usable
// limit (cursor only, or limit absent, invalid or < 1).
const legacyDefaultLimit = 50

// legacyLimit is the page size a legacy cursor/limit request stands for:
// always 1..listquery.MaxPageSize, legacyDefaultLimit when the limit is
// absent, invalid or < 1. A legacy request never reads an unbounded list.
func legacyLimit(q url.Values) int {
	return clampLimit(q.Get("limit"), legacyDefaultLimit)
}

// clampLimit parses a limit query value into 1..listquery.MaxPageSize; an
// absent, invalid or non-positive value stands for def.
func clampLimit(v string, def int) int {
	n := atoiDefault(v, def)
	if n < 1 {
		n = def
	}
	return min(n, listquery.MaxPageSize)
}

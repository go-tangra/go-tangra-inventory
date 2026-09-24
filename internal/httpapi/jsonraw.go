package httpapi

import "encoding/json"

// jsonRaw returns b as raw JSON for embedding in a response map, or a string
// fallback if it is not valid JSON.
func jsonRaw(b []byte) any {
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

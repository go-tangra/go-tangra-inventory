// Package openapi embeds the inventory browser API contract.
package openapi

import _ "embed"

// Inventory is the OpenAPI 3.1 document served and validated by the service.
//
//go:embed inventory.yaml
var Inventory []byte

// Package api embeds the OpenAPI contract so the running binary can serve it.
//
// The spec lives here rather than next to the router because it is the
// contract for three clients, not an implementation detail of one of them.
// Embedding matters for the distroless production image, which has no source
// tree to read from at runtime.
package api

import _ "embed"

// Spec is the raw contents of openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte

// SpecFilename is the name the spec is served under.
const SpecFilename = "openapi.yaml"

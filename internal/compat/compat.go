// Package compat defines Carbon's public version and compatibility-layer contract.
//
// Product builds can advance independently from the durable API compatibility
// surface. In particular, StableLayer is a deliberate governance decision, not a
// value inferred from a build tag or product version.
package compat

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// APIVersion identifies the HTTP/MCP transport contract. It is intentionally
	// independent from both a Carbon product build and the selected compatibility
	// layer.
	APIVersion = "v1"

	// LegacyLayer is the frozen project-local Cairn compatibility surface. It
	// maps to the historical 0.3 semantics and intentionally remains available
	// only for explicit --repo scopes.
	LegacyLayer = "v1"

	// StableLayer is the explicitly approved Carbon compatibility surface. It
	// maps to Carbon 0.4 semantics, but is deliberately a compatibility value,
	// not a product/build version. Do not derive it from a product build.
	StableLayer = "v2"

	// PreviewLayer is retained as a deprecated source-compatibility alias for
	// external callers that compiled against the pre-v2-stabilization name.
	// New code must use StableLayer; v2 is no longer a preview surface.
	//
	// Deprecated: use StableLayer.
	PreviewLayer = StableLayer
)

// Mode selects the default compatibility layer when a caller did not explicitly
// request one. It describes the selected storage/scope model, not an authorization
// boundary.
type Mode string

const (
	ModeLegacy Mode = "legacy"
	ModeCarbon Mode = "carbon"
)

// ErrUnsupportedLayer is returned for an unknown, future, or malformed requested
// compatibility layer. Callers must fail closed rather than guessing a downgrade or
// treating a future client as compatible.
var ErrUnsupportedLayer = errors.New("unsupported compatibility layer")

// ErrLayerScopeMismatch means that a known layer was requested for the wrong
// storage contract. v1 is the frozen project-local Cairn contract; v2 is the
// Carbon-home contract. Keeping this distinct from ErrUnsupportedLayer lets a
// transport tell an invalid future client from an unsafe attempted downgrade.
var ErrLayerScopeMismatch = errors.New("compatibility layer does not match scope")

// ErrUnsupportedMode is defensive: callers must not turn an unrecognised storage
// mode into an implicit compatibility selection.
var ErrUnsupportedMode = errors.New("unsupported compatibility mode")

// Contract is the machine-readable version envelope returned by HTTP status,
// version, health, and identity endpoints.
type Contract struct {
	ProductVersion        string   `json:"productVersion"`
	APIVersion            string   `json:"apiVersion"`
	RequestedCompatLayer  string   `json:"requestedCompatLayer"`
	SupportedCompatLayers []string `json:"supportedCompatLayers"`
	StableCompatLayer     string   `json:"stableCompatLayer"`
	Capabilities          []string `json:"capabilities"`
}

// SupportedLayers returns a copy so a caller cannot mutate the package-level
// compatibility policy.
func SupportedLayers() []string {
	return []string{LegacyLayer, StableLayer}
}

// DefaultLayer is intentionally scope-based: existing --repo workflows retain
// their frozen v1 semantics, while a Carbon home selects the approved stable v2
// semantics by default.
func DefaultLayer(mode Mode) string {
	if mode == ModeLegacy {
		return LegacyLayer
	}
	return StableLayer
}

// Resolve validates an optional requested layer and returns its complete public
// contract. Empty requests pick the mode-specific default. ProductVersion is only
// descriptive: no product build can implicitly move StableLayer.
func Resolve(productVersion, requested string, mode Mode) (Contract, error) {
	if mode != ModeLegacy && mode != ModeCarbon {
		return Contract{}, fmt.Errorf("%w %q", ErrUnsupportedMode, mode)
	}
	expected := DefaultLayer(mode)
	layer := strings.TrimSpace(requested)
	if layer == "" {
		layer = expected
	}
	canonical, ok := canonicalLayer(layer)
	if !ok {
		return Contract{}, fmt.Errorf("%w %q (supported: %s; legacy input aliases: 0.3, 0.4)", ErrUnsupportedLayer, layer, strings.Join(SupportedLayers(), ", "))
	}
	layer = canonical
	if layer != expected {
		return Contract{}, fmt.Errorf("%w: %s scope requires %s, got %s", ErrLayerScopeMismatch, mode, expected, layer)
	}
	productVersion = strings.TrimSpace(productVersion)
	if productVersion == "" {
		productVersion = "dev"
	}
	return Contract{
		ProductVersion:        productVersion,
		APIVersion:            APIVersion,
		RequestedCompatLayer:  layer,
		SupportedCompatLayers: SupportedLayers(),
		StableCompatLayer:     StableLayer,
		Capabilities:          Capabilities(layer),
	}, nil
}

// Capabilities returns the features advertised for exactly one selected layer.
// A v1 legacy client must not be told that an unselected v2 Carbon surface is
// part of its active contract merely because this Carbon binary includes it.
func Capabilities(layer string) []string {
	base := []string{
		"cairn-0.3", "task-graph", "http-api", "mcp", "sessions", "checks",
	}
	canonical, ok := canonicalLayer(strings.TrimSpace(layer))
	if !ok || canonical != StableLayer {
		return base
	}
	return append(base,
		"carbon-0.4", "home", "cluster-scopes", "project-scopes", "etag",
		"leases", "trash", "search", "views", "templates", "bulk", "worker-stats",
		"worker-registry", "worker-analytics", "work-logs", "task-blocker",
		"task-evidence", "catalog-icons", "home-events", "types", "backup", "legacy-migration",
		"event-subscriptions",
	)
}

func canonicalLayer(layer string) (string, bool) {
	switch layer {
	case LegacyLayer, "0.3":
		return LegacyLayer, true
	case StableLayer, "0.4":
		return StableLayer, true
	default:
		return "", false
	}
}

package compat

import (
	"errors"
	"slices"
	"testing"
)

func TestCompatibilityLayersStayPinnedAcrossProductBuilds(t *testing.T) {
	for _, product := range []string{"dev", "0.4.0", "0.4.99", "12.7.3+build.4"} {
		contract, err := Resolve(product, "", ModeLegacy)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", product, err)
		}
		if contract.StableCompatLayer != StableLayer {
			t.Fatalf("build %q moved stable layer to %q, want %q", product, contract.StableCompatLayer, StableLayer)
		}
		if contract.RequestedCompatLayer != LegacyLayer {
			t.Fatalf("legacy build %q requested %q, want %q", product, contract.RequestedCompatLayer, LegacyLayer)
		}
		if contract.ProductVersion != product {
			t.Fatalf("product version = %q, want %q", contract.ProductVersion, product)
		}
	}
}

func TestCarbonDefaultsToApprovedStableLayer(t *testing.T) {
	contract, err := Resolve("0.4.27", "", ModeCarbon)
	if err != nil {
		t.Fatal(err)
	}
	if contract.RequestedCompatLayer != StableLayer {
		t.Fatalf("Carbon default = %q, want %q", contract.RequestedCompatLayer, StableLayer)
	}
	if contract.StableCompatLayer != StableLayer {
		t.Fatalf("stable layer = %q, want %q", contract.StableCompatLayer, StableLayer)
	}
	if !slices.Contains(contract.Capabilities, "carbon-0.4") {
		t.Fatalf("stable v2 capabilities missing carbon-0.4: %v", contract.Capabilities)
	}
}

func TestResolveRejectsUnknownAndFutureLayers(t *testing.T) {
	for _, requested := range []string{"0.2", "0.5", "v3", "1", "v0.4", "0.4.1"} {
		if _, err := Resolve("0.4.0", requested, ModeCarbon); !errors.Is(err, ErrUnsupportedLayer) {
			t.Errorf("Resolve(%q) error = %v, want ErrUnsupportedLayer", requested, err)
		}
	}
}

func TestLegacySemanticAliasesCanonicalizeToCompatibilityLayers(t *testing.T) {
	for _, tc := range []struct {
		requested string
		mode      Mode
		want      string
	}{
		{requested: "0.3", mode: ModeLegacy, want: LegacyLayer},
		{requested: "v1", mode: ModeLegacy, want: LegacyLayer},
		{requested: "0.4", mode: ModeCarbon, want: StableLayer},
		{requested: "v2", mode: ModeCarbon, want: StableLayer},
	} {
		contract, err := Resolve("0.4.9", tc.requested, tc.mode)
		if err != nil {
			t.Fatalf("Resolve(%q, %s): %v", tc.requested, tc.mode, err)
		}
		if contract.RequestedCompatLayer != tc.want {
			t.Fatalf("Resolve(%q) canonical = %q, want %q", tc.requested, contract.RequestedCompatLayer, tc.want)
		}
	}
}

func TestResolveRejectsKnownLayerForWrongScope(t *testing.T) {
	for _, tc := range []struct {
		mode      Mode
		requested string
	}{
		{mode: ModeLegacy, requested: StableLayer},
		{mode: ModeLegacy, requested: "0.4"},
		{mode: ModeCarbon, requested: LegacyLayer},
		{mode: ModeCarbon, requested: "0.3"},
	} {
		if _, err := Resolve("0.4.9", tc.requested, tc.mode); !errors.Is(err, ErrLayerScopeMismatch) {
			t.Errorf("Resolve(%q, %s) error = %v, want ErrLayerScopeMismatch", tc.requested, tc.mode, err)
		}
	}
}

func TestSupportedLayersAndCapabilitiesAreDefensive(t *testing.T) {
	layers := SupportedLayers()
	layers[0] = "mutated"
	if SupportedLayers()[0] != LegacyLayer {
		t.Fatal("SupportedLayers leaked mutable policy")
	}
	legacy := Capabilities(LegacyLayer)
	if slices.Contains(legacy, "carbon-0.4") {
		t.Fatalf("legacy capabilities leaked stable v2 features: %v", legacy)
	}
	stable := Capabilities(StableLayer)
	for _, capability := range []string{
		"worker-registry", "worker-analytics", "work-logs", "task-blocker",
		"task-evidence", "catalog-icons", "home-events",
	} {
		if slices.Contains(legacy, capability) {
			t.Fatalf("legacy capabilities leaked %q: %v", capability, legacy)
		}
		if !slices.Contains(stable, capability) {
			t.Fatalf("stable v2 capabilities missing %q", capability)
		}
	}
}

func TestPreviewLayerRemainsSourceCompatibilityAlias(t *testing.T) {
	if PreviewLayer != StableLayer {
		t.Fatalf("PreviewLayer = %q, want deprecated StableLayer alias %q", PreviewLayer, StableLayer)
	}
}

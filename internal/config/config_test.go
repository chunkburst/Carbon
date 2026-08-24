package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `prefix: PROJ
counter: 2
states: [backlog, in_progress, in_review, done, canceled]
closed: [done, canceled]
initial: backlog
check_timeout_default: 120
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	c, err := Load(writeConfig(t, sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Prefix != "PROJ" || c.Counter != 2 || c.Initial != "backlog" || c.CheckTimeoutDefault != 120 {
		t.Fatalf("unexpected config: %+v", c)
	}
	if len(c.States) != 5 || len(c.Closed) != 2 {
		t.Fatalf("unexpected states/closed: %+v", c)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSaveRoundTrips(t *testing.T) {
	path := writeConfig(t, sample)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Counter = 41
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counter != 41 || got.Prefix != "PROJ" {
		t.Fatalf("round-trip lost data: %+v", got)
	}
}

func TestIdentityModeDefaultsOffAndRoundTrips(t *testing.T) {
	path := writeConfig(t, sample)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IdentityMode {
		t.Fatalf("old config unexpectedly enabled identity mode: %+v", loaded)
	}
	loaded.IdentityMode = true
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "identity_mode: true") {
		t.Fatalf("identity mode was not persisted:\n%s", raw)
	}
	roundTrip, err := Load(path)
	if err != nil || !roundTrip.IdentityMode {
		t.Fatalf("identity mode round-trip = %+v err=%v", roundTrip, err)
	}
}

func TestSavePreservesUnknownYAMLKeys(t *testing.T) {
	body := sample + "future_feature:\n  enabled: true\n  note: preserve me\n"
	path := writeConfig(t, body)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Counter = 99
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "future_feature:") || !strings.Contains(string(raw), "note: preserve me") {
		t.Fatalf("unknown config fields were dropped:\n%s", raw)
	}
	got, err := Load(path)
	if err != nil || got.Counter != 99 {
		t.Fatalf("round trip after unknown preservation = %+v err=%v", got, err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"initial not a state", "prefix: P\ncounter: 0\nstates: [a, b]\nclosed: [b]\ninitial: zzz\ncheck_timeout_default: 1\n", ErrInvalidConfig},
		{"closed not a subset", "prefix: P\ncounter: 0\nstates: [a, b]\nclosed: [c]\ninitial: a\ncheck_timeout_default: 1\n", ErrInvalidConfig},
		{"empty states", "prefix: P\ncounter: 0\nstates: []\nclosed: []\ninitial: a\ncheck_timeout_default: 1\n", ErrInvalidConfig},
		{"valid", sample, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if !errors.Is(err, tt.want) {
				t.Fatalf("Load = %v, want errors.Is %v", err, tt.want)
			}
		})
	}
}

func TestWorkflowDefaultsAndCustomType(t *testing.T) {
	c := Default("PROJ")
	if c.ProjectID == "" || c.TrashRetentionDuration() != 30*24*time.Hour {
		t.Fatalf("workflow defaults = %+v", c)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	next, definition, err := c.WithTaskTypeDisplayName("review_pack", "审查包", "human:li", now)
	if err != nil {
		t.Fatal(err)
	}
	if definition.DisplayName != "审查包" || !next.TypeCatalog().Allowed("review_pack") {
		t.Fatalf("custom type lost: %+v %+v", definition, next)
	}
}

func TestStableProjectIDUsesCarbonSaltAndLegacyValueRemainsAvailable(t *testing.T) {
	if StableProjectID("PROJ") == LegacyStableProjectID("PROJ") {
		t.Fatal("new Carbon project id unexpectedly reuses legacy salt")
	}
	legacy := LegacyStableProjectID("PROJ")
	path := writeConfig(t, sample+"project_id: "+legacy+"\n")
	loaded, err := Load(path)
	if err != nil || loaded.ProjectID != legacy {
		t.Fatalf("legacy persisted project id = %q err=%v", loaded.ProjectID, err)
	}
}

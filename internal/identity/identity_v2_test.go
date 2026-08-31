package identity

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func TestLegacyRegistryProjectsToRolesAndCanonicalAuditWithoutBreakingOldDecode(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IDV2"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	legacy := []byte("version: 1\nrecords:\n  - actor: agent:architect\n    role: 架构师\n    types: [patch]\n    claimed_at: 2026-08-30T08:00:00Z\n    updated_at: 2026-08-30T08:00:00Z\n    changed_by: human:lead\n")
	if err := data.Write(context.Background(), "human:lead", "seed old registry", func(tx *store.WriteTx) error {
		return tx.WriteData(dataDir, registryName, legacy)
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	manager, err := NewProject(data, func() time.Time { return now }, "project_one", true)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := manager.Get("agent:architect")
	if err != nil || projected.Role != "architect" || len(projected.Roles) != 1 || projected.Roles[0] != "architect" {
		t.Fatalf("legacy projection = %#v err=%v", projected, err)
	}
	changed, err := manager.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:architect", Roles: []string{"architect", "reviewer"}, Types: []string{"patch"}, Reason: "补充审核职责"}, ChangeOptions{ProjectID: "project_one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Roles) != 2 || changed.Role != "architect" {
		t.Fatalf("canonical changed record = %#v", changed)
	}
	audits, err := manager.ListAudit()
	if err != nil || len(audits) != 1 || audits[0].RelatedIncidentID == "" || audits[0].Operation != "changed" {
		t.Fatalf("audit = %#v err=%v", audits, err)
	}
	auto, err := manager.ListAutoIncidents("project_one")
	if err != nil || len(auto) != 1 || auto[0].ID != audits[0].RelatedIncidentID || auto[0].Kind != "identity_change" {
		t.Fatalf("automatic incident = %#v err=%v", auto, err)
	}
	// The old file has no roles field, therefore an older KnownFields(true)
	// decoder keeps working even after the new canonical state was written.
	raw, err := data.ReadData(dataDir, registryName)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "roles:") {
		t.Fatalf("legacy projection leaked roles field: %s", raw)
	}
	if _, err := decode(raw); err != nil {
		t.Fatalf("legacy registry no longer decodes: %v", err)
	}
	// Exact retry performs no additional durable audit or automatic incident.
	if _, err := manager.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:architect", Roles: []string{"architect", "reviewer"}, Types: []string{"patch"}, Reason: "ignored retry reason"}, ChangeOptions{ProjectID: "project_one"}); err != nil {
		t.Fatal(err)
	}
	audits, _ = manager.ListAudit()
	auto, _ = manager.ListAutoIncidents("project_one")
	if len(audits) != 1 || len(auto) != 1 {
		t.Fatalf("idempotent retry duplicated journal: audits=%d auto=%d", len(audits), len(auto))
	}
}

func TestNoTraceStillWritesPermanentIdentityAudit(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IDV2"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewProject(store.New(root), nil, "project_one", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:worker", Roles: []string{"backend"}, Types: []string{"patch"}}, ChangeOptions{ProjectID: "project_one", NoTraceMode: true}); err != nil {
		t.Fatal(err)
	}
	audits, err := manager.ListAudit()
	if err != nil || len(audits) != 1 || audits[0].RelatedIncidentID != "" {
		t.Fatalf("no trace audit = %#v err=%v", audits, err)
	}
	auto, err := manager.ListAutoIncidents("project_one")
	if err != nil || len(auto) != 0 {
		t.Fatalf("no trace automatic incidents = %#v err=%v", auto, err)
	}
}

func TestProjectIdentityStateIsolatedAndLegacyProjectionConflictFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IDS"); err != nil {
		t.Fatal(err)
	}
	data := store.New(root)
	now := func() time.Time { return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC) }
	standalone, err := NewProject(data, now, "project_solo", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := standalone.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:solo", Roles: []string{"backend"}, Types: []string{"patch"}}, ChangeOptions{ProjectID: "project_solo"}); err != nil {
		t.Fatal(err)
	}
	projectA, err := NewProject(data, now, "project_a", false)
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := NewProject(data, now, "project_b", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectA.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:worker", Roles: []string{"reviewer"}, Types: []string{"patch"}}, ChangeOptions{ProjectID: "project_a"}); err != nil {
		t.Fatal(err)
	}
	if records, err := projectB.List(); err != nil || len(records) != 0 {
		t.Fatalf("project B records leaked from A: %#v err=%v", records, err)
	}
	if audits, err := projectB.ListAudit(); err != nil || len(audits) != 0 {
		t.Fatalf("project B audits leaked from A: %#v err=%v", audits, err)
	}
	if auto, err := projectB.ListAutoIncidents("project_b"); err != nil || len(auto) != 0 {
		t.Fatalf("project B automatic incidents leaked from A: %#v err=%v", auto, err)
	}

	// Simulate a downgraded standalone binary editing the old projection after a
	// canonical state exists. The content hash baseline must fail closed rather
	// than overwrite this file on the next v2 identity call.
	legacy := []byte("version: 1\nrecords:\n  - actor: agent:solo\n    role: frontend\n    types: [extension]\n    claimed_at: 2026-08-30T10:00:00Z\n    updated_at: 2026-08-30T10:00:00Z\n    changed_by: human:lead\n")
	if err := data.Write(context.Background(), "human:lead", "simulate old registry edit", func(tx *store.WriteTx) error {
		return tx.WriteData(dataDir, registryName, legacy)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := standalone.List(); !errors.Is(err, ErrLegacyProjectionConflict) {
		t.Fatalf("legacy projection conflict = %v, want ErrLegacyProjectionConflict", err)
	}
	// The same condition must also stop an idempotent retry before it can append
	// another audit or silently restore the stale projection.
	if _, err := standalone.ClaimOrChangeWithOptions(context.Background(), "human:lead", ClaimInput{Actor: "agent:solo", Roles: []string{"backend"}, Types: []string{"patch"}}, ChangeOptions{ProjectID: "project_solo"}); !errors.Is(err, ErrLegacyProjectionConflict) {
		t.Fatalf("idempotent retry after legacy projection conflict = %v, want ErrLegacyProjectionConflict", err)
	}
	afterLegacy, err := data.ReadData(dataDir, registryName)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterLegacy, legacy) {
		t.Fatalf("legacy projection was silently overwritten: %q", afterLegacy)
	}
	canonical, err := data.ReadData(projectStateDir, "project_solo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := decodeState(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Audits) != 1 {
		t.Fatalf("conflicted retry appended audit: %#v", journal.Audits)
	}
}

func TestRolesAndLegacyRoleCannotBeSuppliedTogether(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "IDR"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewProject(store.New(root), nil, "project_one", false)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]ClaimInput{
		"same role":   {Actor: "agent:worker", Roles: []string{"backend"}, Role: "backend", Types: []string{"patch"}},
		"empty roles": {Actor: "agent:worker", Roles: []string{}, Role: "backend", Types: []string{"patch"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.ClaimOrChangeWithOptions(context.Background(), "human:lead", input, ChangeOptions{ProjectID: "project_one", NoTraceMode: true}); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("roles + role = %v, want invalid identity", err)
			}
		})
	}
}

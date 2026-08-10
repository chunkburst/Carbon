package types

import (
	"errors"
	"testing"
	"time"
)

func TestCreateWithDisplayNameRateAndQuota(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	catalog := NewCatalog(nil, 1, time.Minute)
	next, created, err := catalog.CreateWithDisplayName("review_pack", "审查包", "human:li", now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Key != "review_pack" || created.DisplayName != "审查包" || !next.Allowed("review_pack") {
		t.Fatalf("created catalog = %+v / %+v", created, next)
	}
	if _, _, err := next.CreateWithDisplayName("another", "另一个", "human:li", now.Add(time.Second)); !errors.Is(err, ErrCustomTypeLimit) {
		t.Fatalf("second create = %v, want custom limit", err)
	}

	rated := NewCatalog([]Definition{{Key: "existing", CreatedAt: now.Format(time.RFC3339)}}, 4, time.Minute)
	if _, _, err := rated.CreateWithDisplayName("another", "另一个", "human:li", now.Add(time.Second)); !errors.Is(err, ErrCreationRateLimit) {
		t.Fatalf("rate limited create = %v", err)
	}
}

func TestValidateDisplayName(t *testing.T) {
	if err := ValidateDisplayName("基础设施"); err != nil {
		t.Fatalf("unicode display name: %v", err)
	}
	if err := ValidateDisplayName("\n"); !errors.Is(err, ErrInvalidDisplayName) {
		t.Fatalf("control display name = %v", err)
	}
}

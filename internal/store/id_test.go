package store

import (
	"regexp"
	"testing"
	"time"
)

func TestMintTaskID(t *testing.T) {
	re := regexp.MustCompile(`^PROJ-[0-9a-z]{10}$`)
	at := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	id, err := mintTaskID("PROJ", at)
	if err != nil {
		t.Fatalf("mintTaskID: %v", err)
	}
	if !re.MatchString(id) {
		t.Fatalf("id = %q, want match %s", id, re)
	}

	// Uniqueness across many same-instant mints (random tail). 20-bit tail makes a
	// collision in a few hundred draws vanishingly unlikely.
	seen := map[string]bool{}
	for range 200 {
		got, err := mintTaskID("PROJ", at)
		if err != nil {
			t.Fatal(err)
		}
		if seen[got] {
			t.Fatalf("duplicate id %q", got)
		}
		seen[got] = true
	}

	// Lexical order tracks chronological order. The time prefix has second resolution, so the
	// gap must be at least a second to advance it.
	older, _ := mintTaskID("PROJ", at)
	newer, _ := mintTaskID("PROJ", at.Add(time.Second))
	if older[:11] >= newer[:11] { // compare "PROJ-" + 6-char time prefix
		t.Fatalf("time prefix not monotonic: older=%q newer=%q", older, newer)
	}
}

package store

import (
	"crypto/rand"
	"fmt"
	"time"
)

// crockford is the lowercase Crockford base32 alphabet (no i, l, o, u). It is strictly
// ascending in ASCII, so the lexical order of an encoded fixed-width number matches its
// numeric order — that is what makes time-ordered ids sort chronologically as plain strings.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// idTimeChars is the width of the encoded timestamp: 6 chars × 5 bits = 30 bits of SECONDS
// since idEpoch, covering ~34 years (to ~2058). idRandChars is the random tie-break tail
// (4 chars × 5 bits = 20 bits) that distinguishes ids minted within the same second.
const (
	idTimeChars = 6
	idRandChars = 4
)

// idEpoch is 2024-01-01T00:00:00Z in Unix seconds. Counting from here instead of 1970 keeps
// the time prefix to 6 chars; ids minted before it clamp to 0 (they only sort first).
const idEpoch = 1704067200

// mintTaskID returns a time-ordered, collision-resistant task id of the form
// "<prefix>-<time><rand>" in lowercase Crockford base32, e.g. "PROJ-k3m9x7q2vw".
//
// The time prefix encodes seconds-since-idEpoch big-endian, so ids sort lexically by creation
// time (to the second); the crypto/rand tail breaks ties within the same second. Crucially,
// minting reads and writes no shared counter, so two people creating tasks in separate clones
// never collide on the id or on the on-disk filename — the conflict that a monotonic counter
// made unavoidable.
func mintTaskID(prefix string, at time.Time) (string, error) {
	v := uint64(0)
	if s := at.Unix() - idEpoch; s > 0 {
		v = uint64(s)
	}
	out := make([]byte, idTimeChars+idRandChars)
	for i := idTimeChars - 1; i >= 0; i-- {
		out[i] = crockford[v&31]
		v >>= 5
	}
	rb := make([]byte, idRandChars)
	if _, err := rand.Read(rb); err != nil {
		return "", fmt.Errorf("store: generate task id: %w", err)
	}
	for i, b := range rb {
		out[idTimeChars+i] = crockford[b&31] // 256 is a multiple of 32, so b&31 is unbiased
	}
	return fmt.Sprintf("%s-%s", prefix, out), nil
}

// mintNoteID returns a short collision-resistant id for a note provenance entry, e.g.
// "n_k3m9x7q2". Uniqueness only needs to hold within one task's provenance list, so a
// random Crockford tail (no time prefix) is enough; the "n_" prefix keeps it visually
// distinct from task ids.
func mintNoteID() (string, error) {
	rb := make([]byte, 8)
	if _, err := rand.Read(rb); err != nil {
		return "", fmt.Errorf("store: generate note id: %w", err)
	}
	out := make([]byte, len(rb))
	for i, b := range rb {
		out[i] = crockford[b&31] // 256 is a multiple of 32, so b&31 is unbiased
	}
	return "n_" + string(out), nil
}

// Package types owns the task-type catalog.  It intentionally contains no store or
// config dependency so repositories can use the same validation policy in any adapter.
package types

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Foundation = "foundation"
	Library    = "library"
	Patch      = "patch"
	Extension  = "extension"
	Plugin     = "plugin"

	DefaultMaxCustom         = 64
	DefaultMinCreateInterval = time.Minute
)

// Defaults are always available. Configured values are additive custom keys rather than
// replacements, which makes task files portable between repositories.
var Defaults = []string{Foundation, Library, Patch, Extension, Plugin}

var (
	ErrInvalidKey         = errors.New("invalid task type key")
	ErrTypeExists         = errors.New("task type already exists")
	ErrCustomTypeLimit    = errors.New("custom task type limit reached")
	ErrCreationRateLimit  = errors.New("task type creation rate limited")
	ErrInvalidDisplayName = errors.New("invalid task type display name")
)

// Definition is the durable record for a custom type. It is deliberately small and
// human-readable because it is written into config.yaml.
type Definition struct {
	Key         string   `yaml:"key" json:"key"`
	DisplayName string   `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Labels      []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	CreatedAt   string   `yaml:"created_at" json:"created_at"`
	CreatedBy   string   `yaml:"created_by,omitempty" json:"created_by,omitempty"`
}

// Catalog joins the built-ins with persisted custom definitions. MaxCustom and
// MinCreateInterval can be zero; the accessors then choose conservative defaults.
type Catalog struct {
	Custom            []Definition
	MaxCustom         int
	MinCreateInterval time.Duration
}

// NewCatalog makes a defensive copy of custom definitions. It does not silently repair
// malformed config: callers can use Validate to surface invalid persisted state.
func NewCatalog(custom []Definition, maxCustom int, minCreateInterval time.Duration) Catalog {
	return Catalog{
		Custom:            slices.Clone(custom),
		MaxCustom:         maxCustom,
		MinCreateInterval: minCreateInterval,
	}
}

func (c Catalog) maxCustom() int {
	if c.MaxCustom <= 0 {
		return DefaultMaxCustom
	}
	return c.MaxCustom
}

func (c Catalog) minInterval() time.Duration {
	if c.MinCreateInterval <= 0 {
		return DefaultMinCreateInterval
	}
	return c.MinCreateInterval
}

// Keys returns all allowed keys in deterministic order: built-ins followed by custom
// definitions in creation/config order.
func (c Catalog) Keys() []string {
	out := slices.Clone(Defaults)
	for _, d := range c.Custom {
		out = append(out, d.Key)
	}
	return out
}

// Allowed reports whether key is one of the built-ins or configured custom types.
func (c Catalog) Allowed(key string) bool {
	return slices.Contains(c.Keys(), key)
}

// Validate verifies all persisted custom definitions without requiring timestamps on old
// configs. A built-in cannot be shadowed, and every custom key must be unique.
func (c Catalog) Validate() error {
	if len(c.Custom) > c.maxCustom() {
		return fmt.Errorf("%w: %d > %d", ErrCustomTypeLimit, len(c.Custom), c.maxCustom())
	}
	seen := make(map[string]struct{}, len(c.Custom))
	for _, d := range c.Custom {
		if err := ValidateKey(d.Key); err != nil {
			return err
		}
		if slices.Contains(Defaults, d.Key) {
			return fmt.Errorf("%w: built-in %q", ErrTypeExists, d.Key)
		}
		if d.DisplayName != "" {
			if err := ValidateDisplayName(d.DisplayName); err != nil {
				return err
			}
		}
		if _, ok := seen[d.Key]; ok {
			return fmt.Errorf("%w: %s", ErrTypeExists, d.Key)
		}
		seen[d.Key] = struct{}{}
	}
	return nil
}

// Create explicitly adds one custom type. The catalog itself carries the recent creation
// history through Definition.CreatedAt, so a fresh Store process cannot bypass the rate
// limit. The returned catalog is not mutated until the caller assigns it/persists it.
func (c Catalog) Create(key, actor string, at time.Time) (Catalog, Definition, error) {
	return c.CreateWithDisplayName(key, "", actor, at)
}

// CreateWithDisplayName explicitly creates a custom slug and an optional human-facing
// display name. The key remains a stable ASCII identifier for agents/API clients; the
// display name accepts controlled Unicode so Chinese and other localized UI labels do not
// need to be encoded into the key.
func (c Catalog) CreateWithDisplayName(key, displayName, actor string, at time.Time) (Catalog, Definition, error) {
	key = strings.TrimSpace(strings.ToLower(key))
	if err := ValidateKey(key); err != nil {
		return c, Definition{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		if err := ValidateDisplayName(displayName); err != nil {
			return c, Definition{}, err
		}
	}
	if c.Allowed(key) {
		return c, Definition{}, fmt.Errorf("%w: %s", ErrTypeExists, key)
	}
	if len(c.Custom) >= c.maxCustom() {
		return c, Definition{}, fmt.Errorf("%w: max %d", ErrCustomTypeLimit, c.maxCustom())
	}
	if latest, ok := c.latestCreation(); ok && at.UTC().Before(latest.Add(c.minInterval())) {
		return c, Definition{}, fmt.Errorf("%w: retry after %s", ErrCreationRateLimit, latest.Add(c.minInterval()).UTC().Format(time.RFC3339))
	}
	d := Definition{Key: key, DisplayName: displayName, CreatedAt: at.UTC().Format(time.RFC3339), CreatedBy: actor}
	next := c
	next.Custom = append(slices.Clone(c.Custom), d)
	return next, d, nil
}

// ValidateDisplayName permits readable Unicode labels while excluding blank/control-only
// values and bounding storage/UI abuse. It deliberately does not normalize user language.
func ValidateDisplayName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("%w: %q", ErrInvalidDisplayName, name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %q", ErrInvalidDisplayName, name)
		}
	}
	return nil
}

func (c Catalog) latestCreation() (time.Time, bool) {
	var latest time.Time
	found := false
	for _, d := range c.Custom {
		if d.CreatedAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, d.CreatedAt)
		if err != nil {
			continue // validation intentionally tolerates legacy timestamp-less entries
		}
		if !found || parsed.After(latest) {
			latest, found = parsed, true
		}
	}
	return latest, found
}

// ValidateKey accepts stable, URL/YAML-friendly keys. The normalization in Create makes
// user input lower-case; direct config validation intentionally requires already-normalized
// values so a manual typo is visible instead of silently changing stored data.
func ValidateKey(key string) error {
	if key == "" || len(key) > 48 || strings.TrimSpace(key) != key || strings.ToLower(key) != key {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, r := range key {
		if unicode.IsLower(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return nil
}

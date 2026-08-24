// Package config loads and saves .carbon/config.yaml and owns id minting (SPEC §3).
// It is a leaf package: it does not import task, so it defines no gate logic — the
// store/mcp layer maps a Config into task.Rules. config.yaml is engine-owned and small,
// so a plain struct round-trip is fine here (unlike task files, which need lossless
// node-level writes per SPEC §8).
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tasktypes "carbon/internal/types"

	"gopkg.in/yaml.v3"
)

// ErrInvalidConfig wraps every validation failure so callers match with errors.Is.
var ErrInvalidConfig = errors.New("invalid config")

// ErrUnsafeConfigPath reports a config output path that is not a normal file below a
// real directory. Store callers resolve the path through their own managed-path helper;
// this additional guard keeps direct config.Save callers from publishing through a
// symlink, junction, or other reparse point.
var ErrUnsafeConfigPath = errors.New("unsafe config path")

// ErrConfigWritePublished means a new config file is already visible but its final
// durability confirmation failed. Callers must reload before retrying rather than
// assuming the old file survived.
var ErrConfigWritePublished = errors.New("config write published but durability is unconfirmed")

// Config mirrors config.yaml (SPEC §3). states are user-defined free strings; there is
// no hardcoded status enum. closed must be a subset of states; initial must be a state.
type Config struct {
	// ProjectID is a stable project scope used by newly-created tasks. Empty is valid
	// for legacy/cluster-wide task files, so loading an older config never requires a
	// migration.
	ProjectID           string   `yaml:"project_id,omitempty"`
	Prefix              string   `yaml:"prefix"`
	Counter             int      `yaml:"counter"` // deprecated: ids are now time-ordered (store.mintTaskID); kept only so existing config.yaml still parses.
	States              []string `yaml:"states"`
	Closed              []string `yaml:"closed"`
	Initial             string   `yaml:"initial"`
	CheckTimeoutDefault int      `yaml:"check_timeout_default"`
	CheckShell          string   `yaml:"check_shell,omitempty"` // shell for cmd checks; empty ⇒ sh (CARBON_SHELL env overrides; CAIRN_SHELL is legacy fallback)
	WorkingState        string   `yaml:"working_state,omitempty"`
	ReviewState         string   `yaml:"review_state,omitempty"`
	SessionHeartbeat    int      `yaml:"session_heartbeat_interval,omitempty"`
	SessionStaleAfter   int      `yaml:"session_stale_after,omitempty"`
	// TaskTypes are additive custom type definitions. Built-ins live in internal/types
	// and are always accepted even when this list is absent in old config.yaml files.
	TaskTypes                 []tasktypes.Definition `yaml:"task_types,omitempty"`
	TaskTypeMaxCustom         int                    `yaml:"task_type_max_custom,omitempty"`
	TaskTypeCreateMinInterval int                    `yaml:"task_type_create_min_interval,omitempty"` // seconds
	// TrashRetentionDays controls automatic garbage collection of already-trashed
	// tasks. A zero/missing value intentionally resolves to the safe 30-day default.
	TrashRetentionDays int `yaml:"trash_retention_days,omitempty"`

	// IdentityMode enables the optional Worker identity registry and its task-type
	// ownership guard. It intentionally defaults to false so existing projects and
	// older config.yaml files retain their historical, unrestricted behaviour.
	IdentityMode bool `yaml:"identity_mode,omitempty"`

	// node retains the original frontmatter-level YAML representation when Config came
	// from Load. Save merges known engine keys into this node so future/third-party keys
	// and their comments survive config updates just like task frontmatter does.
	node yaml.Node
}

// Default returns the standard starting config for a freshly initialized repo. The
// prefix is caller-supplied (derived from the project name); the rest are sensible v0
// defaults matching SPEC §3.
func Default(prefix string) Config {
	return Config{
		ProjectID:                 StableProjectID(prefix),
		Prefix:                    prefix,
		Counter:                   0,
		States:                    []string{"backlog", "in_progress", "in_review", "done", "canceled"},
		Closed:                    []string{"done", "canceled"},
		Initial:                   "backlog",
		CheckTimeoutDefault:       120,
		WorkingState:              "in_progress",
		ReviewState:               "in_review",
		SessionHeartbeat:          30,
		SessionStaleAfter:         180,
		TaskTypeMaxCustom:         tasktypes.DefaultMaxCustom,
		TaskTypeCreateMinInterval: int(tasktypes.DefaultMinCreateInterval / time.Second),
		TrashRetentionDays:        30,
	}
}

// StableProjectID derives a deterministic default for freshly initialized repositories.
// It is persisted in config.yaml by Default/Save, so subsequent prefix changes cannot
// alter it. Callers may deliberately leave ProjectID empty for cluster-wide tasks.
func StableProjectID(prefix string) string {
	base := strings.TrimSpace(strings.ToLower(prefix))
	if base == "" {
		base = "carbon"
	}
	sum := sha256.Sum256([]byte("carbon-project-id-v1:" + base))
	return "prj_" + hex.EncodeToString(sum[:8])
}

// LegacyStableProjectID exposes the historic derivation for importing or checking
// persisted legacy metadata. New repositories must use StableProjectID instead.
func LegacyStableProjectID(prefix string) string {
	base := strings.TrimSpace(strings.ToLower(prefix))
	if base == "" {
		base = "cairn"
	}
	sum := sha256.Sum256([]byte("cairn-project-id-v1:" + base))
	return "prj_" + hex.EncodeToString(sum[:8])
}

// Load reads and validates config.yaml.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	var c Config
	if err := node.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: decode %s: %w", path, err)
	}
	c.node = node
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save writes config back to path. When the Config came from Load it preserves unknown
// YAML keys/comments by surgically merging known fields into the parsed yaml.Node. A
// freshly constructed Config still marshals normally.
func Save(path string, c Config) error {
	b, err := render(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	return atomicWrite(path, b)
}

const configWriteFileMode = 0o600

// atomicWrite uses the same durable publication sequence as Store-owned records:
// private same-directory temp file, explicit permissions, write, Sync, Close, and one
// replace operation. There is deliberately no remove-then-rename fallback: a failed
// Windows replacement leaves the previous config.yaml visible and intact.
func atomicWrite(path string, data []byte) error {
	return atomicWriteWithDurability(path, data, atomicReplace, syncAtomicParent, validateConfigWritePath)
}

func atomicWriteWithReplace(path string, data []byte, replace func(from, to string) error) (err error) {
	return atomicWriteWithDurability(path, data, replace, syncAtomicParent, nil)
}

func atomicWriteWithDurability(path string, data []byte, replace func(from, to string) error, syncParent func(string) error, validatePath func(string) error) (err error) {
	if replace == nil {
		return errors.New("config: atomic replacement function is required")
	}
	if syncParent == nil {
		return errors.New("config: atomic parent sync function is required")
	}
	path = filepath.Clean(path)
	if validatePath != nil {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	if err := validateAtomicTarget(path, true); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "carbon-config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create atomic temp: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	published := false
	tempIdentity, err := captureAtomicTempIdentity(tmp)
	if err != nil {
		_ = tmp.Close()
		// Without the original file identity, do not unlink a pathname that could
		// have been replaced; the empty randomized staging file is harmless.
		return fmt.Errorf("config: inspect newly created atomic temp: %w", err)
	}
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !published {
			// Cleanup is identity-checked so a failed write never deletes an entry
			// substituted at the randomized temp pathname.
			_ = removeAtomicTempFile(tmpPath, tempIdentity)
		}
	}()

	if err := secureAtomicTempFile(tmp); err != nil {
		return fmt.Errorf("config: secure atomic temp file: %w", err)
	}
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("config: write atomic temp: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("config: write atomic temp: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("config: sync atomic temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close atomic temp: %w", err)
	}
	closed = true

	// Recheck immediately before publication. Rename replaces a final directory entry
	// rather than following it, and this makes an introduced symlink/reparse point a
	// fail-closed error instead of silently accepting a changed target.
	if validatePath != nil {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	if err := validateAtomicTarget(path, true); err != nil {
		return err
	}
	if err := validateAtomicTempFile(tmpPath, tempIdentity); err != nil {
		return err
	}
	if err := replace(tmpPath, path); err != nil {
		return fmt.Errorf("config: atomically replace %s: %w", path, err)
	}
	published = true
	if err := validateAtomicRegularFile(path, false); err != nil {
		return fmt.Errorf("%w: verify published entry %s: %w", ErrConfigWritePublished, path, err)
	}
	if err := verifyAtomicPrivateFile(path); err != nil {
		return fmt.Errorf("%w: verify published file %s: %w", ErrConfigWritePublished, path, err)
	}

	// Windows classifies parent-directory sync as best-effort because directory handles
	// are not uniformly syncable; its replacement uses MOVEFILE_WRITE_THROUGH. POSIX
	// surfaces a parent Sync failure after publication.
	if err := syncParent(dir); err != nil {
		return fmt.Errorf("%w: sync parent %s: %w", ErrConfigWritePublished, dir, err)
	}
	return nil
}

func validateAtomicTarget(path string, allowMissing bool) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: inspect parent %s: %v", ErrUnsafeConfigPath, dir, err)
	}
	if isConfigReparsePoint(dir, info) || !info.IsDir() {
		return fmt.Errorf("%w: parent is not a real directory: %s", ErrUnsafeConfigPath, dir)
	}
	return validateAtomicRegularFile(path, allowMissing)
}

// validateConfigWritePath rejects every reparse/symlink component from the volume root
// down to config.yaml, then rechecks it immediately before publication through the
// atomic writer. Direct config.Save has no Store root to trust, so it validates the
// complete physical parent chain rather than only filepath.Dir(path).
//
// This narrows pathname races but cannot fully eliminate a same-identity directory
// replacement between validation and a Win32/POSIX pathname call. The writer therefore
// never follows a final reparse point and treats post-publication durability errors as
// ErrConfigWritePublished instead of claiming a no-op.
func validateConfigWritePath(path string) error {
	if err := validateConfigAtomicPathInput(path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: resolve %s: %v", ErrUnsafeConfigPath, path, err)
	}
	abs = filepath.Clean(abs)
	if err := validateConfigAtomicPathRoot(abs); err != nil {
		return err
	}

	var chain []string
	for current := filepath.Dir(abs); ; {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		component := chain[i]
		info, err := os.Lstat(component)
		if err != nil {
			return fmt.Errorf("%w: inspect parent %s: %v", ErrUnsafeConfigPath, component, err)
		}
		if isConfigReparsePoint(component, info) || !info.IsDir() {
			return fmt.Errorf("%w: refusing symlink, reparse point, or non-directory parent %s", ErrUnsafeConfigPath, component)
		}
	}

	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrUnsafeConfigPath, abs, err)
	}
	if isConfigReparsePoint(abs, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing symlink, reparse point, or non-regular config %s", ErrUnsafeConfigPath, abs)
	}
	return nil
}

func validateAtomicTemp(path string) error {
	return validateAtomicRegularFile(path, false)
}

func validateAtomicRegularFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrUnsafeConfigPath, path, err)
	}
	if isConfigReparsePoint(path, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing symlink, reparse point, or non-regular file %s", ErrUnsafeConfigPath, path)
	}
	return nil
}

func render(c Config) ([]byte, error) {
	knownBytes, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	if c.node.Kind == 0 {
		return knownBytes, nil
	}
	var known yaml.Node
	if err := yaml.Unmarshal(knownBytes, &known); err != nil {
		return nil, err
	}
	merged := cloneNode(c.node)
	base := mapping(&merged)
	overlay := mapping(&known)
	if base == nil || overlay == nil {
		return knownBytes, nil
	}
	for _, key := range configKeys {
		if value, ok := mapValue(overlay, key); ok {
			setMapValue(base, key, cloneNode(*value))
		} else {
			removeMapValue(base, key)
		}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&merged); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var configKeys = []string{
	"project_id", "prefix", "counter", "states", "closed", "initial", "check_timeout_default",
	"check_shell", "working_state", "review_state", "session_heartbeat_interval",
	"session_stale_after", "task_types", "task_type_max_custom",
	"task_type_create_min_interval", "trash_retention_days", "identity_mode",
}

func mapping(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	if node != nil && node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func mapValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

func setMapValue(node *yaml.Node, key string, value yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = &value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &value)
}

func removeMapValue(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func cloneNode(value yaml.Node) yaml.Node {
	copy := value
	copy.Content = make([]*yaml.Node, len(value.Content))
	for i, child := range value.Content {
		if child == nil {
			continue
		}
		cloned := cloneNode(*child)
		copy.Content[i] = &cloned
	}
	copy.Alias = value.Alias
	return copy
}

// Validate enforces the §3 invariants: at least one state, a known initial state, and a
// closed set drawn from states.
func (c Config) Validate() error {
	if len(c.States) == 0 {
		return fmt.Errorf("%w: states is empty", ErrInvalidConfig)
	}
	if !c.isState(c.Initial) {
		return fmt.Errorf("%w: initial %q is not in states", ErrInvalidConfig, c.Initial)
	}
	for _, s := range c.Closed {
		if !c.isState(s) {
			return fmt.Errorf("%w: closed state %q is not in states", ErrInvalidConfig, s)
		}
	}
	for name, state := range map[string]string{"working_state": c.WorkingState, "review_state": c.ReviewState} {
		if state != "" && !c.isState(state) {
			return fmt.Errorf("%w: %s %q is not in states", ErrInvalidConfig, name, state)
		}
	}
	if err := c.TypeCatalog().Validate(); err != nil {
		return fmt.Errorf("%w: task types: %v", ErrInvalidConfig, err)
	}
	if c.TrashRetentionDays < 0 {
		return fmt.Errorf("%w: trash_retention_days cannot be negative", ErrInvalidConfig)
	}
	return nil
}

func (c Config) isState(s string) bool {
	return slices.Contains(c.States, s)
}

// CheckTimeout resolves a per-check timeout in seconds to a duration, falling back to
// check_timeout_default when the check omits one (SPEC §6).
func (c Config) CheckTimeout(perCheckSeconds int) time.Duration {
	if perCheckSeconds <= 0 {
		perCheckSeconds = c.CheckTimeoutDefault
	}
	return time.Duration(perCheckSeconds) * time.Second
}

// Working returns the configured first working state, or the first non-initial, non-closed
// state for legacy configurations.
func (c Config) Working() string {
	if c.WorkingState != "" {
		return c.WorkingState
	}
	for _, state := range c.States {
		if state != c.Initial && !slices.Contains(c.Closed, state) {
			return state
		}
	}
	return c.Initial
}

// Review returns the configured review state when one exists.
func (c Config) Review() string {
	if c.ReviewState != "" {
		return c.ReviewState
	}
	for _, state := range c.States {
		if state == "in_review" {
			return state
		}
	}
	return ""
}

// SessionStaleDuration returns the heartbeat expiry, defaulting to three minutes.
func (c Config) SessionStaleDuration() time.Duration {
	seconds := c.SessionStaleAfter
	if seconds <= 0 {
		seconds = 180
	}
	return time.Duration(seconds) * time.Second
}

// SessionHeartbeatDuration returns the cadence at which agents should heartbeat,
// defaulting to thirty seconds. It mirrors SessionStaleDuration so configs missing the
// field (e.g. a zero-value Config) still resolve a sensible interval.
func (c Config) SessionHeartbeatDuration() time.Duration {
	seconds := c.SessionHeartbeat
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

// TypeCatalog returns the effective built-in + configured custom type registry.
func (c Config) TypeCatalog() tasktypes.Catalog {
	return tasktypes.NewCatalog(
		c.TaskTypes,
		c.TaskTypeMaxCustom,
		time.Duration(c.TaskTypeCreateMinInterval)*time.Second,
	)
}

// WithTaskType adds a custom type through the catalog's explicit, quota- and
// rate-limited creation primitive. The caller must persist the returned Config.
func (c Config) WithTaskType(key, actor string, at time.Time) (Config, tasktypes.Definition, error) {
	return c.WithTaskTypeDisplayName(key, "", actor, at)
}

// WithTaskTypeDisplayName adds a stable machine key plus an optional localized display
// name. The key is what task.Type stores; display_name is presentation-only metadata.
func (c Config) WithTaskTypeDisplayName(key, displayName, actor string, at time.Time) (Config, tasktypes.Definition, error) {
	catalog, definition, err := c.TypeCatalog().CreateWithDisplayName(key, displayName, actor, at)
	if err != nil {
		return c, tasktypes.Definition{}, err
	}
	next := c
	next.TaskTypes = catalog.Custom
	return next, definition, nil
}

// TrashRetentionDuration returns the configured retention window, defaulting to 30 days
// for old configurations. Negative values are rejected by Validate before use.
func (c Config) TrashRetentionDuration() time.Duration {
	days := c.TrashRetentionDays
	if days <= 0 {
		days = 30
	}
	return time.Duration(days) * 24 * time.Hour
}

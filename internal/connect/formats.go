// Package connect turns "point this agent at Carbon" into one call: it knows where each
// AI coding agent keeps its MCP config and in what shape, and merges a Carbon server entry
// in without disturbing the rest of the file. The Carbon process itself does the writing —
// it already runs locally with the user's permissions — so the same code serves the
// desktop app and `carbon web` in a browser.
package connect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// serverConfig is the resolved Carbon MCP entry written into an agent's config file.
type serverConfig struct {
	Name string   // server key, always serverName
	Bin  string   // absolute path to the Carbon binary (sidecar-safe)
	Args []string // launch args after the binary
}

// argv is bin + args as one slice, for formats whose command is an array (OpenCode).
func (c serverConfig) argv() []string {
	return append([]string{c.Bin}, c.Args...)
}

// format reads and writes one agent-config shape. upsert merges the Carbon entry into
// existing bytes (possibly empty) preserving everything else; connected reports whether a
// local Carbon entry is bound to a given legacy repo or exact Carbon scope; has reports
// whether a Carbon or legacy Cairn key is present so legacy Disconnect can remove stale or malformed entries
// too; lang names the manual-guide syntax.
type format interface {
	upsert(existing []byte, c serverConfig) ([]byte, error)
	remove(existing []byte) ([]byte, error) // drop only Carbon's entries, keep the rest
	connected(existing []byte, repo string) bool
	connectedCarbon(existing []byte, scope CarbonScope) bool
	has(existing []byte) bool
	lang() string
}

// mcpServersJSON is the common `{ "mcpServers": { "<name>": { command, args } } }` shape
// used by Claude Code (.mcp.json), Cursor (.cursor/mcp.json) and Windsurf (mcp_config.json).
type mcpServersJSON struct{}

func (mcpServersJSON) lang() string { return "json" }

func (mcpServersJSON) upsert(existing []byte, c serverConfig) ([]byte, error) {
	root, err := decodeJSON(existing)
	if err != nil {
		return nil, err
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	// New configs are canonical-only. The historical key belonged to Carbon's
	// predecessor, so removing it avoids registering duplicate local servers.
	delete(servers, legacyServerName)
	servers[serverName] = map[string]any{"command": c.Bin, "args": c.Args}
	root["mcpServers"] = servers
	return encodeJSON(root)
}

func (mcpServersJSON) remove(existing []byte) ([]byte, error) {
	return jsonNestedDelete(existing, "mcpServers", serverName, legacyServerName)
}

func (mcpServersJSON) connected(existing []byte, repo string) bool {
	entry, ok := jsonNestedEntryPreferred(existing, "mcpServers")
	if !ok {
		return false
	}
	command, ok := entry["command"].(string)
	if !ok {
		return false
	}
	args, ok := stringsFromValue(entry["args"])
	return ok && localRepoMatches(entry, command, args, repo)
}

func (mcpServersJSON) connectedCarbon(existing []byte, scope CarbonScope) bool {
	entry, ok := jsonNestedEntryPreferred(existing, "mcpServers")
	if !ok {
		return false
	}
	command, ok := entry["command"].(string)
	if !ok {
		return false
	}
	args, ok := stringsFromValue(entry["args"])
	return ok && localCarbonMatches(entry, command, args, scope)
}

func (mcpServersJSON) has(existing []byte) bool {
	return jsonNestedHasAny(existing, "mcpServers", serverName, legacyServerName)
}

// openCodeJSON writes OpenCode's `{ "mcp": { "<name>": { type:"local", command:[...] } } }`,
// where command is the full argv. It seeds the $schema for fresh files.
type openCodeJSON struct{}

func (openCodeJSON) lang() string { return "json" }

func (openCodeJSON) upsert(existing []byte, c serverConfig) ([]byte, error) {
	root, err := decodeJSON(existing)
	if err != nil {
		return nil, err
	}
	if _, ok := root["$schema"]; !ok {
		root["$schema"] = "https://opencode.ai/config.json"
	}
	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	delete(mcp, legacyServerName)
	mcp[serverName] = map[string]any{"type": "local", "command": c.argv(), "enabled": true}
	root["mcp"] = mcp
	return encodeJSON(root)
}

func (openCodeJSON) remove(existing []byte) ([]byte, error) {
	return jsonNestedDelete(existing, "mcp", serverName, legacyServerName)
}

func (openCodeJSON) connected(existing []byte, repo string) bool {
	entry, ok := jsonNestedEntryPreferred(existing, "mcp")
	if !ok {
		return false
	}
	if rawType, present := entry["type"]; present {
		kind, ok := rawType.(string)
		if !ok || kind != "local" {
			return false
		}
	}
	argv, ok := stringsFromValue(entry["command"])
	if !ok || len(argv) == 0 {
		return false
	}
	return localRepoMatches(entry, argv[0], argv[1:], repo)
}

func (openCodeJSON) connectedCarbon(existing []byte, scope CarbonScope) bool {
	entry, ok := jsonNestedEntryPreferred(existing, "mcp")
	if !ok {
		return false
	}
	if rawType, present := entry["type"]; present {
		kind, ok := rawType.(string)
		if !ok || kind != "local" {
			return false
		}
	}
	argv, ok := stringsFromValue(entry["command"])
	if !ok || len(argv) == 0 {
		return false
	}
	return localCarbonMatches(entry, argv[0], argv[1:], scope)
}

func (openCodeJSON) has(existing []byte) bool {
	return jsonNestedHasAny(existing, "mcp", serverName, legacyServerName)
}

// codexTOML writes `[mcp_servers.<name>]` into Codex's config.toml, preserving other tables.
type codexTOML struct{}

func (codexTOML) lang() string { return "toml" }

func (codexTOML) upsert(existing []byte, c serverConfig) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := toml.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("parse existing config: %w", err)
		}
	}
	servers, _ := root["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	delete(servers, legacyServerName)
	servers[serverName] = map[string]any{"command": c.Bin, "args": c.Args}
	root["mcp_servers"] = servers

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.SetIndentTables(true)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (codexTOML) remove(existing []byte) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := toml.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("parse existing config: %w", err)
		}
	}
	if servers, ok := root["mcp_servers"].(map[string]any); ok {
		delete(servers, serverName)
		delete(servers, legacyServerName)
		if len(servers) == 0 {
			delete(root, "mcp_servers") // don't leave an empty table header behind
		} else {
			root["mcp_servers"] = servers
		}
	}
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.SetIndentTables(true)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (codexTOML) connected(existing []byte, repo string) bool {
	root := map[string]any{}
	if toml.Unmarshal(existing, &root) != nil {
		return false
	}
	m, _ := root["mcp_servers"].(map[string]any)
	entry, ok := tomlEntryPreferred(m)
	if !ok {
		return false
	}
	command, ok := entry["command"].(string)
	if !ok {
		return false
	}
	args, ok := stringsFromValue(entry["args"])
	return ok && localRepoMatches(entry, command, args, repo)
}

func (codexTOML) connectedCarbon(existing []byte, scope CarbonScope) bool {
	root := map[string]any{}
	if toml.Unmarshal(existing, &root) != nil {
		return false
	}
	m, _ := root["mcp_servers"].(map[string]any)
	entry, ok := tomlEntryPreferred(m)
	if !ok {
		return false
	}
	command, ok := entry["command"].(string)
	if !ok {
		return false
	}
	args, ok := stringsFromValue(entry["args"])
	return ok && localCarbonMatches(entry, command, args, scope)
}

func (codexTOML) has(existing []byte) bool {
	root := map[string]any{}
	if toml.Unmarshal(existing, &root) != nil {
		return false
	}
	m, _ := root["mcp_servers"].(map[string]any)
	_, canonical := m[serverName]
	_, legacy := m[legacyServerName]
	return canonical || legacy
}

// --- JSON helpers (2-space indent, trailing newline; tolerant of empty input) ---

func decodeJSON(existing []byte) (map[string]any, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("parse existing config: %w", err)
		}
	}
	return root, nil
}

func encodeJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// jsonNestedDelete removes root[outer][serverName], dropping the outer map if it becomes
// empty, and re-encodes — preserving every other key in the file.
func jsonNestedDelete(existing []byte, outer string, names ...string) ([]byte, error) {
	root, err := decodeJSON(existing)
	if err != nil {
		return nil, err
	}
	if m, ok := root[outer].(map[string]any); ok {
		for _, name := range names {
			delete(m, name)
		}
		if len(m) == 0 {
			delete(root, outer)
		} else {
			root[outer] = m
		}
	}
	return encodeJSON(root)
}

func jsonNestedHasAny(existing []byte, outer string, names ...string) bool {
	root := map[string]any{}
	if json.Unmarshal(existing, &root) != nil {
		return false
	}
	m, _ := root[outer].(map[string]any)
	for _, name := range names {
		if _, ok := m[name]; ok {
			return true
		}
	}
	return false
}

func jsonNestedEntryPreferred(existing []byte, outer string) (map[string]any, bool) {
	root := map[string]any{}
	if json.Unmarshal(existing, &root) != nil {
		return nil, false
	}
	m, _ := root[outer].(map[string]any)
	for _, name := range []string{serverName, legacyServerName} {
		if entry, ok := m[name].(map[string]any); ok {
			return entry, true
		}
		if _, exists := m[name]; exists {
			return nil, false
		}
	}
	return nil, false
}

// tomlEntryPreferred applies the same canonical-first rule as JSON MCP configs.
func tomlEntryPreferred(servers map[string]any) (map[string]any, bool) {
	for _, name := range []string{serverName, legacyServerName} {
		if entry, ok := servers[name].(map[string]any); ok {
			return entry, true
		}
		if _, exists := servers[name]; exists {
			return nil, false
		}
	}
	return nil, false
}

// localRepoMatches checks only the serialized config; it never invokes the configured
// command. We deliberately accept an existing local binary path instead of comparing it to
// the currently running binary, since desktop sidecars and upgrades legitimately move it.
func localRepoMatches(entry map[string]any, command string, args []string, repo string) bool {
	if _, isHTTP := entry["url"]; isHTTP || !isLocalCommand(command) {
		return false
	}
	return servesRepo(args, repo)
}

// localCarbonMatches applies the same local-command guard as legacy detection, then
// requires the serialized launch arguments to match every Carbon scope boundary.
func localCarbonMatches(entry map[string]any, command string, args []string, scope CarbonScope) bool {
	if _, isHTTP := entry["url"]; isHTTP || !isLocalCommand(command) {
		return false
	}
	return servesCarbon(args, scope)
}

func isLocalCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	lower := strings.ToLower(command)
	return !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")
}

func stringsFromValue(v any) ([]string, bool) {
	values, ok := v.([]any)
	if !ok {
		return nil, false
	}
	args := make([]string, len(values))
	for i, value := range values {
		arg, ok := value.(string)
		if !ok {
			return nil, false
		}
		args[i] = arg
	}
	return args, true
}

func servesRepo(args []string, repo string) bool {
	if len(args) == 0 || args[0] != "serve" {
		return false
	}
	configuredRepo, found, valid := launchOption(args, "--repo")
	return valid && found && sameRepo(configuredRepo, repo)
}

// servesCarbon recognizes the approved stable Carbon v2 launch contract exactly. A connection that
// happens to point at the same source-tree config but a different home/cluster/project/session
// boundary is intentionally not considered connected: it would operate on a different task boundary.
func servesCarbon(args []string, scope CarbonScope) bool {
	normalized, err := normalizeCarbonScope(scope)
	if err != nil || len(args) == 0 || args[0] != "serve" {
		return false
	}
	// --repo and Carbon scope flags are mutually exclusive. Reject any form rather than
	// letting argument order choose a surprising data root.
	if _, found, valid := launchOption(args, "--repo"); found || !valid {
		return false
	}

	home, homeFound, homeValid := launchOption(args, "--home")
	cluster, clusterFound, clusterValid := launchOption(args, "--cluster")
	project, projectFound, projectValid := launchOption(args, "--project")
	compat, compatFound, compatValid := launchOption(args, "--compat-layer")
	projectSessionFound, projectSessionValid := launchSwitch(args, "--project-session")
	if !homeValid || !clusterValid || !projectValid || !compatValid || !projectSessionValid ||
		!homeFound || !compatFound || compat != CarbonCompatLayer {
		return false
	}
	if !samePath(home, normalized.Home) {
		return false
	}
	if normalized.ScopeMode == CarbonScopeModeSession {
		return projectSessionFound && !clusterFound && !projectFound
	}
	if projectSessionFound {
		return false
	}
	if normalized.ClusterID == "" {
		if clusterFound {
			return false
		}
	} else if !clusterFound || cluster != normalized.ClusterID {
		return false
	}
	if normalized.ProjectID == "" {
		return !projectFound
	}
	return projectFound && project == normalized.ProjectID
}

// launchSwitch recognizes a generated boolean switch. Carbon writes the switch as a
// bare token, so equals forms and duplicate switches are rejected during detection
// rather than accepting a config that is not the stable launch contract.
func launchSwitch(args []string, name string) (found, valid bool) {
	valid = true
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == name:
			if found {
				return true, false
			}
			found = true
		case strings.HasPrefix(arg, name+"="):
			return true, false
		case launchValueOption(arg):
			// Do not mistake an option-looking value (for example an invalid
			// `--actor --project-session`) for a bare session switch.
			if i+1 < len(args) {
				i++
			}
		}
	}
	return found, valid
}

func launchValueOption(arg string) bool {
	switch arg {
	case "--actor", "--client", "--repo", "--home", "--cluster", "--project", "--compat-layer":
		return true
	default:
		return false
	}
}

// launchOption returns an exact option's value while rejecting ambiguous duplicate,
// empty, or option-looking values. It supports both --name value and --name=value because
// all supported agent config formats preserve argv as an array.
func launchOption(args []string, name string) (value string, found, valid bool) {
	valid = true
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == name:
			if found || i+1 >= len(args) {
				return "", true, false
			}
			i++
			value = args[i]
			found = true
		case strings.HasPrefix(arg, name+"="):
			if found {
				return "", true, false
			}
			value = strings.TrimPrefix(arg, name+"=")
			found = true
		}
	}
	if found && (strings.TrimSpace(value) == "" || strings.HasPrefix(value, "--")) {
		return "", true, false
	}
	return value, found, valid
}

func sameRepo(a, b string) bool {
	return samePath(a, b)
}

func samePath(a, b string) bool {
	a = canonicalLocalPath(a)
	b = canonicalLocalPath(b)
	if a == "" || b == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// canonicalLocalPath resolves symlinks when possible. A path may not exist yet, so fall
// back to a cleaned absolute path rather than treating that configuration as malformed.
// Windows comparisons happen after this canonicalization and use EqualFold in samePath.
func canonicalLocalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	path = absolute
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

// canonicalRepoPath remains a narrow legacy alias for tests and callers in this package.
func canonicalRepoPath(path string) string { return canonicalLocalPath(path) }

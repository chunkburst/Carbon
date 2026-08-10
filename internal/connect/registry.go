package connect

import (
	"os"
	"os/exec"
	"path/filepath"
)

// serverName is the canonical MCP server key Carbon writes into every agent config.
// legacyServerName is read only so existing configurations can be migrated safely.
const (
	serverName       = "carbon"
	legacyServerName = "cairn"
)

// Mode is whether an agent supports one-click auto-connect or only a manual guide.
type Mode string

const (
	ModeAuto   Mode = "auto"   // Carbon can write this agent's config directly
	ModeManual Mode = "manual" // config location not yet verified — show a guide only
)

// sys abstracts host lookups (home dir, PATH) so detection is testable with a temp HOME
// and a stubbed PATH.
type sys struct {
	home     string
	lookPath func(string) (string, error)
}

func defaultSys() (sys, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return sys{}, err
	}
	return sys{home: h, lookPath: exec.LookPath}, nil
}

func (s sys) onPath(bin string) bool {
	if s.lookPath == nil {
		return false
	}
	_, err := s.lookPath(bin)
	return err == nil
}

func (s sys) dirExists(parts ...string) bool {
	info, err := os.Stat(filepath.Join(append([]string{s.home}, parts...)...))
	return err == nil && info.IsDir()
}

func (s sys) fileExists(parts ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{s.home}, parts...)...))
	return err == nil
}

// agent is a registry descriptor for one integration.
type agent struct {
	id      string
	name    string
	mode    Mode
	docsURL string
	format  format
	// detect reports whether the agent looks installed on this machine (nil ⇒ unknown).
	detect func(s sys) bool
	// target resolves the absolute config path for repo (project-scoped) or HOME (global).
	// nil ⇒ no auto-connect target.
	target func(s sys, repo string) string
	// trustedRoot contains the generated target during mutations. Project-scoped
	// agents use the selected repo by default; a global-only agent names its own root.
	trustedRoot func(s sys, repo string) string
}

// registry is the full catalog. Auto agents have a verified path + format; manual agents
// carry a format only to render a best-effort guide snippet. Project-scoped paths win
// (never clobber a user's global config); Windsurf has no project config so it's global.
func registry() []agent {
	projectFile := func(parts ...string) func(sys, string) string {
		return func(_ sys, repo string) string {
			return filepath.Join(append([]string{repo}, parts...)...)
		}
	}
	return []agent{
		{
			id: "claude", name: "Claude Code", mode: ModeAuto,
			docsURL: "https://docs.claude.com/en/docs/claude-code/mcp",
			format:  mcpServersJSON{},
			detect:  func(s sys) bool { return s.onPath("claude") || s.fileExists(".claude.json") || s.dirExists(".claude") },
			target:  projectFile(".mcp.json"),
		},
		{
			id: "cursor", name: "Cursor", mode: ModeAuto,
			docsURL: "https://docs.cursor.com/context/model-context-protocol",
			format:  mcpServersJSON{},
			detect:  func(s sys) bool { return s.onPath("cursor") || s.dirExists(".cursor") },
			target:  projectFile(".cursor", "mcp.json"),
		},
		{
			id: "codex", name: "Codex", mode: ModeAuto,
			docsURL: "https://github.com/openai/codex",
			format:  codexTOML{},
			detect:  func(s sys) bool { return s.onPath("codex") || s.dirExists(".codex") },
			target:  projectFile(".codex", "config.toml"),
		},
		{
			id: "windsurf", name: "Windsurf", mode: ModeAuto,
			docsURL:     "https://docs.windsurf.com/windsurf/mcp",
			format:      mcpServersJSON{},
			detect:      func(s sys) bool { return s.onPath("windsurf") || s.dirExists(".codeium", "windsurf") },
			target:      func(s sys, _ string) string { return filepath.Join(s.home, ".codeium", "windsurf", "mcp_config.json") },
			trustedRoot: func(s sys, _ string) string { return s.home },
		},
		{
			id: "opencode", name: "OpenCode", mode: ModeAuto,
			docsURL: "https://opencode.ai/docs/mcp-servers/",
			format:  openCodeJSON{},
			detect:  func(s sys) bool { return s.onPath("opencode") || s.dirExists(".config", "opencode") },
			target:  projectFile("opencode.json"),
		},
		{
			// Kilo reads a project-scoped .kilocode/mcp.json (created if missing, takes
			// precedence over its global config) in the standard mcpServers shape.
			id: "kilo", name: "Kilo Code", mode: ModeAuto,
			docsURL: "https://kilo.ai/docs/automate/mcp/using-in-kilo-code",
			format:  mcpServersJSON{},
			detect:  func(s sys) bool { return s.onPath("kilo") || s.dirExists(".config", "kilo") },
			target:  projectFile(".kilocode", "mcp.json"),
		},
		{
			// Pi's preferred project config is .mcp.json (same file + shape as Claude Code),
			// so connecting either writes one shared Carbon entry — idempotent by design.
			id: "pi", name: "Pi", mode: ModeAuto,
			docsURL: "https://pi.dev/packages/pi-mcp-adapter",
			format:  mcpServersJSON{},
			detect:  func(s sys) bool { return s.onPath("pi") || s.dirExists(".pi") },
			target:  projectFile(".mcp.json"),
		},
		{
			// Manual-only: Antigravity uses the mcpServers shape, but its exact config path
			// (~/.gemini/config/mcp_config.json per one source) and local/stdio schema aren't
			// confirmed — show an accurate guide, don't auto-write. target drives the guide
			// path hint only (Connect refuses for manual agents).
			id: "antigravity", name: "Antigravity", mode: ModeManual,
			docsURL: "https://antigravity.google/docs/mcp",
			format:  mcpServersJSON{},
			detect:  func(s sys) bool { return s.dirExists(".antigravity") || s.dirExists(".gemini") },
			target:  func(s sys, _ string) string { return filepath.Join(s.home, ".gemini", "config", "mcp_config.json") },
		},
	}
}

func find(id string) (agent, bool) {
	for _, a := range registry() {
		if a.id == id {
			return a, true
		}
	}
	return agent{}, false
}

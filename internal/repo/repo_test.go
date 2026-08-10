package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"carbon/internal/config"
)

func TestDerivePrefix(t *testing.T) {
	tests := map[string]string{
		"myproject":   "MYPROJECT",
		"web-app":     "WEBAPP",
		"My_Cool.App": "MYCOOLAPP",
		"123":         "TASK123", // ids must start with a letter
		"---":         "TASK",    // nothing usable -> fallback
	}
	for in, want := range tests {
		dir := filepath.Join(t.TempDir(), in)
		if got := DerivePrefix(dir); got != want {
			t.Errorf("DerivePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInitCreatesScaffold(t *testing.T) {
	root := t.TempDir()
	if IsInitialized(root) {
		t.Fatal("fresh dir should not be initialized")
	}
	if err := Init(root, "MYP"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !IsInitialized(root) {
		t.Fatal("should be initialized after Init")
	}

	// Dirs exist.
	for _, d := range []string{".carbon/tasks", ".carbon/runs", ".carbon/sessions", ".carbon/live"} {
		if fi, err := os.Stat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			t.Fatalf("missing dir %s: %v", d, err)
		}
	}
	// Config has the prefix and validates.
	cfg, err := config.Load(filepath.Join(root, ".carbon", "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Prefix != "MYP" || cfg.Initial != "backlog" {
		t.Fatalf("bad config: %+v", cfg)
	}
	// .gitignore ignores every ephemeral Carbon path.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	for _, entry := range gitignoreEntries {
		if !strings.Contains(string(gi), entry) {
			t.Fatalf(".gitignore missing %q:\n%s", entry, gi)
		}
	}
}

func TestInitEmptyPrefixDerives(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cool-thing")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(root, ".carbon", "config.yaml"))
	if cfg.Prefix != "COOLTHING" {
		t.Fatalf("derived prefix = %q, want COOLTHING", cfg.Prefix)
	}
}

func TestInitIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "AAA"); err != nil {
		t.Fatal(err)
	}
	// Mutate the counter, re-init, and confirm the existing config is untouched.
	cfgPath := filepath.Join(root, ".carbon", "config.yaml")
	cfg, _ := config.Load(cfgPath)
	cfg.Counter = 7
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "BBB"); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	again, _ := config.Load(cfgPath)
	if again.Prefix != "AAA" || again.Counter != 7 {
		t.Fatalf("re-Init clobbered existing config: %+v", again)
	}
	// .gitignore not duplicated.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	for _, entry := range gitignoreEntries {
		if n := strings.Count(string(gi), entry); n != 1 {
			t.Fatalf(".gitignore entry %q appears %d times, want 1", entry, n)
		}
	}
}

func TestInitWritesWorkflowAndAgentDocs(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "P"); err != nil {
		t.Fatal(err)
	}
	wf, err := os.ReadFile(filepath.Join(root, ".carbon", "WORKFLOW.md"))
	if err != nil || !strings.Contains(string(wf), "Task workflow") {
		t.Fatalf("WORKFLOW.md missing/empty: %v", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !strings.Contains(string(b), ".carbon/WORKFLOW.md") {
			t.Fatalf("%s missing workflow reference: %v", name, err)
		}
	}
}

func TestInitDoesNotOverwriteWorkflow(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".carbon"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "# custom workflow\n"
	wfPath := filepath.Join(root, ".carbon", "WORKFLOW.md")
	if err := os.WriteFile(wfPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "P"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(wfPath)
	if string(got) != custom {
		t.Fatalf("Init overwrote a custom WORKFLOW.md")
	}
}

func TestInitAppendsRefToExistingAgentDocOnce(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("# AGENTS\n\nExisting guidance.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "P"); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "P"); err != nil { // idempotent
		t.Fatal(err)
	}
	b, _ := os.ReadFile(agents)
	s := string(b)
	if !strings.Contains(s, "Existing guidance.") {
		t.Fatal("existing content lost")
	}
	if !strings.Contains(s, ".carbon/WORKFLOW.md") {
		t.Fatal("workflow ref not added")
	}
	if !strings.Contains(s, "Agent loop — required") {
		t.Fatal("agent-loop block not embedded")
	}
	if n := strings.Count(s, agentLoopStart); n != 1 {
		t.Fatalf("agent-loop block added %d times, want 1 (idempotent in-place refresh)", n)
	}
}

func TestInitMigratesLegacyCairnAgentLoopMarkers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	legacy := "# Existing\n\n" + legacyAgentLoopStart + "\nold guidance\n" + legacyAgentLoopEnd + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, "P"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if strings.Contains(content, legacyAgentLoopStart) || strings.Contains(content, legacyAgentLoopEnd) {
		t.Fatalf("legacy Cairn marker remained after safe migration:\n%s", content)
	}
	if strings.Count(content, agentLoopStart) != 1 || !strings.Contains(content, "Carbon's MCP tools") {
		t.Fatalf("canonical Carbon loop was not installed:\n%s", content)
	}
}

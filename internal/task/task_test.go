package task

import (
	"errors"
	"strings"
	"testing"
)

// rules mirrors the example config (SPEC §3): backlog is initial; done/canceled are closed.
var rules = Rules{
	Initial: "backlog",
	Closed:  []string{"done", "canceled"},
	States:  []string{"backlog", "in_progress", "in_review", "done", "canceled"},
}

// closedTask is a tiny helper to build a dep that sits in a given state.
func mk(id, status string) Task { return Task{ID: id, Status: status} }

func graph(ts ...Task) map[string]Task {
	m := make(map[string]Task, len(ts))
	for _, t := range ts {
		m[t.ID] = t
	}
	return m
}

func passCheck() Check    { return Check{Desc: "auto", Cmd: "true", Result: "pass"} }
func pendingCheck() Check { return Check{Desc: "auto", Cmd: "true", Result: "pending"} }
func failCheck() Check    { return Check{Desc: "auto", Cmd: "false", Result: "fail"} }
func manualPending() Check {
	return Check{Desc: "human review", Type: "manual", Result: "pending"}
}

func TestValidateEvidence(t *testing.T) {
	valid := []Evidence{{Kind: "git_commit", Value: "abc123", URL: "https://example.test/commit/abc123"}, {Kind: "ci_job", Value: "build #42", Label: "CI"}}
	if err := ValidateEvidence(valid); err != nil {
		t.Fatalf("valid built-in/extension evidence = %v", err)
	}

	for _, tc := range []struct {
		name  string
		items []Evidence
	}{
		{name: "missing value", items: []Evidence{{Kind: "artifact", Value: "  "}}},
		{name: "unsafe kind", items: []Evidence{{Kind: "CI Job", Value: "42"}}},
		{name: "bad URL", items: []Evidence{{Kind: "git_url", Value: "ref", URL: "file:///tmp/proof"}}},
		{name: "long value", items: []Evidence{{Kind: "other", Value: strings.Repeat("x", 2049)}}},
		{name: "long label", items: []Evidence{{Kind: "other", Value: "x", Label: strings.Repeat("x", 257)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateEvidence(tc.items); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("ValidateEvidence(%s) = %v, want ErrInvalidEvidence", tc.name, err)
			}
		})
	}

	tooMany := make([]Evidence, MaxEvidence+1)
	for i := range tooMany {
		tooMany[i] = Evidence{Kind: "other", Value: "x"}
	}
	if err := ValidateEvidence(tooMany); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("too many evidence = %v, want ErrInvalidEvidence", err)
	}
}

func TestValidateBlockerReason(t *testing.T) {
	if err := ValidateBlockerReason("waiting for token\n\towner: infra"); err != nil {
		t.Fatalf("valid multiline blocker reason = %v", err)
	}
	for _, reason := range []string{
		"contains\x00nul",
		"contains\x01control",
		"contains\x7fdelete",
		string([]byte{0xff}),
		strings.Repeat("x", MaxBlockerReason+1),
	} {
		if err := ValidateBlockerReason(reason); !errors.Is(err, ErrInvalidBlockerReason) {
			t.Fatalf("ValidateBlockerReason(%q) = %v, want ErrInvalidBlockerReason", reason, err)
		}
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		task Task
		to   string
		all  map[string]Task
		want error // sentinel expected via errors.Is; nil = allowed
	}{
		// --- deps gate: cannot leave initial until all deps closed (SPEC §4, §5.1) ---
		{
			name: "leave initial with open dep is blocked",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}},
			to:   "in_progress",
			all:  graph(mk("D", "in_progress")),
			want: ErrDepsNotClosed,
		},
		{
			name: "leave initial with closed dep is allowed",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}},
			to:   "in_progress",
			all:  graph(mk("D", "done")),
			want: nil,
		},
		{
			name: "leave initial with no deps is allowed",
			task: Task{ID: "T", Status: "backlog"},
			to:   "in_progress",
			all:  graph(),
			want: nil,
		},
		{
			name: "stay in initial is a free no-op even with open deps",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}},
			to:   "backlog",
			all:  graph(mk("D", "in_progress")),
			want: nil,
		},

		// --- checks gate: cannot enter a closed state unless all checks pass (SPEC §5.2) ---
		{
			name: "close with a pending check is blocked",
			task: Task{ID: "T", Status: "in_review", Checks: []Check{pendingCheck()}},
			to:   "done",
			all:  graph(),
			want: ErrChecksNotPassed,
		},
		{
			name: "close with a failing check is blocked",
			task: Task{ID: "T", Status: "in_review", Checks: []Check{passCheck(), failCheck()}},
			to:   "done",
			all:  graph(),
			want: ErrChecksNotPassed,
		},
		{
			name: "close with all checks passing is allowed",
			task: Task{ID: "T", Status: "in_review", Checks: []Check{passCheck(), passCheck()}},
			to:   "done",
			all:  graph(),
			want: nil,
		},
		{
			name: "close with zero checks passes vacuously",
			task: Task{ID: "T", Status: "in_review"},
			to:   "done",
			all:  graph(),
			want: nil,
		},
		{
			name: "close with a pending manual check is blocked",
			task: Task{ID: "T", Status: "in_review", Checks: []Check{manualPending()}},
			to:   "canceled",
			all:  graph(),
			want: ErrChecksNotPassed,
		},

		// --- free intermediate transitions: no gate applies (SPEC §5) ---
		{
			name: "non-initial to non-closed is free even with open deps",
			task: Task{ID: "T", Status: "in_progress", Deps: []string{"D"}},
			to:   "in_review",
			all:  graph(mk("D", "in_progress")),
			want: nil,
		},

		// --- combined: leaving initial straight into closed applies BOTH gates (SPEC §5) ---
		{
			name: "backlog to done directly with both gates passing",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}, Checks: []Check{passCheck()}},
			to:   "done",
			all:  graph(mk("D", "done")),
			want: nil,
		},
		{
			name: "backlog to done blocked by deps gate first",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}, Checks: []Check{passCheck()}},
			to:   "done",
			all:  graph(mk("D", "in_progress")),
			want: ErrDepsNotClosed,
		},
		{
			name: "backlog to done blocked by checks gate when deps satisfied",
			task: Task{ID: "T", Status: "backlog", Deps: []string{"D"}, Checks: []Check{failCheck()}},
			to:   "done",
			all:  graph(mk("D", "done")),
			want: ErrChecksNotPassed,
		},

		// --- reopen is free; check results retained, so re-close reuses them (SPEC §5) ---
		{
			name: "reopen closed task is a free transition",
			task: Task{ID: "T", Status: "done", Checks: []Check{passCheck()}},
			to:   "in_progress",
			all:  graph(),
			want: nil,
		},
		{
			name: "re-close after reopen reuses retained passing results",
			task: Task{ID: "T", Status: "in_progress", Checks: []Check{passCheck()}},
			to:   "done",
			all:  graph(),
			want: nil,
		},

		// --- unknown target state (SPEC §3: states are the only valid targets) ---
		{
			name: "unknown target state is rejected",
			task: Task{ID: "T", Status: "backlog"},
			to:   "shipped",
			all:  graph(),
			want: ErrUnknownState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CanTransition(tt.task, tt.to, tt.all, rules)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CanTransition(%q -> %q) = %v, want errors.Is %v", tt.task.Status, tt.to, err, tt.want)
			}
		})
	}
}

// TestCanTransitionReviewGate covers the handoff gate: entering the review state requires
// every COMMAND check to pass, while manual checks stay exempt until attested during review.
func TestCanTransitionReviewGate(t *testing.T) {
	reviewRules := Rules{
		Initial: "backlog",
		Closed:  []string{"done", "canceled"},
		States:  []string{"backlog", "in_progress", "in_review", "done", "canceled"},
		Review:  "in_review",
	}
	tests := []struct {
		name string
		task Task
		to   string
		want error
	}{
		{
			name: "enter review with a pending cmd check is blocked",
			task: Task{ID: "T", Status: "in_progress", Checks: []Check{pendingCheck()}},
			to:   "in_review",
			want: ErrChecksNotPassed,
		},
		{
			name: "enter review with a failing cmd check is blocked",
			task: Task{ID: "T", Status: "in_progress", Checks: []Check{passCheck(), failCheck()}},
			to:   "in_review",
			want: ErrChecksNotPassed,
		},
		{
			name: "enter review with all cmd checks passing is allowed",
			task: Task{ID: "T", Status: "in_progress", Checks: []Check{passCheck(), passCheck()}},
			to:   "in_review",
			want: nil,
		},
		{
			name: "enter review with a pending MANUAL check is allowed (attested during review)",
			task: Task{ID: "T", Status: "in_progress", Checks: []Check{passCheck(), manualPending()}},
			to:   "in_review",
			want: nil,
		},
		{
			name: "enter review with zero checks passes vacuously",
			task: Task{ID: "T", Status: "in_progress"},
			to:   "in_review",
			want: nil,
		},
		{
			name: "closing still requires the manual check even after review let it through",
			task: Task{ID: "T", Status: "in_review", Checks: []Check{passCheck(), manualPending()}},
			to:   "done",
			want: ErrChecksNotPassed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CanTransition(tt.task, tt.to, graph(), reviewRules)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CanTransition(%q -> %q) = %v, want errors.Is %v", tt.task.Status, tt.to, err, tt.want)
			}
		})
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name string
		task Task
		all  map[string]Task
		want bool
	}{
		{
			name: "no deps is ready",
			task: Task{ID: "T"},
			all:  graph(),
			want: true,
		},
		{
			name: "all deps closed is ready",
			task: Task{ID: "T", Deps: []string{"A", "B"}},
			all:  graph(mk("A", "done"), mk("B", "canceled")),
			want: true,
		},
		{
			name: "one open dep is not ready",
			task: Task{ID: "T", Deps: []string{"A", "B"}},
			all:  graph(mk("A", "done"), mk("B", "in_progress")),
			want: false,
		},
		{
			name: "missing dep is not ready (defensive; real dangling is a load error)",
			task: Task{ID: "T", Deps: []string{"A", "GHOST"}},
			all:  graph(mk("A", "done")),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Ready(tt.task, tt.all, rules); got != tt.want {
				t.Fatalf("Ready = %v, want %v", got, tt.want)
			}
		})
	}
}

// ReadyFunc/CanTransitionFunc resolve only a task's own deps on demand, so a status move
// never has to load the whole board. Assert they match the map-backed wrappers and that the
// resolver is asked for exactly the listed deps.
func TestReadyFuncResolvesOnlyListedDeps(t *testing.T) {
	all := graph(mk("A", "done"), mk("B", "in_progress"), mk("UNRELATED", "done"))
	asked := map[string]int{}
	resolve := func(id string) (Task, bool) {
		asked[id]++
		x, ok := all[id]
		return x, ok
	}

	// Deps all closed -> ready; only A is queried, not the unrelated task.
	ready := Task{ID: "T", Deps: []string{"A"}}
	if !ReadyFunc(ready, resolve, rules) {
		t.Fatalf("ReadyFunc = false, want true")
	}
	if asked["A"] == 0 || asked["UNRELATED"] != 0 {
		t.Fatalf("resolver asked %v, want A queried and UNRELATED untouched", asked)
	}

	// One open dep blocks leaving the initial state.
	blocked := Task{ID: "T", Status: rules.Initial, Deps: []string{"B"}}
	if err := CanTransitionFunc(blocked, "in_progress", resolve, rules); !errors.Is(err, ErrDepsNotClosed) {
		t.Fatalf("CanTransitionFunc err = %v, want ErrDepsNotClosed", err)
	}

	// Closed dep allows it.
	okTask := Task{ID: "T", Status: rules.Initial, Deps: []string{"A"}}
	if err := CanTransitionFunc(okTask, "in_progress", resolve, rules); err != nil {
		t.Fatalf("CanTransitionFunc err = %v, want nil", err)
	}
}

func TestValidateDeps(t *testing.T) {
	tests := []struct {
		name string
		all  map[string]Task
		want error
	}{
		{
			name: "valid DAG",
			all:  graph(Task{ID: "A"}, Task{ID: "B", Deps: []string{"A"}}, Task{ID: "C", Deps: []string{"A", "B"}}),
			want: nil,
		},
		{
			name: "dangling dep",
			all:  graph(Task{ID: "A", Deps: []string{"GHOST"}}),
			want: ErrDanglingDep,
		},
		{
			name: "two-node cycle",
			all:  graph(Task{ID: "A", Deps: []string{"B"}}, Task{ID: "B", Deps: []string{"A"}}),
			want: ErrCycle,
		},
		{
			name: "self loop",
			all:  graph(Task{ID: "A", Deps: []string{"A"}}),
			want: ErrCycle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateDeps(tt.all); !errors.Is(err, tt.want) {
				t.Fatalf("ValidateDeps = %v, want errors.Is %v", err, tt.want)
			}
		})
	}
}

func TestValidateParents(t *testing.T) {
	if err := ValidateParents(graph(Task{ID: "A"}, Task{ID: "B", Parent: "A"})); err != nil {
		t.Fatalf("valid parent: %v", err)
	}
	if err := ValidateParents(graph(Task{ID: "A", Parent: "GHOST"})); !errors.Is(err, ErrParentMissing) {
		t.Fatalf("missing parent: %v", err)
	}
	if err := ValidateParents(graph(Task{ID: "A", Parent: "A"})); !errors.Is(err, ErrParentCycle) {
		t.Fatalf("self parent: %v", err)
	}
	if err := ValidateParents(graph(Task{ID: "A", Parent: "B"}, Task{ID: "B", Parent: "A"})); !errors.Is(err, ErrParentCycle) {
		t.Fatalf("parent cycle: %v", err)
	}
}

func TestValidateDeletable(t *testing.T) {
	all := graph(
		Task{ID: "A"},
		Task{ID: "B", Parent: "A"},
		Task{ID: "C", Deps: []string{"A"}},
		Task{ID: "D"},
	)
	if err := ValidateDeletable("D", all); err != nil {
		t.Fatalf("leaf D should be deletable: %v", err)
	}
	if err := ValidateDeletable("A", all); !errors.Is(err, ErrHasChildren) {
		t.Fatalf("A has child B: %v, want ErrHasChildren", err)
	}
	// Remove the child so the dependents rule is what blocks A.
	noChild := graph(Task{ID: "A"}, Task{ID: "C", Deps: []string{"A"}})
	if err := ValidateDeletable("A", noChild); !errors.Is(err, ErrHasDependents) {
		t.Fatalf("A has dependent C: %v, want ErrHasDependents", err)
	}
}

func TestImportanceAndTypeStayIndependentFromPriority(t *testing.T) {
	if !ValidImportanceKey("core") || ValidImportanceKey("urgent") || !ValidImportance("") {
		t.Fatalf("importance validation is incorrect")
	}
	if !ValidType("plugin", nil) || !ValidType("review_pack", []string{"review_pack"}) || ValidType("review_pack", nil) {
		t.Fatalf("type validation is incorrect")
	}
	if !ValidPriority("urgent") || ValidPriority("core") {
		t.Fatalf("priority unexpectedly accepts importance keys")
	}
}

package home

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestWorkerAliasesMissingFileIsEmptyAndDoesNotWrite(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}

	aliases, err := ListWorkerAliases(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Fatalf("missing aliases = %#v, want empty", aliases)
	}
	if _, err := os.Lstat(filepath.Join(root, CarbonDirName, WorkerAliasesFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read of missing aliases created a file: %v", err)
	}
}

func TestWorkerAliasesRoundTripTrimDeleteAndLeaveManifestUntouched(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, CarbonDirName, ManifestFilename)
	beforeManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	aliases, err := SetWorkerAlias(root, "agent:codex", "  codex1  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases["agent:codex"]; got != "codex1" {
		t.Fatalf("stored alias = %q, want trimmed codex1", got)
	}

	h, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	aliases, err = h.ListWorkerAliases()
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases["agent:codex"]; got != "codex1" {
		t.Fatalf("handle read alias = %q, want codex1", got)
	}

	aliases, err = h.SetWorkerAlias("agent:codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Fatalf("delete aliases = %#v, want empty", aliases)
	}
	afterManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterManifest) != string(beforeManifest) {
		t.Fatalf("worker aliases modified home manifest\nbefore=%s\nafter=%s", beforeManifest, afterManifest)
	}

	data, err := os.ReadFile(filepath.Join(root, CarbonDirName, WorkerAliasesFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\n  \"version\": 1,\n  \"aliases\": {}\n}\n"; got != want {
		t.Fatalf("alias document = %q, want %q", got, want)
	}
}

func TestWorkerAliasesAreIndependentPerHome(t *testing.T) {
	useHomeTestCache(t)
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if _, err := Ensure(root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SetWorkerAlias(first, "agent:codex", "codex1"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetWorkerAlias(second, "agent:codex", "codex2"); err != nil {
		t.Fatal(err)
	}
	firstAliases, err := ListWorkerAliases(first)
	if err != nil {
		t.Fatal(err)
	}
	secondAliases, err := ListWorkerAliases(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstAliases["agent:codex"] != "codex1" || secondAliases["agent:codex"] != "codex2" {
		t.Fatalf("home aliases leaked: first=%#v second=%#v", firstAliases, secondAliases)
	}
}

func TestWorkerAliasesRejectInvalidAndDuplicateAliasesWithoutMutation(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	if _, err := SetWorkerAlias(root, "agent:codex", "codex1"); err != nil {
		t.Fatal(err)
	}

	for _, request := range []struct {
		actor string
		alias string
	}{
		{"", "empty actor"},
		{" agent:codex", "leading actor space"},
		{"agent:codex\nother", "control actor"},
		{strings.Repeat("a", maxWorkerActorBytes+1), "long actor"},
		{"agent:other", "alias\ncontrol"},
		{"agent:other", strings.Repeat("a", maxWorkerAliasBytes+1)},
		{"agent:other", "CODEX1"},
	} {
		if _, err := SetWorkerAlias(root, request.actor, request.alias); !errors.Is(err, ErrInvalidWorkerAlias) {
			t.Fatalf("SetWorkerAlias(%q, %q) = %v, want ErrInvalidWorkerAlias", request.actor, request.alias, err)
		}
	}

	aliases, err := ListWorkerAliases(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases["agent:codex"] != "codex1" {
		t.Fatalf("invalid mutation changed aliases: %#v", aliases)
	}
}

func TestWorkerAliasesFailClosedForMalformedDocuments(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, CarbonDirName, WorkerAliasesFilename)
	for _, test := range []struct {
		name string
		data string
		want error
	}{
		{
			name: "duplicate aliases",
			data: `{"version":1,"aliases":{"agent:first":"Codex","agent:second":"codex"}}`,
			want: ErrInvalidWorkerAliases,
		},
		{
			name: "unknown field",
			data: `{"version":1,"aliases":{},"surprise":true}`,
			want: ErrInvalidWorkerAliases,
		},
		{
			name: "duplicate JSON key",
			data: `{"version":1,"aliases":{},"aliases":{}}`,
			want: ErrInvalidWorkerAliases,
		},
		{
			name: "future version",
			data: `{"version":2,"aliases":{}}`,
			want: ErrFutureWorkerAliasesVersion,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ListWorkerAliases(root); !errors.Is(err, test.want) {
				t.Fatalf("ListWorkerAliases = %v, want %v", err, test.want)
			}
			if _, err := SetWorkerAlias(root, "agent:codex", "codex1"); !errors.Is(err, test.want) {
				t.Fatalf("SetWorkerAlias on malformed document = %v, want %v", err, test.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("malformed document was changed\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

func TestWorkerAliasesConcurrentMutationsPreserveEveryActor(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		identity := strconv.Itoa(i)
		actor := "agent:worker-" + identity
		alias := "worker-" + identity
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := SetWorkerAlias(root, actor, alias)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SetWorkerAlias = %v", err)
		}
	}
	aliases, err := ListWorkerAliases(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != workers {
		t.Fatalf("concurrent aliases length = %d, want %d: %#v", len(aliases), workers, aliases)
	}
	for i := 0; i < workers; i++ {
		identity := strconv.Itoa(i)
		actor := "agent:worker-" + identity
		if got, want := aliases[actor], "worker-"+identity; got != want {
			t.Fatalf("alias for %s = %q, want %q", actor, got, want)
		}
	}
}

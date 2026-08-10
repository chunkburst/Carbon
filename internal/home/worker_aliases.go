package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// WorkerAliasesFilename is the home-global, presentation-only worker alias file.
	// It is intentionally separate from home.json: actor identity is not project
	// metadata and an alias must follow a user across every cluster in one home.
	WorkerAliasesFilename = "worker-aliases.json"
	// WorkerAliasesVersion is the only supported worker-alias file schema.
	WorkerAliasesVersion = 1

	maxWorkerAliasesBytes = 1 << 20
	maxWorkerActorBytes   = 256
	maxWorkerAliasBytes   = 256
	maxWorkerAliases      = 4096
)

var (
	// ErrInvalidWorkerAlias is returned for an invalid alias mutation request. It is
	// deliberately distinct from malformed on-disk data so callers can show an input
	// error without treating a durable metadata corruption as user text to normalise.
	ErrInvalidWorkerAlias = errors.New("invalid Carbon worker alias")
	// ErrInvalidWorkerAliases means the independent alias document is malformed or
	// semantically ambiguous. It is never repaired implicitly.
	ErrInvalidWorkerAliases = errors.New("invalid Carbon worker aliases")
	// ErrFutureWorkerAliasesVersion prevents an older Carbon binary from rewriting an
	// alias file it cannot fully understand.
	ErrFutureWorkerAliasesVersion = errors.New("unsupported future Carbon worker aliases version")
)

// WorkerAliasesFile is the durable v1 wire form. It deliberately has no actor or
// project metadata: keys are existing canonical actor identifiers and values are only
// user-facing presentation aliases.
type WorkerAliasesFile struct {
	Version int               `json:"version"`
	Aliases map[string]string `json:"aliases"`
}

type workerAliasesWire struct {
	Version *int            `json:"version"`
	Aliases json.RawMessage `json:"aliases"`
}

// ListWorkerAliases returns a defensive copy of this home's worker aliases. A missing
// file is the normal empty state and does not create any metadata on disk.
func ListWorkerAliases(main string) (map[string]string, error) {
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}
	carbonRoot, err := workerAliasesCarbonRoot(root)
	if err != nil {
		return nil, err
	}
	aliases, err := readWorkerAliases(carbonRoot)
	if err != nil {
		return nil, err
	}
	return cloneWorkerAliases(aliases), nil
}

// ListWorkerAliases is the Home-handle equivalent of the package function.
func (h *Home) ListWorkerAliases() (map[string]string, error) {
	if h == nil {
		return nil, ErrNotInitialized
	}
	return ListWorkerAliases(h.Root)
}

// SetWorkerAlias creates or updates one presentation alias. actor is never trimmed,
// lower-cased, or otherwise canonicalised: it remains the task/session actor identity.
// An empty alias removes the alias for that actor. All mutations serialize through the
// same per-home lock as the manifest and use an atomic same-directory replacement.
func SetWorkerAlias(main, actor, alias string) (map[string]string, error) {
	if err := validateWorkerActor(actor); err != nil {
		return nil, err
	}
	normalizedAlias, remove, err := normalizeWorkerAlias(alias)
	if err != nil {
		return nil, err
	}
	root, err := resolveRoot(main)
	if err != nil {
		return nil, err
	}

	var result map[string]string
	err = withLock(root, func() error {
		carbonRoot, err := workerAliasesCarbonRoot(root)
		if err != nil {
			return err
		}
		aliases, err := readWorkerAliases(carbonRoot)
		if err != nil {
			return err
		}

		changed := false
		if remove {
			if _, exists := aliases[actor]; exists {
				delete(aliases, actor)
				changed = true
			}
		} else {
			if err := ensureWorkerAliasUnique(aliases, actor, normalizedAlias); err != nil {
				return err
			}
			if aliases[actor] != normalizedAlias {
				aliases[actor] = normalizedAlias
				changed = true
			}
		}

		if changed {
			if err := writeWorkerAliases(carbonRoot, aliases); err != nil {
				return err
			}
		}
		result = cloneWorkerAliases(aliases)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SetWorkerAlias is the Home-handle equivalent of the package function.
func (h *Home) SetWorkerAlias(actor, alias string) (map[string]string, error) {
	if h == nil {
		return nil, ErrNotInitialized
	}
	return SetWorkerAlias(h.Root, actor, alias)
}

func workerAliasesCarbonRoot(root string) (string, error) {
	carbonRoot, exists, err := carbonDir(root, false)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrNotInitialized
	}
	if _, exists, err := readManifest(carbonRoot); err != nil {
		return "", err
	} else if !exists {
		return "", ErrNotInitialized
	}
	return carbonRoot, nil
}

func workerAliasesPath(carbonRoot string) string {
	return filepath.Join(carbonRoot, WorkerAliasesFilename)
}

func readWorkerAliases(carbonRoot string) (map[string]string, error) {
	filename := workerAliasesPath(carbonRoot)
	if _, exists, err := safeRegularFile(carbonRoot, filename, true); err != nil {
		return nil, err
	} else if !exists {
		return map[string]string{}, nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("carbon: open worker aliases: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxWorkerAliasesBytes+1))
	if err != nil {
		return nil, fmt.Errorf("carbon: read worker aliases: %w", err)
	}
	if int64(len(data)) > maxWorkerAliasesBytes {
		return nil, fmt.Errorf("%w: worker aliases exceed %d bytes", ErrInvalidWorkerAliases, maxWorkerAliasesBytes)
	}
	return decodeWorkerAliases(data)
}

func decodeWorkerAliases(data []byte) (map[string]string, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: JSON is not valid UTF-8", ErrInvalidWorkerAliases)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("%w: ambiguous JSON: %v", ErrInvalidWorkerAliases, err)
	}
	var wire workerAliasesWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: parse JSON: %v", ErrInvalidWorkerAliases, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: multiple JSON values", ErrInvalidWorkerAliases)
		}
		return nil, fmt.Errorf("%w: parse JSON: %v", ErrInvalidWorkerAliases, err)
	}
	if wire.Version == nil || len(wire.Aliases) == 0 || bytes.Equal(bytes.TrimSpace(wire.Aliases), []byte("null")) {
		return nil, fmt.Errorf("%w: version and aliases are required", ErrInvalidWorkerAliases)
	}
	if *wire.Version > WorkerAliasesVersion {
		return nil, fmt.Errorf("%w: version %d", ErrFutureWorkerAliasesVersion, *wire.Version)
	}
	if *wire.Version != WorkerAliasesVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidWorkerAliases, *wire.Version)
	}
	var aliases map[string]string
	aliasesDecoder := json.NewDecoder(bytes.NewReader(wire.Aliases))
	if err := aliasesDecoder.Decode(&aliases); err != nil {
		return nil, fmt.Errorf("%w: parse aliases: %v", ErrInvalidWorkerAliases, err)
	}
	if err := aliasesDecoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: parse aliases: %v", ErrInvalidWorkerAliases, err)
	}
	if aliases == nil {
		return nil, fmt.Errorf("%w: aliases must be an object", ErrInvalidWorkerAliases)
	}
	if err := validateWorkerAliases(aliases); err != nil {
		return nil, err
	}
	return aliases, nil
}

func writeWorkerAliases(carbonRoot string, aliases map[string]string) error {
	if err := validateWorkerAliases(aliases); err != nil {
		return err
	}
	data, err := json.MarshalIndent(WorkerAliasesFile{
		Version: WorkerAliasesVersion,
		Aliases: cloneWorkerAliases(aliases),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("carbon: encode worker aliases: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteJSON(carbonRoot, WorkerAliasesFilename, data)
}

func validateWorkerAliases(aliases map[string]string) error {
	if aliases == nil || len(aliases) > maxWorkerAliases {
		return fmt.Errorf("%w: aliases must contain at most %d entries", ErrInvalidWorkerAliases, maxWorkerAliases)
	}
	for actor, alias := range aliases {
		if err := validateWorkerActorStored(actor); err != nil {
			return err
		}
		if err := validateWorkerAliasStored(alias); err != nil {
			return err
		}
		for otherActor, otherAlias := range aliases {
			if actor != otherActor && strings.EqualFold(alias, otherAlias) {
				return fmt.Errorf("%w: alias %q is shared by %q and %q", ErrInvalidWorkerAliases, alias, actor, otherActor)
			}
		}
	}
	return nil
}

func validateWorkerActor(actor string) error {
	if !validWorkerActor(actor) {
		return fmt.Errorf("%w: actor must be a non-empty canonical identifier", ErrInvalidWorkerAlias)
	}
	return nil
}

func validateWorkerActorStored(actor string) error {
	if !validWorkerActor(actor) {
		return fmt.Errorf("%w: invalid canonical actor %q", ErrInvalidWorkerAliases, actor)
	}
	return nil
}

func validWorkerActor(actor string) bool {
	if actor == "" || len(actor) > maxWorkerActorBytes || !utf8.ValidString(actor) || strings.TrimSpace(actor) != actor {
		return false
	}
	for _, r := range actor {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func normalizeWorkerAlias(alias string) (normalized string, remove bool, err error) {
	// Check the caller text before trimming so a newline or tab cannot be silently
	// converted into a distinct durable alias. Ordinary surrounding spaces are trimmed.
	for _, r := range alias {
		if unicode.IsControl(r) {
			return "", false, fmt.Errorf("%w: alias cannot contain control characters", ErrInvalidWorkerAlias)
		}
	}
	normalized = strings.TrimSpace(alias)
	if normalized == "" {
		return "", true, nil
	}
	if len(normalized) > maxWorkerAliasBytes || !utf8.ValidString(normalized) {
		return "", false, fmt.Errorf("%w: alias must be at most %d bytes", ErrInvalidWorkerAlias, maxWorkerAliasBytes)
	}
	return normalized, false, nil
}

func validateWorkerAliasStored(alias string) error {
	if alias == "" || len(alias) > maxWorkerAliasBytes || !utf8.ValidString(alias) || strings.TrimSpace(alias) != alias {
		return fmt.Errorf("%w: invalid alias %q", ErrInvalidWorkerAliases, alias)
	}
	for _, r := range alias {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: alias contains control characters", ErrInvalidWorkerAliases)
		}
	}
	return nil
}

func ensureWorkerAliasUnique(aliases map[string]string, actor, alias string) error {
	for otherActor, otherAlias := range aliases {
		if otherActor != actor && strings.EqualFold(otherAlias, alias) {
			return fmt.Errorf("%w: alias %q is already used by %q", ErrInvalidWorkerAlias, alias, otherActor)
		}
	}
	return nil
}

func cloneWorkerAliases(aliases map[string]string) map[string]string {
	clone := make(map[string]string, len(aliases))
	for actor, alias := range aliases {
		clone[actor] = alias
	}
	return clone
}

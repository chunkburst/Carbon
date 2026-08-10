package home

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"carbon/internal/repo"

	"gopkg.in/yaml.v3"
)

// sourceCairnDir returns only a physical source task directory. Canonical .carbon is
// preferred; a legacy .cairn directory is read only when canonical storage is absent.
// Import deliberately rejects even an in-root symlink/reparse point because a snapshot
// must be auditable and must not accidentally include files outside the source project.
func sourceCairnDir(sourceRoot string) (string, bool, error) {
	root, err := resolveRoot(sourceRoot)
	if err != nil {
		return "", false, err
	}
	for _, name := range []string{repo.CarbonDirName, repo.LegacyCairnDirName} {
		filename := filepath.Join(root, name)
		info, err := os.Lstat(filename)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("%w: inspect source task directory: %v", ErrUnsafePath, err)
		}
		if isReparsePoint(filename, info) || !info.IsDir() {
			return "", false, fmt.Errorf("%w: refusing source task directory", ErrUnsafePath)
		}
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil || !samePath(resolved, filename) || !pathWithin(root, resolved) {
			return "", false, fmt.Errorf("%w: source task directory escapes its project", ErrUnsafePath)
		}
		return filename, true, nil
	}
	return "", false, nil
}

func hashTree(root string) (string, error) {
	h := sha256.New()
	if err := walkStrictTree(root, func(relative string, info os.FileInfo, data []byte) error {
		if _, err := io.WriteString(h, relative); err != nil {
			return err
		}
		if info.IsDir() {
			_, err := h.Write([]byte{0})
			return err
		}
		if _, err := h.Write([]byte{1}); err != nil {
			return err
		}
		if _, err := h.Write(data); err != nil {
			return err
		}
		_, err := h.Write([]byte{0})
		return err
	}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// walkStrictTree makes every source element explicit. It does not follow reparse points,
// and callers receive file bytes only after Lstat proved that entry regular.
func walkStrictTree(root string, visit func(relative string, info os.FileInfo, data []byte) error) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if isReparsePoint(root, info) || !info.IsDir() {
		return fmt.Errorf("%w: unsafe snapshot root %s", ErrUnsafePath, root)
	}
	return walkStrictTreeAt(root, "", visit)
}

func walkStrictTreeAt(root, relative string, visit func(relative string, info os.FileInfo, data []byte) error) error {
	filename := root
	if relative != "" {
		filename = filepath.Join(root, filepath.FromSlash(relative))
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if isReparsePoint(filename, info) {
		return fmt.Errorf("%w: refusing source reparse point %s", ErrUnsafePath, filename)
	}
	if info.IsDir() {
		if err := visit(relative, info, nil); err != nil {
			return err
		}
		entries, err := os.ReadDir(filename)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			child := entry.Name()
			if relative != "" {
				child = relative + "/" + child
			}
			if err := walkStrictTreeAt(root, child, visit); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: snapshot source is not a regular file %s", ErrUnsafePath, filename)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	return visit(relative, info, data)
}

func hashRegularFile(filename string) (string, bool, error) {
	data, exists, err := readStrictRegularFile(filename)
	if err != nil || !exists {
		return "", exists, err
	}
	return hashBytesHex(data), true, nil
}

func readStrictRegularFile(filename string) ([]byte, bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if isReparsePoint(filename, info) || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: refusing non-regular source file %s", ErrUnsafePath, filename)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func scanImportTasks(projectID, directory string) ([]taskInput, error) {
	entries, exists, err := strictReadDir(directory)
	if err != nil || !exists {
		return nil, err
	}
	var out []taskInput
	for _, entry := range entries {
		filename := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(filename)
		if err != nil {
			return nil, err
		}
		if isReparsePoint(filename, info) {
			return nil, fmt.Errorf("%w: refusing task reparse point %s", ErrUnsafePath, filename)
		}
		if info.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: task source is not regular %s", ErrUnsafePath, filename)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		id, deps, parent, attempt, err := parseImportedTask(data)
		if err != nil {
			return nil, fmt.Errorf("carbon: parse source task %s: %w", filename, err)
		}
		if filepath.Base(filename) != id+".md" || !validImportedID(id) {
			return nil, fmt.Errorf("%w: task filename/id mismatch %s", ErrInvalidMigrationPlan, filename)
		}
		out = append(out, taskInput{projectID: projectID, filename: filename, hash: hashBytesHex(data), id: id, deps: deps, parent: parent, attempt: attempt})
	}
	return out, nil
}

func scanImportSessions(projectID, directory string) ([]sessionInput, error) {
	entries, exists, err := strictReadDir(directory)
	if err != nil || !exists {
		return nil, err
	}
	var out []sessionInput
	for _, entry := range entries {
		filename := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(filename)
		if err != nil {
			return nil, err
		}
		if isReparsePoint(filename, info) {
			return nil, fmt.Errorf("%w: refusing session reparse point %s", ErrUnsafePath, filename)
		}
		if info.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: session source is not regular %s", ErrUnsafePath, filename)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		id, taskID, attemptID, err := parseImportedSession(data)
		if err != nil {
			return nil, fmt.Errorf("carbon: parse source session %s: %w", filename, err)
		}
		if filepath.Base(filename) != id+".yaml" || !validImportedID(id) || !validImportedID(taskID) || (attemptID != "" && !validImportedID(attemptID)) {
			return nil, fmt.Errorf("%w: session filename/content mismatch %s", ErrInvalidMigrationPlan, filename)
		}
		out = append(out, sessionInput{projectID: projectID, filename: filename, hash: hashBytesHex(data), id: id, taskID: taskID, attemptID: attemptID})
	}
	return out, nil
}

func populateRunImports(plan *LegacyImportPlan) error {
	local, _ := taskReferenceMaps(plan.Tasks)
	var runs []RunImport
	for _, project := range plan.Projects {
		if project.CairnPath == "" {
			continue
		}
		entries, exists, err := strictReadDir(filepath.Join(project.CairnPath, "runs"))
		if err != nil || !exists {
			if err != nil {
				return err
			}
			continue
		}
		for _, entry := range entries {
			filename := filepath.Join(project.CairnPath, "runs", entry.Name())
			info, err := os.Lstat(filename)
			if err != nil {
				return err
			}
			if isReparsePoint(filename, info) {
				return fmt.Errorf("%w: refusing run-log reparse point %s", ErrUnsafePath, filename)
			}
			if info.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: run log is not regular %s", ErrUnsafePath, filename)
			}
			sourceTaskID, targetTaskID, suffix, ok := parseRunFilename(entry.Name(), local[project.TargetID])
			if !ok {
				// Unknown historical logs are still preserved by the complete source
				// snapshot. They are not activated in the shared runs directory because
				// assigning them to a task would be guesswork.
				continue
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			runs = append(runs, RunImport{ProjectID: project.TargetID, SourceFile: filename, SourceHash: hashBytesHex(data), SourceTaskID: sourceTaskID, TargetFilename: targetTaskID + suffix})
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].ProjectID == runs[j].ProjectID {
			return runs[i].SourceFile < runs[j].SourceFile
		}
		return runs[i].ProjectID < runs[j].ProjectID
	})
	plan.Runs = runs
	return nil
}

func parseRunFilename(filename string, taskMap map[string]string) (sourceTaskID, targetTaskID, suffix string, ok bool) {
	ids := make([]string, 0, len(taskMap))
	for id := range taskMap {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if len(ids[i]) == len(ids[j]) {
			return ids[i] < ids[j]
		}
		return len(ids[i]) > len(ids[j])
	})
	for _, id := range ids {
		prefix := id + "-"
		if strings.HasPrefix(filename, prefix) {
			return id, taskMap[id], strings.TrimPrefix(filename, id), true
		}
	}
	return "", "", "", false
}

func strictReadDir(directory string) ([]os.DirEntry, bool, error) {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if isReparsePoint(directory, info) || !info.IsDir() {
		return nil, false, fmt.Errorf("%w: refusing source directory %s", ErrUnsafePath, directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, true, nil
}

func parseImportedTask(data []byte) (id string, deps []string, parent, attempt string, err error) {
	frontmatter, _, err := splitImportedFrontmatter(data)
	if err != nil {
		return "", nil, "", "", err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(frontmatter, &node); err != nil {
		return "", nil, "", "", err
	}
	mapping, err := yamlMapping(&node)
	if err != nil {
		return "", nil, "", "", err
	}
	id, _ = yamlMappingString(mapping, "id")
	parent, _ = yamlMappingString(mapping, "parent")
	attempt, _ = yamlMappingString(mapping, "active_attempt")
	deps, err = yamlMappingStrings(mapping, "deps")
	return id, deps, parent, attempt, err
}

func parseImportedSession(data []byte) (id, taskID, attemptID string, err error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return "", "", "", err
	}
	mapping, err := yamlMapping(&node)
	if err != nil {
		return "", "", "", err
	}
	id, _ = yamlMappingString(mapping, "id")
	taskID, _ = yamlMappingString(mapping, "task")
	attemptID, _ = yamlMappingString(mapping, "attempt")
	return id, taskID, attemptID, nil
}

func splitImportedFrontmatter(data []byte) ([]byte, []byte, error) {
	opening := []byte("---\n")
	newline := []byte("\n")
	closing := []byte("\n---\n")
	terminal := []byte("\n---")
	if bytes.HasPrefix(data, []byte("---\r\n")) {
		opening, newline, closing, terminal = []byte("---\r\n"), []byte("\r\n"), []byte("\r\n---\r\n"), []byte("\r\n---")
	}
	if !bytes.HasPrefix(data, opening) {
		return nil, nil, errors.New("missing task frontmatter")
	}
	rest := data[len(opening):]
	if i := bytes.Index(rest, closing); i >= 0 {
		return rest[:i+len(newline)], rest[i+len(closing):], nil
	}
	if bytes.HasSuffix(rest, terminal) {
		return rest[:len(rest)-len("---")], nil, nil
	}
	return nil, nil, errors.New("missing task frontmatter closing fence")
}

func yamlMapping(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("expected YAML mapping")
	}
	return node, nil
}

func yamlMappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func yamlMappingString(mapping *yaml.Node, key string) (string, bool) {
	value, ok := yamlMappingValue(mapping, key)
	if !ok || value.Kind != yaml.ScalarNode {
		return "", false
	}
	return value.Value, true
}

func yamlMappingStrings(mapping *yaml.Node, key string) ([]string, error) {
	value, ok := yamlMappingValue(mapping, key)
	if !ok {
		return nil, nil
	}
	if value.Kind != yaml.SequenceNode {
		return nil, errors.New("expected sequence")
	}
	out := make([]string, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.ScalarNode {
			return nil, errors.New("expected scalar sequence value")
		}
		out = append(out, item.Value)
	}
	return out, nil
}

package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"carbon/internal/check"
	"carbon/internal/repo"
	"carbon/internal/store"
)

// stampLayout matches the leading stamp in check.Runner run-log filenames. A
// same-millisecond collision can add a -NNN suffix after this stamp.
const stampLayout = "20060102-150405.000"

// runDTO is one parsed check-run log. Output is the tail captured by the check
// runner; the header fields are parsed from the log's leading lines (SPEC §6).
type runDTO struct {
	File     string `json:"file"`
	At       string `json:"at,omitempty"`
	Cmd      string `json:"cmd,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Head     string `json:"head,omitempty"`
	Exit     int    `json:"exit"`
	TimedOut bool   `json:"timedout"`
	Duration string `json:"duration,omitempty"`
	Output   string `json:"output,omitempty"`
}

// handleRuns returns the run logs for a task, newest-first. The task file stores
// only pass/fail (SPEC §149); full output lives in gitignored .carbon/runs logs,
// which this endpoint reads and parses. A missing runs dir yields an empty list.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	svc, scope, ok := s.svcFor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := store.ValidateTaskID(id); err != nil {
		writeErr(w, err)
		return
	}
	// Run logs often contain source output and command details. Authorize the task before
	// enumerating filenames so a project-bound connection cannot probe another project's
	// diagnostics; include_cluster is a read-only, explicit opt-in.
	if _, err := svc.GetScoped(id, includeCluster(r)); err != nil {
		writeErr(w, err)
		return
	}
	if err := store.New(scope.Root).EnsureConsistentRead(); err != nil {
		writeJSON(w, http.StatusConflict, errBody(err))
		return
	}
	runsDir, matches := runLogFiles(scope.Root, id)
	// Filenames embed the stamp, so lexical-descending == newest-first.
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))

	runs := make([]runDTO, 0, len(matches))
	for _, path := range matches {
		data, err := check.ReadRunLog(scope.Root, runsDir, path)
		if err != nil {
			continue // unsafe/vanished logs are never exposed through this endpoint
		}
		runs = append(runs, parseRun(id, filepath.Base(path), string(data)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func latestRunHead(root, id string) string {
	if err := store.ValidateTaskID(id); err != nil {
		return ""
	}
	runsDir, matches := runLogFiles(root, id)
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, path := range matches {
		data, err := check.ReadRunLog(root, runsDir, path)
		if err != nil {
			continue
		}
		if head := parseRun(id, filepath.Base(path), string(data)).Head; head != "" {
			return head
		}
	}
	return ""
}

// runLogFiles enumerates only direct children of a checked, canonical runs directory.
// It intentionally returns no files when .carbon/runs is absent or unsafe: the API is a
// best-effort diagnostics view and must never make a repository reparse point readable.
func runLogFiles(root, id string) (string, []string) {
	runsDir, err := check.RunLogDir(root, filepath.Join(root, repo.CarbonDirName, "runs"))
	if err != nil {
		return "", nil
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return runsDir, nil
	}
	prefix := id + "-"
	matches := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".log") {
			matches = append(matches, filepath.Join(runsDir, name))
		}
	}
	return runsDir, matches
}

// parseRun turns a run-log file into a runDTO. The header format is fixed by
// check.Runner.writeLog; an unparseable header degrades to the raw body so output
// is never lost.
func parseRun(id, file, content string) runDTO {
	run := runDTO{File: file}

	// Timestamp comes from the filename: <id>-<stamp>.log.
	stamp := strings.TrimSuffix(strings.TrimPrefix(file, id+"-"), ".log")
	if len(stamp) > len(stampLayout) {
		stamp = stamp[:len(stampLayout)]
	}
	if at, err := time.ParseInLocation(stampLayout, stamp, time.UTC); err == nil {
		run.At = at.UTC().Format(time.RFC3339)
	}

	head, body, found := strings.Cut(content, "\n----\n")
	if !found {
		run.Output = content // no recognizable header; keep everything as output
		return run
	}
	run.Output = body

	for line := range strings.SplitSeq(head, "\n") {
		switch {
		case strings.HasPrefix(line, "cmd: "):
			run.Cmd = strings.TrimPrefix(line, "cmd: ")
		case strings.HasPrefix(line, "cwd: "):
			run.Cwd = strings.TrimPrefix(line, "cwd: ")
		case strings.HasPrefix(line, "head: "):
			run.Head = strings.TrimPrefix(line, "head: ")
		case strings.HasPrefix(line, "exit: "):
			fmt.Sscanf(line, "exit: %d  timedout: %t  duration: %s",
				&run.Exit, &run.TimedOut, &run.Duration)
		}
	}
	return run
}

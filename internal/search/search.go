// Package search provides an in-process, dependency-free task search backend. It works
// over Docs rather than an index so file edits are reflected immediately and global hosts
// can combine any number of project stores without coupling to home/cluster packages.
package search

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"carbon/internal/store"
	"carbon/internal/task"
)

type Query struct {
	Text       string   `yaml:"text,omitempty" json:"text,omitempty"`
	ProjectID  *string  `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	ClusterID  string   `yaml:"cluster_id,omitempty" json:"cluster_id,omitempty"` // informational/scoping token for callers; sources supply the mapping
	Type       string   `yaml:"type,omitempty" json:"type,omitempty"`
	Importance string   `yaml:"importance,omitempty" json:"importance,omitempty"`
	Status     string   `yaml:"status,omitempty" json:"status,omitempty"`
	Assignee   string   `yaml:"assignee,omitempty" json:"assignee,omitempty"`
	Labels     []string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

type Highlight struct {
	Field   string `json:"field"`
	Excerpt string `json:"excerpt"`
}

type Result struct {
	Task       task.Task   `json:"task"`
	Body       string      `json:"body,omitempty"`
	Score      int         `json:"score"`
	Highlights []Highlight `json:"highlights,omitempty"`
	Doc        *store.Doc  `json:"-"`
	ClusterID  string      `json:"cluster_id,omitempty"`
}

// Source is a project-supplied input for global search. ClusterID is opaque metadata: the
// search package never imports or discovers clusters itself.
type Source struct {
	Store     *store.Store
	ClusterID string
}

// Search filters and ranks already-loaded documents. It does not mutate or cache them.
func Search(docs []*store.Doc, query Query) []Result {
	needle := strings.ToLower(strings.TrimSpace(query.Text))
	results := make([]Result, 0, len(docs))
	for _, doc := range docs {
		if !matches(doc.Task, query) {
			continue
		}
		score, highlights := score(doc, needle)
		if needle != "" && score == 0 {
			continue
		}
		results = append(results, Result{Task: doc.Task, Body: doc.Body, Score: score, Highlights: highlights, Doc: doc})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Task.ProjectID != results[j].Task.ProjectID {
			return results[i].Task.ProjectID < results[j].Task.ProjectID
		}
		return results[i].Task.ID < results[j].Task.ID
	})
	return results
}

func matches(t task.Task, q Query) bool {
	if q.ProjectID != nil && t.ProjectID != *q.ProjectID {
		return false
	}
	if q.Type != "" && t.Type != q.Type {
		return false
	}
	if q.Importance != "" && t.Importance != q.Importance {
		return false
	}
	if q.Status != "" && t.Status != q.Status {
		return false
	}
	if q.Assignee != "" && t.Assignee != q.Assignee {
		return false
	}
	for _, label := range q.Labels {
		if !slices.Contains(t.Labels, label) {
			return false
		}
	}
	return true
}

func score(doc *store.Doc, needle string) (int, []Highlight) {
	if needle == "" {
		return 1, nil
	}
	var score int
	var highlights []Highlight
	add := func(field, value string, weight int) {
		if idx := strings.Index(strings.ToLower(value), needle); idx >= 0 {
			score += weight
			highlights = append(highlights, Highlight{Field: field, Excerpt: excerpt(value, idx, len(needle))})
		}
	}
	add("title", doc.Task.Title, 100)
	add("id", doc.Task.ID, 90)
	add("project_id", doc.Task.ProjectID, 70)
	add("type", doc.Task.Type, 65)
	add("importance", doc.Task.Importance, 60)
	add("assignee", doc.Task.Assignee, 55)
	for _, label := range doc.Task.Labels {
		add("label", label, 50)
	}
	add("body", doc.Body, 30)
	return score, highlights
}

func excerpt(value string, idx, needleLen int) string {
	const radius = 56
	start := max(0, idx-radius)
	end := min(len(value), idx+needleLen+radius)
	text := value[start:end]
	if start > 0 {
		text = "…" + text
	}
	if end < len(value) {
		text += "…"
	}
	return text
}

// SearchStore loads one repository fresh and applies Search.
func SearchStore(s *store.Store, query Query) ([]Result, error) {
	if s == nil {
		return nil, fmt.Errorf("search: nil store")
	}
	docs, err := s.ListDocs()
	if err != nil {
		return nil, err
	}
	return Search(docs, query), nil
}

// SearchSources combines independent project stores. If query.ClusterID is non-empty,
// only sources with the same opaque ID participate; a host can map clusters to sources
// without importing a home model into this leaf package.
func SearchSources(sources []Source, query Query) ([]Result, error) {
	var out []Result
	for _, source := range sources {
		if source.Store == nil || (query.ClusterID != "" && source.ClusterID != query.ClusterID) {
			continue
		}
		docs, err := source.Store.ListDocs()
		if err != nil {
			return nil, err
		}
		for _, result := range Search(docs, query) {
			result.ClusterID = source.ClusterID
			out = append(out, result)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].Task.ID < out[j].Task.ID
	})
	return out, nil
}

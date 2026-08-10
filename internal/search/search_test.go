package search

import (
	"testing"

	"carbon/internal/store"
	"carbon/internal/task"
)

func TestSearchFiltersAndRanksTitleBeforeBody(t *testing.T) {
	p1 := "project-a"
	docs := []*store.Doc{
		{Task: task.Task{ID: "A-1", Title: "Fix cache invalidation", ProjectID: p1, Type: "patch", Importance: "core", Labels: []string{"backend"}}, Body: "unrelated"},
		{Task: task.Task{ID: "A-2", Title: "Notes", ProjectID: p1, Type: "library", Importance: "normal", Labels: []string{"backend"}}, Body: "cache invalidation details"},
		{Task: task.Task{ID: "B-1", Title: "Fix cache", ProjectID: "project-b", Type: "patch", Importance: "core"}, Body: ""},
	}
	results := Search(docs, Query{Text: "cache", ProjectID: &p1, Labels: []string{"backend"}})
	if len(results) != 2 || results[0].Task.ID != "A-1" || results[0].Score <= results[1].Score {
		t.Fatalf("search results = %+v", results)
	}
	if len(results[0].Highlights) == 0 || results[0].Highlights[0].Field != "title" {
		t.Fatalf("missing title highlight: %+v", results[0])
	}
}

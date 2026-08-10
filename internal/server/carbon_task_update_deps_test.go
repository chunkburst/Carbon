package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCarbonTaskUpdateDepsHTTP(t *testing.T) {
	f := newProjectScopeFixture(t)
	dep := f.createTask(t, f.project1.ID, "dependency")
	target := f.createTask(t, f.project1.ID, "target")

	var updated taskDTO
	call(t, f.handler, http.MethodPost, "/api/tasks/"+target.Task.ID+"/update",
		fmt.Sprintf(`{"deps":[%q],"expectedVersion":%q}`, dep.Task.ID, target.Version()), &updated)
	if len(updated.Deps) != 1 || updated.Deps[0] != dep.Task.ID {
		t.Fatalf("successful Carbon deps update = %+v", updated)
	}

	var cleared taskDTO
	call(t, f.handler, http.MethodPost, "/api/tasks/"+target.Task.ID+"/update",
		fmt.Sprintf(`{"deps":[],"expectedVersion":%q}`, updated.Version), &cleared)
	if len(cleared.Deps) != 0 {
		t.Fatalf("empty deps did not clear dependencies: %+v", cleared.Deps)
	}

	if code, body := raw(f.handler, http.MethodPost, "/api/tasks/"+target.Task.ID+"/update",
		fmt.Sprintf(`{"deps":[%q],"expectedVersion":%q}`, "CAR-missing", cleared.Version)); code != http.StatusUnprocessableEntity {
		t.Fatalf("dangling Carbon deps = %d %s, want 422", code, body)
	}

	first := f.createTask(t, f.project1.ID, "cycle first")
	second := f.createTask(t, f.project1.ID, "cycle second")
	var firstUpdated taskDTO
	call(t, f.handler, http.MethodPost, "/api/tasks/"+first.Task.ID+"/update",
		fmt.Sprintf(`{"deps":[%q],"expectedVersion":%q}`, second.Task.ID, first.Version()), &firstUpdated)
	if code, body := raw(f.handler, http.MethodPost, "/api/tasks/"+second.Task.ID+"/update",
		fmt.Sprintf(`{"deps":[%q],"expectedVersion":%q}`, first.Task.ID, second.Version())); code != http.StatusUnprocessableEntity {
		t.Fatalf("cyclic Carbon deps = %d %s, want 422", code, body)
	}
	if len(firstUpdated.Deps) != 1 || firstUpdated.Deps[0] != second.Task.ID {
		t.Fatalf("cycle setup lost first dependency: %+v", firstUpdated)
	}

	var advanced taskDTO
	call(t, f.handler, http.MethodPost, "/api/tasks/"+target.Task.ID+"/update",
		fmt.Sprintf(`{"labels":["advance-version"],"expectedVersion":%q}`, cleared.Version), &advanced)
	staleETag := `"` + cleared.Version + `"`
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+target.Task.ID+"/update",
		strings.NewReader(fmt.Sprintf(`{"deps":[%q],"expectedVersion":%q}`, dep.Task.ID, advanced.Version)))
	request.Header.Set("If-Match", staleETag)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale If-Match = %d %s, want 409", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("ETag"), `"`+advanced.Version+`"`; got != want {
		t.Fatalf("conflict ETag = %q, want %q", got, want)
	}
}

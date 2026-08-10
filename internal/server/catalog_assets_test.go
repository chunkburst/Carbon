package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"carbon/internal/home"
)

func TestCatalogPresentationAssetHTTPPutGetDeleteContract(t *testing.T) {
	f := newCatalogPresentationHTTPFixture(t)
	path := "/api/home/presentation/project/" + f.project.ID + "/asset"
	imageData := catalogAssetHTTPPNG(t, 9, 6)

	put := catalogAssetHTTPCall(f.handler, http.MethodPut, path, imageData, "image/png")
	if put.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d %s, want 204", put.Code, put.Body.String())
	}
	etag := put.Header().Get("ETag")
	if etag == "" {
		t.Fatal("PUT omitted normalized image ETag")
	}

	get := catalogAssetHTTPCall(f.handler, http.MethodGet, path, nil, "")
	if get.Code != http.StatusOK {
		t.Fatalf("GET = %d %s, want 200", get.Code, get.Body.String())
	}
	if got := get.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("GET Content-Type = %q, want image/png", got)
	}
	if got := get.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("GET nosniff = %q", got)
	}
	if got := get.Header().Get("Cache-Control"); got != "private, max-age=0, must-revalidate" {
		t.Fatalf("GET cache = %q", got)
	}
	if got := get.Header().Get("ETag"); got != etag {
		t.Fatalf("GET ETag = %q, want %q", got, etag)
	}
	if config, format, err := image.DecodeConfig(bytes.NewReader(get.Body.Bytes())); err != nil || format != "png" || config.Width != 9 || config.Height != 6 {
		t.Fatalf("GET image = format %q dimensions %dx%d err %v", format, config.Width, config.Height, err)
	}

	conditional := httptest.NewRequest(http.MethodGet, path, nil)
	conditional.Header.Set("If-None-Match", etag)
	conditionalResponse := httptest.NewRecorder()
	f.handler.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional GET = %d %q, want empty 304", conditionalResponse.Code, conditionalResponse.Body.String())
	}

	deleteResponse := catalogAssetHTTPCall(f.handler, http.MethodDelete, path, nil, "")
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", deleteResponse.Code, deleteResponse.Body.String())
	}
	missing := catalogAssetHTTPCall(f.handler, http.MethodGet, path, nil, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE = %d %s, want 404", missing.Code, missing.Body.String())
	}
	if got := missing.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("missing GET cache = %q, want no-store", got)
	}
	// Clear stays idempotent for an optimistic UI action.
	if response := catalogAssetHTTPCall(f.handler, http.MethodDelete, path, nil, ""); response.Code != http.StatusNoContent {
		t.Fatalf("second DELETE = %d %s, want 204", response.Code, response.Body.String())
	}
}

func TestCatalogPresentationAssetHTTPRejectsSpoofsOversizeAndInvalidTargets(t *testing.T) {
	f := newCatalogPresentationHTTPFixture(t)
	path := "/api/home/presentation/cluster/" + f.cluster.ID + "/asset"
	pngData := catalogAssetHTTPPNG(t, 4, 4)
	for _, test := range []struct {
		name        string
		contentType string
		body        []byte
		want        int
	}{
		{"missing MIME", "", pngData, http.StatusUnsupportedMediaType},
		{"unsupported MIME", "image/svg+xml", []byte(`<svg/>`), http.StatusUnsupportedMediaType},
		{"MIME spoof", "image/jpeg", pngData, http.StatusUnprocessableEntity},
		{"corrupt PNG", "image/png", []byte("not a PNG"), http.StatusUnprocessableEntity},
		{"oversized raw body", "image/png", bytes.Repeat([]byte{0}, int(home.MaxCatalogPresentationAssetBytes+1)), http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := catalogAssetHTTPCall(f.handler, http.MethodPut, path, test.body, test.contentType)
			if response.Code != test.want {
				t.Fatalf("PUT = %d %s, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
	invalidPath := "/api/home/presentation/clusters/" + f.cluster.ID + "/asset"
	if response := catalogAssetHTTPCall(f.handler, http.MethodPut, invalidPath, pngData, "image/png"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid kind = %d %s, want 422", response.Code, response.Body.String())
	}
	missingID := "cluster_" + strings.Repeat("0", 32)
	if response := catalogAssetHTTPCall(f.handler, http.MethodPut, "/api/home/presentation/cluster/"+missingID+"/asset", pngData, "image/png"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown target = %d %s, want 404", response.Code, response.Body.String())
	}
}

func TestCatalogPresentationAssetHTTPRequiresHomeOnlyScope(t *testing.T) {
	f := newProjectScopeFixture(t)
	handler := catalogPresentationTestHandler(f.server)
	path := "/api/home/presentation/project/project_" + strings.Repeat("0", 32) + "/asset"
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		response := catalogAssetHTTPCall(handler, method, path, catalogAssetHTTPPNG(t, 2, 2), "image/png")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("project-scoped %s = %d %s, want 400", method, response.Code, response.Body.String())
		}
	}
	homeOnly := NewWithScope("human:test", ScopeDefaults{Home: f.homeRoot, HomeByDefault: true})
	handler = catalogPresentationTestHandler(homeOnly)
	request := httptest.NewRequest(http.MethodGet, path+"?cluster=cluster_scope", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("query cluster scope = %d %s, want 400", response.Code, response.Body.String())
	}
}

func catalogAssetHTTPCall(handler http.Handler, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func catalogAssetHTTPPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.SetRGBA(x, y, color.RGBA{R: byte(30 + x), G: byte(60 + y), B: byte(90 + x + y), A: 0xff})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, imageData); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

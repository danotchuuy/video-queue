package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("body = %s", got)
	}
}

func TestUnknownAPIRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := newMux(dir)

	for path, want := range map[string]string{
		"/":          "<html>app</html>",
		"/queue/123": "<html>app</html>",
		"/app.js":    "console.log(1)",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: body = %q, want %q", path, rec.Body.String(), want)
		}
	}
}

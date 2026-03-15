package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/soulteary/gorge-search/internal/engine"

	"github.com/labstack/echo/v4"
)

func newTestDeps(token string) *Deps {
	se := engine.New(nil)
	return &Deps{Engine: se, Token: token}
}

func TestHealthz(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("")
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("expected ok in body, got %s", rec.Body.String())
	}
}

func TestTokenAuth_NoToken(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("secret123")
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/api/search/backends", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestTokenAuth_ValidToken(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("secret123")
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/api/search/backends", nil)
	req.Header.Set("X-Service-Token", "secret123")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestTokenAuth_QueryParam(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("secret123")
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/api/search/backends?token=secret123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestTokenAuth_Disabled(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("")
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/api/search/backends", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestIndexDocument_MissingPHID(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("")
	RegisterRoutes(e, deps)

	body := `{"type":"TASK","title":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/search/index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSearchQuery_NoBackends(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("")
	RegisterRoutes(e, deps)

	body := `{"query":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/search/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (no backends), got %d", rec.Code)
	}
}

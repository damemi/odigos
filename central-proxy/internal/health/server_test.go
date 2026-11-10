package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzAlwaysOK(t *testing.T) {
	s := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestReadyzBeforeAndAfterReady(t *testing.T) {
	s := NewServer()
	handler := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}

	s.MarkReady()

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("readyz after ready status = %d, want %d", rr.Code, http.StatusOK)
	}
}

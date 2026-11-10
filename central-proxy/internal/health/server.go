package health

import (
	"net/http"
	"sync/atomic"
	"time"
)

// Server exposes kubelet probe endpoints for central-proxy.
// /healthz is always 200 once the process is listening so startup/liveness
// probes do not restart the pod while effective-config is still missing.
// /readyz returns 200 only after MarkReady has been called.
type Server struct {
	ready atomic.Bool
}

func NewServer() *Server {
	return &Server{}
}

func (s *Server) MarkReady() {
	s.ready.Store(true)
}

func (s *Server) Ready() bool {
	return s.ready.Load()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// ListenAndServe binds the probe HTTP server. Intended to run in a goroutine;
// the chart probes port 8080.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

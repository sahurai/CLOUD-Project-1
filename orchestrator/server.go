package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

type ServerOptions struct {
	Store *ConfigStore
	LBURL string
	Kube  KubeClient
}

type Server struct {
	opts ServerOptions
	mux  *http.ServeMux
}

func NewServer(opts ServerOptions) *Server {
	s := &Server{opts: opts, mux: http.NewServeMux()}

	// Reconcile cluster state whenever config changes — fire-and-forget so the
	// API responds immediately, kubectl runs in the background.
	if opts.Kube != nil {
		opts.Store.OnChange(func(_, next Config) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if errs := opts.Kube.Apply(ctx, next); len(errs) > 0 {
				for _, err := range errs {
					log.Printf("reconcile: %v", err)
				}
			} else {
				log.Printf("reconcile: applied %s", next.Summary())
			}
		})
	}

	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.handle("GET /healthz", s.handleHealthz)
	s.handle("GET /api/config", s.handleGetConfig)
	s.handle("PUT /api/config", s.handlePutConfig)
	s.handle("POST /api/config/apply", s.handleApplyConfig)
	s.handle("POST /api/config/reset", s.handleResetConfig)
	s.handle("GET /api/status", s.handleStatus)
	s.handle("POST /api/demo/predict", s.handleDemoPredict)
	s.handle("POST /api/demo/loadtest", s.handleDemoLoadtest)
}

// handle wraps an HTTP handler with shared middleware: panic recovery, request
// log, JSON header default. Method routing is handled by Go 1.22+ ServeMux
// pattern syntax (e.g. "GET /api/config").
func (s *Server) handle(pattern string, fn http.HandlerFunc) {
	s.mux.HandleFunc(pattern, withRecovery(withLogging(fn)))
}

func withRecovery(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next(w, r)
	}
}

func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next(rw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rw.status, time.Since(start).Truncate(time.Millisecond))
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

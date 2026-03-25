package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
)

// Default routing threshold: 2.5 MB (in bytes).
const defaultThreshold = 2_500_000

func main() {
	cpuAddr := envOrDefault("CPU_BACKEND", "http://ai-worker-cpu:8001")
	gpuAddr := envOrDefault("GPU_BACKEND", "http://ai-worker-gpu:8001")
	listenPort := envOrDefault("PORT", "8080")

	threshold := defaultThreshold
	if v := os.Getenv("SIZE_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			threshold = n
		}
	}

	cpuURL, err := url.Parse(cpuAddr)
	if err != nil {
		log.Fatalf("invalid CPU_BACKEND url: %v", err)
	}
	gpuURL, err := url.Parse(gpuAddr)
	if err != nil {
		log.Fatalf("invalid GPU_BACKEND url: %v", err)
	}

	cpuProxy := httputil.NewSingleHostReverseProxy(cpuURL)
	gpuProxy := httputil.NewSingleHostReverseProxy(gpuURL)

	mux := http.NewServeMux()

	// /predict/ — route based on Content-Length
	mux.HandleFunc("/predict/", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength >= int64(threshold) {
			log.Printf("[route] %s %s (%d bytes) -> GPU", r.Method, r.URL.Path, r.ContentLength)
			gpuProxy.ServeHTTP(w, r)
		} else {
			log.Printf("[route] %s %s (%d bytes) -> CPU", r.Method, r.URL.Path, r.ContentLength)
			cpuProxy.ServeHTTP(w, r)
		}
	})

	// /models/ — always goes to CPU worker
	mux.HandleFunc("/models/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[route] %s %s -> CPU", r.Method, r.URL.Path)
		cpuProxy.ServeHTTP(w, r)
	})

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	addr := ":" + listenPort
	log.Printf("load-balancer listening on %s  (threshold=%d bytes, cpu=%s, gpu=%s)",
		addr, threshold, cpuAddr, gpuAddr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

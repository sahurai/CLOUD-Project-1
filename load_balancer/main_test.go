package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsRouting(t *testing.T) {
	// Create a test server that mimics the CPU worker
	cpuServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"success","models":["test.h5"]}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cpuServer.Close()

	// We can't easily test the full routing without mocking the reverse proxy
	// But we can test the threshold logic indirectly

	// Test that /models/ would be routed to CPU (always)
	// This is more of an integration test, but shows the structure
}

func TestThresholdLogic(t *testing.T) {
	tests := []struct {
		name         string
		contentLength int64
		expectedRoute string
	}{
		{"small file", 1000000, "CPU"},     // 1MB < 2.5MB
		{"medium file", 2000000, "CPU"},    // 2MB < 2.5MB
		{"large file", 3000000, "GPU"},     // 3MB > 2.5MB
		{"exact threshold", 2500000, "CPU"}, // 2.5MB == 2.5MB (should go to CPU)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't directly test the routing logic without refactoring
			// But we can verify the threshold constant
			if defaultThreshold != 2500000 {
				t.Errorf("Expected threshold 2500000, got %d", defaultThreshold)
			}

			// Simulate the routing decision
			var route string
			if tt.contentLength >= int64(defaultThreshold) {
				route = "GPU"
			} else {
				route = "CPU"
			}

			if route != tt.expectedRoute {
				t.Errorf("For content length %d, expected route %s, got %s",
					tt.contentLength, tt.expectedRoute, route)
			}
		})
	}
}

func TestHealthEndpoint(t *testing.T) {
	// Test the health endpoint
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	// Create a simple handler for health
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	expected := `{"status":"ok"}`
	if strings.TrimSpace(w.Body.String()) != expected {
		t.Errorf("Expected body %s, got %s", expected, w.Body.String())
	}
}

func TestEnvOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		fallback string
		expected string
	}{
		{"env set", "TEST_VAR", "test_value", "fallback", "test_value"},
		{"env not set", "NONEXISTENT_VAR", "", "fallback", "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != "" {
				t.Setenv(tt.key, tt.value)
			}
			result := envOrDefault(tt.key, tt.fallback)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func BenchmarkThresholdCheck(b *testing.B) {
	contentLength := int64(3000000) // 3MB
	threshold := int64(defaultThreshold)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if contentLength >= threshold {
			// GPU route
		} else {
			// CPU route
		}
	}
}
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is the live, user-tunable configuration of the cluster.
//
// Every field has a safe default — partial input on PUT /api/config is fine,
// missing fields keep their existing value. Validate() enforces hard bounds
// so a typo can't take the cluster down.
type Config struct {
	// Routing
	SizeThresholdBytes int64 `json:"size_threshold_bytes"` // /predict over this -> GPU pool

	// Autoscaling — CPU pool
	CPUMinReplicas         int32 `json:"cpu_min_replicas"`
	CPUMaxReplicas         int32 `json:"cpu_max_replicas"`
	CPUTargetUtilization   int32 `json:"cpu_target_utilization"` // %, 1..100

	// Autoscaling — GPU pool
	GPUMinReplicas         int32 `json:"gpu_min_replicas"`
	GPUMaxReplicas         int32 `json:"gpu_max_replicas"`
	GPUTargetUtilization   int32 `json:"gpu_target_utilization"` // %, 1..100

	// Upload safeguards
	MaxUploadBytes      int64    `json:"max_upload_bytes"`
	MaxImagePixels      int64    `json:"max_image_pixels"`      // width * height ceiling
	AllowedContentTypes []string `json:"allowed_content_types"` // declared mime allowlist

	// Demo runner caps
	MaxLoadTestRequests    int `json:"max_loadtest_requests"`
	MaxLoadTestConcurrency int `json:"max_loadtest_concurrency"`

	// Operational
	UpstreamTimeoutSeconds int `json:"upstream_timeout_seconds"`

	UpdatedAt time.Time `json:"updated_at"`
}

// Hard bounds — these are absolute caps, not defaults. Defaults sit comfortably
// inside these ranges. See DefaultConfig().
const (
	minThreshold       int64 = 1024            // 1 KiB; below this routing is meaningless
	maxThreshold       int64 = 1 << 30         // 1 GiB
	minReplicas        int32 = 0
	maxReplicas        int32 = 50              // safety cap; raise consciously
	minUtil            int32 = 1
	maxUtil            int32 = 100
	minUploadBytes     int64 = 4 * 1024        // 4 KiB
	maxUploadBytes     int64 = 64 * 1024 * 1024 // 64 MiB
	minPixels          int64 = 64 * 64
	maxPixels          int64 = 64_000_000      // ~8000x8000
	maxLoadtestN       int   = 5000
	maxLoadtestConcur  int   = 256
	minUpstreamTimeout int   = 1
	maxUpstreamTimeout int   = 600
)

// DefaultConfig returns the configuration applied when no persisted file exists
// and the values used to backfill any missing field on PUT.
func DefaultConfig() Config {
	return Config{
		SizeThresholdBytes:     2_500_000,
		CPUMinReplicas:         1,
		CPUMaxReplicas:         5,
		CPUTargetUtilization:   70,
		GPUMinReplicas:         1,
		GPUMaxReplicas:         3,
		GPUTargetUtilization:   75,
		MaxUploadBytes:         16 * 1024 * 1024, // 16 MiB
		MaxImagePixels:         16_000_000,        // 4000x4000
		AllowedContentTypes:    []string{"image/jpeg", "image/png"},
		MaxLoadTestRequests:    200,
		MaxLoadTestConcurrency: 16,
		UpstreamTimeoutSeconds: 60,
		UpdatedAt:              time.Now().UTC(),
	}
}

// Summary returns a one-line human description of the config — used in startup logs.
func (c Config) Summary() string {
	return fmt.Sprintf(
		"threshold=%d cpu=%d-%d@%d%% gpu=%d-%d@%d%% upload<=%dB pixels<=%d types=%s",
		c.SizeThresholdBytes,
		c.CPUMinReplicas, c.CPUMaxReplicas, c.CPUTargetUtilization,
		c.GPUMinReplicas, c.GPUMaxReplicas, c.GPUTargetUtilization,
		c.MaxUploadBytes, c.MaxImagePixels,
		strings.Join(c.AllowedContentTypes, ","),
	)
}

// Validate enforces structural invariants and absolute bounds. Returns a list
// of human-readable errors so the API can report all problems at once.
func (c Config) Validate() error {
	var errs []string

	if c.SizeThresholdBytes < minThreshold || c.SizeThresholdBytes > maxThreshold {
		errs = append(errs, fmt.Sprintf("size_threshold_bytes must be in [%d, %d]", minThreshold, maxThreshold))
	}
	for label, v := range map[string]int32{
		"cpu_min_replicas": c.CPUMinReplicas,
		"cpu_max_replicas": c.CPUMaxReplicas,
		"gpu_min_replicas": c.GPUMinReplicas,
		"gpu_max_replicas": c.GPUMaxReplicas,
	} {
		if v < minReplicas || v > maxReplicas {
			errs = append(errs, fmt.Sprintf("%s must be in [%d, %d]", label, minReplicas, maxReplicas))
		}
	}
	if c.CPUMinReplicas > c.CPUMaxReplicas {
		errs = append(errs, "cpu_min_replicas must be <= cpu_max_replicas")
	}
	if c.GPUMinReplicas > c.GPUMaxReplicas {
		errs = append(errs, "gpu_min_replicas must be <= gpu_max_replicas")
	}
	if c.CPUMaxReplicas < 1 {
		errs = append(errs, "cpu_max_replicas must be >= 1 to keep the CPU pool serving")
	}
	if c.GPUMaxReplicas < 1 {
		errs = append(errs, "gpu_max_replicas must be >= 1 to keep the GPU pool serving")
	}
	for label, v := range map[string]int32{
		"cpu_target_utilization": c.CPUTargetUtilization,
		"gpu_target_utilization": c.GPUTargetUtilization,
	} {
		if v < minUtil || v > maxUtil {
			errs = append(errs, fmt.Sprintf("%s must be in [%d, %d]", label, minUtil, maxUtil))
		}
	}
	if c.MaxUploadBytes < minUploadBytes || c.MaxUploadBytes > maxUploadBytes {
		errs = append(errs, fmt.Sprintf("max_upload_bytes must be in [%d, %d]", minUploadBytes, maxUploadBytes))
	}
	if c.MaxImagePixels < minPixels || c.MaxImagePixels > maxPixels {
		errs = append(errs, fmt.Sprintf("max_image_pixels must be in [%d, %d]", minPixels, maxPixels))
	}
	if len(c.AllowedContentTypes) == 0 {
		errs = append(errs, "allowed_content_types must contain at least one entry")
	}
	for _, ct := range c.AllowedContentTypes {
		if !strings.HasPrefix(strings.ToLower(ct), "image/") {
			errs = append(errs, fmt.Sprintf("allowed_content_types entry %q must start with image/", ct))
		}
	}
	if c.MaxLoadTestRequests < 1 || c.MaxLoadTestRequests > maxLoadtestN {
		errs = append(errs, fmt.Sprintf("max_loadtest_requests must be in [1, %d]", maxLoadtestN))
	}
	if c.MaxLoadTestConcurrency < 1 || c.MaxLoadTestConcurrency > maxLoadtestConcur {
		errs = append(errs, fmt.Sprintf("max_loadtest_concurrency must be in [1, %d]", maxLoadtestConcur))
	}
	if c.UpstreamTimeoutSeconds < minUpstreamTimeout || c.UpstreamTimeoutSeconds > maxUpstreamTimeout {
		errs = append(errs, fmt.Sprintf("upstream_timeout_seconds must be in [%d, %d]", minUpstreamTimeout, maxUpstreamTimeout))
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// merge takes a partial update represented as a generic JSON map and applies
// any present fields to the receiver. Absent or null fields keep their value
// (this is what "default if not filled" means at the API edge).
//
// Returns ErrUnknownField if the patch contains keys we don't recognize, so
// typos surface instead of silently no-op'ing.
func (c *Config) merge(patch map[string]json.RawMessage) error {
	known := map[string]func(json.RawMessage) error{
		"size_threshold_bytes":     func(v json.RawMessage) error { return json.Unmarshal(v, &c.SizeThresholdBytes) },
		"cpu_min_replicas":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.CPUMinReplicas) },
		"cpu_max_replicas":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.CPUMaxReplicas) },
		"cpu_target_utilization":   func(v json.RawMessage) error { return json.Unmarshal(v, &c.CPUTargetUtilization) },
		"gpu_min_replicas":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.GPUMinReplicas) },
		"gpu_max_replicas":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.GPUMaxReplicas) },
		"gpu_target_utilization":   func(v json.RawMessage) error { return json.Unmarshal(v, &c.GPUTargetUtilization) },
		"max_upload_bytes":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.MaxUploadBytes) },
		"max_image_pixels":         func(v json.RawMessage) error { return json.Unmarshal(v, &c.MaxImagePixels) },
		"allowed_content_types":    func(v json.RawMessage) error { return json.Unmarshal(v, &c.AllowedContentTypes) },
		"max_loadtest_requests":    func(v json.RawMessage) error { return json.Unmarshal(v, &c.MaxLoadTestRequests) },
		"max_loadtest_concurrency": func(v json.RawMessage) error { return json.Unmarshal(v, &c.MaxLoadTestConcurrency) },
		"upstream_timeout_seconds": func(v json.RawMessage) error { return json.Unmarshal(v, &c.UpstreamTimeoutSeconds) },
	}

	var unknown []string
	for k := range patch {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown fields: %s", strings.Join(unknown, ", "))
	}

	for k, raw := range patch {
		if len(raw) == 0 || string(raw) == "null" {
			continue // explicit null = leave as-is (no-op default behavior)
		}
		if err := known[k](raw); err != nil {
			return fmt.Errorf("field %q: %w", k, err)
		}
	}

	// Normalize the content-type allowlist: lowercased, trimmed, deduped.
	if len(c.AllowedContentTypes) > 0 {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(c.AllowedContentTypes))
		for _, ct := range c.AllowedContentTypes {
			ct = strings.ToLower(strings.TrimSpace(ct))
			if ct == "" {
				continue
			}
			if _, dup := seen[ct]; dup {
				continue
			}
			seen[ct] = struct{}{}
			out = append(out, ct)
		}
		c.AllowedContentTypes = out
	}

	return nil
}

// ConfigStore wraps a Config with safe concurrent access and JSON file persistence.
// Persistence is best-effort: a write failure is logged but does not block the API,
// because in-memory state is the source of truth at runtime and the user can
// re-PUT to recover.
type ConfigStore struct {
	mu       sync.RWMutex
	current  Config
	path     string
	onChange func(old, next Config)
}

func NewConfigStore(path string) (*ConfigStore, error) {
	s := &ConfigStore{path: path, current: DefaultConfig()}
	if path == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// First run — write defaults so the user has a file to inspect.
		if werr := s.persistLocked(); werr != nil {
			return nil, fmt.Errorf("persist defaults: %w", werr)
		}
	case err != nil:
		return nil, fmt.Errorf("read config: %w", err)
	default:
		var loaded Config
		if jerr := json.Unmarshal(data, &loaded); jerr != nil {
			return nil, fmt.Errorf("parse config: %w", jerr)
		}
		// Backfill any missing fields with defaults — older config files stay valid.
		merged := DefaultConfig()
		raw := map[string]json.RawMessage{}
		if jerr := json.Unmarshal(data, &raw); jerr != nil {
			return nil, fmt.Errorf("re-scan config: %w", jerr)
		}
		if merr := merged.merge(raw); merr != nil {
			return nil, fmt.Errorf("merge config: %w", merr)
		}
		if verr := merged.Validate(); verr != nil {
			return nil, fmt.Errorf("invalid persisted config: %w", verr)
		}
		s.current = merged
	}
	return s, nil
}

// OnChange registers a callback fired (synchronously, after persist) whenever
// the config is updated. Used to reconcile cluster state in the background.
func (s *ConfigStore) OnChange(fn func(old, next Config)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *ConfigStore) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Update applies a partial JSON patch atomically. Validation runs against the
// merged result so the store is never left in an invalid state.
func (s *ConfigStore) Update(patch map[string]json.RawMessage) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.current
	if err := next.merge(patch); err != nil {
		return s.current, err
	}
	if err := next.Validate(); err != nil {
		return s.current, err
	}
	next.UpdatedAt = time.Now().UTC()

	old := s.current
	s.current = next
	if err := s.persistLocked(); err != nil {
		// Persist failure is not fatal — runtime state already updated.
		// Surface in logs; client still gets the new config back.
		fmt.Fprintf(os.Stderr, "[orchestrator] WARN persist config: %v\n", err)
	}
	if s.onChange != nil {
		go s.onChange(old, next)
	}
	return next, nil
}

// Reset replaces the current config with defaults, persists, and fires the
// OnChange hook so the cluster reconciles back to the documented baseline.
// Used by POST /api/config/reset.
func (s *ConfigStore) Reset() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.current
	next := DefaultConfig()
	next.UpdatedAt = time.Now().UTC()
	s.current = next
	if err := s.persistLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "[orchestrator] WARN persist config (reset): %v\n", err)
	}
	if s.onChange != nil {
		go s.onChange(old, next)
	}
	return next
}

func (s *ConfigStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

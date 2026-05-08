package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// LoadTestParams configures a synthetic batch run against the load balancer.
type LoadTestParams struct {
	N           int
	Concurrency int
	LBURL       string
	ModelName   string
	ImageBytes  []byte
	ContentType string
	Timeout     time.Duration
}

// LoadTestReport is the structured response from a /api/demo/loadtest call.
// It captures the things you actually want to see in a demo: throughput,
// latency distribution, per-pool routing breakdown, and error counts.
type LoadTestReport struct {
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
	DurationSeconds  float64           `json:"duration_seconds"`
	N                int               `json:"requests"`
	Concurrency      int               `json:"concurrency"`
	Successes        int               `json:"successes"`
	Failures         int               `json:"failures"`
	Throughput       float64           `json:"throughput_rps"`
	LatencyMS        LatencyStats      `json:"latency_ms"`
	NodeDistribution map[string]int    `json:"node_distribution"` // node_type -> count
	WorkerSpread     map[string]int    `json:"worker_spread"`     // worker_id (pod) -> count
	StatusCodes      map[string]int    `json:"status_codes"`
	Errors           []string          `json:"errors,omitempty"`
}

type LatencyStats struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// callResult is the per-request outcome aggregated by RunLoadTest. Lifted to
// package scope so doOneCall and RunLoadTest share a single, named type.
type callResult struct {
	latencyMS  float64
	statusCode int
	nodeType   string
	workerID   string
	err        error
}

// RunLoadTest fires N requests at /predict with the given concurrency and
// returns aggregated stats. It is bounded by ctx — cancellation stops new
// requests being launched but lets in-flight ones finish naturally.
func RunLoadTest(ctx context.Context, p LoadTestParams) LoadTestReport {
	report := LoadTestReport{
		StartedAt:        time.Now().UTC(),
		N:                p.N,
		Concurrency:      p.Concurrency,
		NodeDistribution: map[string]int{},
		WorkerSpread:     map[string]int{},
		StatusCodes:      map[string]int{},
	}

	// One shared HTTP client with explicit per-request timeout (contexts).
	// MaxIdleConnsPerHost defaults to 2 which throttles us; bump it.
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        p.Concurrency * 2,
			MaxIdleConnsPerHost: p.Concurrency * 2,
			MaxConnsPerHost:     p.Concurrency * 2,
		},
	}

	target, err := url.JoinPath(p.LBURL, "/predict/")
	if err != nil {
		report.Errors = append(report.Errors, "build url: "+err.Error())
		report.FinishedAt = time.Now().UTC()
		return report
	}

	jobs := make(chan int, p.Concurrency)
	results := make(chan callResult, p.N)

	var wg sync.WaitGroup
	for i := 0; i < p.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				select {
				case <-ctx.Done():
					results <- callResult{err: ctx.Err()}
					continue
				default:
				}
				results <- doOneCall(ctx, client, target, p)
			}
		}()
	}

dispatch:
	for i := 0; i < p.N; i++ {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	close(results)

	latencies := make([]float64, 0, p.N)
	for r := range results {
		if r.err != nil {
			report.Failures++
			report.Errors = appendCapped(report.Errors, r.err.Error(), 20)
			continue
		}
		key := fmt.Sprintf("%d", r.statusCode)
		report.StatusCodes[key]++
		if r.statusCode >= 200 && r.statusCode < 300 {
			report.Successes++
			latencies = append(latencies, r.latencyMS)
			if r.nodeType != "" {
				report.NodeDistribution[r.nodeType]++
			}
			if r.workerID != "" {
				report.WorkerSpread[r.workerID]++
			}
		} else {
			report.Failures++
		}
	}

	report.LatencyMS = computeLatencyStats(latencies)
	report.FinishedAt = time.Now().UTC()
	report.DurationSeconds = report.FinishedAt.Sub(report.StartedAt).Seconds()
	if report.DurationSeconds > 0 {
		report.Throughput = float64(report.Successes) / report.DurationSeconds
	}

	return report
}

func doOneCall(ctx context.Context, client *http.Client, target string, p LoadTestParams) (out callResult) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "loadtest.jpg")
	if err != nil {
		out.err = err
		return
	}
	if _, err := part.Write(p.ImageBytes); err != nil {
		out.err = err
		return
	}
	_ = mw.WriteField("model_name", p.ModelName)
	if err := mw.Close(); err != nil {
		out.err = err
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, body)
	if err != nil {
		out.err = err
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		out.err = err
		return
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	out.latencyMS = float64(time.Since(start).Microseconds()) / 1000.0
	out.statusCode = resp.StatusCode

	// The worker returns node_type and worker_id — extract for distribution stats.
	var parsed struct {
		NodeType string `json:"node_type"`
		WorkerID string `json:"worker_id"`
	}
	if jerr := json.Unmarshal(rawBody, &parsed); jerr == nil {
		out.nodeType = parsed.NodeType
		out.workerID = parsed.WorkerID
	}
	return
}

func computeLatencyStats(values []float64) LatencyStats {
	if len(values) == 0 {
		return LatencyStats{}
	}
	sort.Float64s(values)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return LatencyStats{
		Min: values[0],
		Avg: sum / float64(len(values)),
		P50: percentile(values, 0.50),
		P95: percentile(values, 0.95),
		P99: percentile(values, 0.99),
		Max: values[len(values)-1],
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func appendCapped(slice []string, s string, max int) []string {
	if len(slice) >= max {
		return slice
	}
	return append(slice, s)
}

// loadtestImage decodes the user-supplied base64 image, or generates a tiny
// synthetic JPEG so the demo is runnable with no input. The synthetic image
// is small and well-formed so the worker's CNN preprocessing still succeeds.
func loadtestImage(b64 string, cfg Config) ([]byte, string, error) {
	if b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, "", fmt.Errorf("image_base64 decode: %w", err)
		}
		if int64(len(raw)) > cfg.MaxUploadBytes {
			return nil, "", fmt.Errorf("image_base64 exceeds max_upload_bytes=%d", cfg.MaxUploadBytes)
		}
		ct := http.DetectContentType(raw)
		if !contentTypeAllowed(cfg.AllowedContentTypes, ct) {
			return nil, "", fmt.Errorf("image_base64 content-type %q not allowed", ct)
		}
		return raw, ct, nil
	}

	img := image.NewRGBA(image.Rect(0, 0, 224, 224))
	for y := 0; y < 224; y++ {
		for x := 0; x < 224; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, "", fmt.Errorf("synthesize image: %w", err)
	}
	return buf.Bytes(), "image/jpeg", nil
}

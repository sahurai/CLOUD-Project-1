package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- Health ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "orchestrator",
	})
}

// --- Config ---

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Store.Snapshot())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	// Cap config patch at 64 KiB — these are tiny JSON objects, anything bigger
	// is suspicious.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "request too large or malformed: "+err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "request body required")
		return
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	next, err := s.opts.Store.Update(patch)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, next)
}

// handleApplyConfig forces a re-apply of the current config to the cluster.
// Useful when kubectl was unavailable at the time of the previous PUT, or for
// a fresh deploy where reconcile-on-change hasn't fired yet.
func (s *Server) handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.opts.Store.Snapshot()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	errs := s.opts.Kube.Apply(ctx, cfg)
	resp := map[string]any{
		"applied_at": time.Now().UTC(),
		"config":     cfg,
	}
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		resp["errors"] = msgs
		writeJSON(w, http.StatusMultiStatus, resp)
		return
	}
	resp["status"] = "ok"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleResetConfig(w http.ResponseWriter, _ *http.Request) {
	// Reset is implemented as a Reset() on the store, not as a synthetic PUT,
	// so server-managed fields like updated_at don't bounce through the
	// unknown-fields check in merge().
	next := s.opts.Store.Reset()
	writeJSON(w, http.StatusOK, next)
}

// --- Status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	st, err := s.opts.Kube.Status(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "status: "+err.Error())
		return
	}
	resp := map[string]any{
		"config":  s.opts.Store.Snapshot(),
		"cluster": st,
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Demo: single prediction ---

// handleDemoPredict accepts a multipart form (file + model_name), runs the
// full validation chain, and forwards to the load balancer. The response is
// the LB's JSON response augmented with orchestrator metadata.
func (s *Server) handleDemoPredict(w http.ResponseWriter, r *http.Request) {
	cfg := s.opts.Store.Snapshot()

	// Bound the entire request body. multipart.Reader reads incrementally so
	// MaxBytesReader is the hard ceiling regardless of how the client streams.
	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadBytes+1024*1024) // +1MiB form overhead

	if err := r.ParseMultipartForm(cfg.MaxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
		return
	}

	modelName := strings.TrimSpace(r.FormValue("model_name"))
	if modelName == "" {
		writeError(w, http.StatusBadRequest, "model_name is required")
		return
	}
	if !looksLikeSafeModelName(modelName) {
		writeError(w, http.StatusBadRequest, "model_name contains illegal characters")
		return
	}

	fh, fileHeader, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required: "+err.Error())
		return
	}
	defer fh.Close()

	declared := ""
	if fileHeader.Header != nil {
		declared = fileHeader.Header.Get("Content-Type")
	}

	img, err := ValidateImageUpload(fh, declared, fileHeader.Filename, cfg)
	if err != nil {
		switch {
		case errors.Is(err, ErrUploadTooLarge), errors.Is(err, ErrImageTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, ErrUploadEmpty), errors.Is(err, ErrContentTypeBlocked), errors.Is(err, ErrNotAnImage):
			writeError(w, http.StatusUnsupportedMediaType, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.UpstreamTimeoutSeconds)*time.Second)
	defer cancel()

	upstreamResp, status, err := forwardPredict(ctx, s.opts.LBURL, modelName, img)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}

	// Wrap the upstream response with orchestrator metadata so the client sees
	// what the validator measured (helps debugging routing decisions).
	wrapper := map[string]any{
		"orchestrator": map[string]any{
			"validated": map[string]any{
				"content_type": img.ContentType,
				"filename":     img.Filename,
				"width":        img.Width,
				"height":       img.Height,
				"bytes":        len(img.Bytes),
			},
			"routed_to_pool": routingPool(int64(len(img.Bytes)), cfg.SizeThresholdBytes),
		},
		"upstream": json.RawMessage(upstreamResp),
	}
	writeJSON(w, status, wrapper)
}

// --- Demo: synthetic load test ---

type loadTestRequest struct {
	N           int    `json:"n"`            // total requests
	Concurrency int    `json:"concurrency"`  // parallel workers
	ModelName   string `json:"model_name"`
	// One of these two — image to use for the test:
	ImageBase64 string `json:"image_base64"` // small image, base64-encoded
	// If neither is provided, the server uses a tiny synthetic JPEG.
}

func (s *Server) handleDemoLoadtest(w http.ResponseWriter, r *http.Request) {
	cfg := s.opts.Store.Snapshot()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "request too large: "+err.Error())
		return
	}
	var req loadTestRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}

	if req.N <= 0 {
		req.N = 20
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 4
	}
	if req.N > cfg.MaxLoadTestRequests {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("n=%d exceeds max_loadtest_requests=%d", req.N, cfg.MaxLoadTestRequests))
		return
	}
	if req.Concurrency > cfg.MaxLoadTestConcurrency {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("concurrency=%d exceeds max_loadtest_concurrency=%d", req.Concurrency, cfg.MaxLoadTestConcurrency))
		return
	}
	if req.Concurrency > req.N {
		req.Concurrency = req.N
	}
	if req.ModelName == "" {
		writeError(w, http.StatusBadRequest, "model_name is required")
		return
	}
	if !looksLikeSafeModelName(req.ModelName) {
		writeError(w, http.StatusBadRequest, "model_name contains illegal characters")
		return
	}

	imgBytes, ct, err := loadtestImage(req.ImageBase64, cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	report := RunLoadTest(r.Context(), LoadTestParams{
		N:           req.N,
		Concurrency: req.Concurrency,
		LBURL:       s.opts.LBURL,
		ModelName:   req.ModelName,
		ImageBytes:  imgBytes,
		ContentType: ct,
		Timeout:     time.Duration(cfg.UpstreamTimeoutSeconds) * time.Second,
	})

	writeJSON(w, http.StatusOK, report)
}

// --- Internals ---

// forwardPredict builds a multipart request to the LB /predict endpoint and
// returns its raw JSON body along with status code.
func forwardPredict(ctx context.Context, lbURL, modelName string, img *ValidatedImage) ([]byte, int, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	part, err := mw.CreateFormFile("file", safeFilename(img.Filename))
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(img.Bytes); err != nil {
		return nil, 0, err
	}
	if err := mw.WriteField("model_name", modelName); err != nil {
		return nil, 0, err
	}
	if err := mw.Close(); err != nil {
		return nil, 0, err
	}

	target, err := url.JoinPath(lbURL, "/predict/")
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

func routingPool(size, threshold int64) string {
	if size >= threshold {
		return "gpu"
	}
	return "cpu"
}

// looksLikeSafeModelName guards against path-traversal style inputs hitting
// the worker's filesystem. Worker code joins MODEL_DIR + model_name, so we
// reject "../" and shell-special characters here at the orchestrator edge.
func looksLikeSafeModelName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	if strings.ContainsAny(name, `/\` + "\x00") {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func safeFilename(name string) string {
	if name == "" {
		return "upload.bin"
	}
	// Strip any directory component the client might have sent.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "upload.bin"
	}
	return name
}

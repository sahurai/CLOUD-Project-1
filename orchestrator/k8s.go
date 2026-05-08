package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// KubeClient is the abstraction over cluster mutation/read ops the orchestrator
// needs. It exists so we can swap in noopKube{} in --no-kube mode without
// peppering nil-checks through the handlers.
type KubeClient interface {
	Probe(ctx context.Context) error

	// Apply reconciles the running cluster with cfg. Best-effort: a single field
	// failure does not abort the rest, but every error is returned in the slice
	// so the caller can surface them.
	Apply(ctx context.Context, cfg Config) []error

	// Status returns aggregated cluster info for /api/status.
	Status(ctx context.Context) (ClusterStatus, error)
}

// ClusterStatus is the JSON-shaped snapshot we return from /api/status.
type ClusterStatus struct {
	KubectlAvailable bool             `json:"kubectl_available"`
	Namespace        string           `json:"namespace"`
	Pods             []PodInfo        `json:"pods"`
	HPAs             []HPAInfo        `json:"hpas"`
	Errors           []string         `json:"errors,omitempty"`
}

type PodInfo struct {
	Name   string `json:"name"`
	App    string `json:"app"`
	Phase  string `json:"phase"`
	Ready  bool   `json:"ready"`
	NodeIP string `json:"node_ip,omitempty"`
}

type HPAInfo struct {
	Name            string `json:"name"`
	MinReplicas     int32  `json:"min_replicas"`
	MaxReplicas     int32  `json:"max_replicas"`
	CurrentReplicas int32  `json:"current_replicas"`
	TargetCPU       int32  `json:"target_cpu_utilization"`
	CurrentCPU      *int32 `json:"current_cpu_utilization,omitempty"`
}

// kubectlClient runs `kubectl` as a subprocess. Simple, no SDK dependency, and
// inherits whatever auth context the pod's ServiceAccount provides.
type kubectlClient struct {
	bin       string
	namespace string
}

func NewKubectl(bin, namespace string) KubeClient {
	return &kubectlClient{bin: bin, namespace: namespace}
}

func (k *kubectlClient) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-n", k.namespace}, args...)
	cmd := exec.CommandContext(ctx, k.bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v: %w (stderr=%s)", k.bin, full, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

func (k *kubectlClient) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := k.run(ctx, "version", "--client=true", "--output=json")
	if err != nil {
		// Fall back to plain version (older kubectl). We only need to confirm
		// the binary exists and runs.
		_, err2 := exec.CommandContext(ctx, k.bin, "version", "--client=true").Output()
		if err2 != nil {
			return fmt.Errorf("kubectl probe: %w", errors.Join(err, err2))
		}
	}
	return nil
}

// Apply patches HPA min/max + utilization for both pools, and updates
// SIZE_THRESHOLD on the load-balancer deployment. Each step is independent
// so a missing resource (e.g. no GPU HPA in some envs) doesn't abort the others.
func (k *kubectlClient) Apply(ctx context.Context, cfg Config) []error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var errs []error
	apply := func(label string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
	}

	apply("patch hpa cpu min/max", func() error {
		return k.patchHPAReplicas(ctx, "ai-worker-cpu", cfg.CPUMinReplicas, cfg.CPUMaxReplicas)
	})
	apply("patch hpa cpu cpu-target", func() error {
		return k.patchHPAUtilization(ctx, "ai-worker-cpu", cfg.CPUTargetUtilization)
	})
	apply("patch hpa gpu min/max", func() error {
		return k.patchHPAReplicas(ctx, "ai-worker-gpu", cfg.GPUMinReplicas, cfg.GPUMaxReplicas)
	})
	apply("patch hpa gpu cpu-target", func() error {
		return k.patchHPAUtilization(ctx, "ai-worker-gpu", cfg.GPUTargetUtilization)
	})
	apply("set lb threshold env", func() error {
		return k.setEnv(ctx, "deployment/load-balancer", "SIZE_THRESHOLD", fmt.Sprintf("%d", cfg.SizeThresholdBytes))
	})

	return errs
}

func (k *kubectlClient) patchHPAReplicas(ctx context.Context, name string, minR, maxR int32) error {
	patch := fmt.Sprintf(`{"spec":{"minReplicas":%d,"maxReplicas":%d}}`, minR, maxR)
	_, err := k.run(ctx, "patch", "hpa", name, "--type=merge", "-p", patch)
	return err
}

func (k *kubectlClient) patchHPAUtilization(ctx context.Context, name string, target int32) error {
	// Patch the first CPU resource metric. autoscaling/v2 supports multiple
	// metrics; we only manage the CPU one here.
	patch := fmt.Sprintf(
		`{"spec":{"metrics":[{"type":"Resource","resource":{"name":"cpu","target":{"type":"Utilization","averageUtilization":%d}}}]}}`,
		target,
	)
	_, err := k.run(ctx, "patch", "hpa", name, "--type=merge", "-p", patch)
	return err
}

func (k *kubectlClient) setEnv(ctx context.Context, target, key, value string) error {
	_, err := k.run(ctx, "set", "env", target, fmt.Sprintf("%s=%s", key, value))
	return err
}

// --- Status ---

func (k *kubectlClient) Status(ctx context.Context) (ClusterStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out := ClusterStatus{KubectlAvailable: true, Namespace: k.namespace}

	if pods, err := k.fetchPods(ctx); err != nil {
		out.Errors = append(out.Errors, "pods: "+err.Error())
	} else {
		out.Pods = pods
	}
	if hpas, err := k.fetchHPAs(ctx); err != nil {
		out.Errors = append(out.Errors, "hpas: "+err.Error())
	} else {
		out.HPAs = hpas
	}
	return out, nil
}

func (k *kubectlClient) fetchPods(ctx context.Context) ([]PodInfo, error) {
	raw, err := k.run(ctx, "get", "pods", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				HostIP            string `json:"hostIP"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse pods json: %w", err)
	}
	out := make([]PodInfo, 0, len(list.Items))
	for _, item := range list.Items {
		ready := len(item.Status.ContainerStatuses) > 0
		for _, cs := range item.Status.ContainerStatuses {
			if !cs.Ready {
				ready = false
				break
			}
		}
		out = append(out, PodInfo{
			Name:   item.Metadata.Name,
			App:    item.Metadata.Labels["app"],
			Phase:  item.Status.Phase,
			Ready:  ready,
			NodeIP: item.Status.HostIP,
		})
	}
	return out, nil
}

func (k *kubectlClient) fetchHPAs(ctx context.Context) ([]HPAInfo, error) {
	raw, err := k.run(ctx, "get", "hpa", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				MinReplicas *int32 `json:"minReplicas"`
				MaxReplicas int32  `json:"maxReplicas"`
				Metrics     []struct {
					Type     string `json:"type"`
					Resource struct {
						Name   string `json:"name"`
						Target struct {
							Type               string `json:"type"`
							AverageUtilization *int32 `json:"averageUtilization"`
						} `json:"target"`
					} `json:"resource"`
				} `json:"metrics"`
			} `json:"spec"`
			Status struct {
				CurrentReplicas int32 `json:"currentReplicas"`
				CurrentMetrics  []struct {
					Type     string `json:"type"`
					Resource struct {
						Name    string `json:"name"`
						Current struct {
							AverageUtilization *int32 `json:"averageUtilization"`
						} `json:"current"`
					} `json:"resource"`
				} `json:"currentMetrics"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse hpa json: %w", err)
	}
	out := make([]HPAInfo, 0, len(list.Items))
	for _, item := range list.Items {
		info := HPAInfo{
			Name:            item.Metadata.Name,
			MaxReplicas:     item.Spec.MaxReplicas,
			CurrentReplicas: item.Status.CurrentReplicas,
		}
		if item.Spec.MinReplicas != nil {
			info.MinReplicas = *item.Spec.MinReplicas
		}
		for _, m := range item.Spec.Metrics {
			if m.Type == "Resource" && m.Resource.Name == "cpu" && m.Resource.Target.AverageUtilization != nil {
				info.TargetCPU = *m.Resource.Target.AverageUtilization
				break
			}
		}
		for _, m := range item.Status.CurrentMetrics {
			if m.Type == "Resource" && m.Resource.Name == "cpu" {
				info.CurrentCPU = m.Resource.Current.AverageUtilization
				break
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// noopKube is the implementation used when --no-kube is set. Status reports
// "kubectl unavailable" so the API stays consistent.
type noopKube struct{}

func (noopKube) Probe(context.Context) error { return nil }
func (noopKube) Apply(context.Context, Config) []error {
	return []error{errors.New("kubectl integration disabled (--no-kube)")}
}
func (noopKube) Status(context.Context) (ClusterStatus, error) {
	return ClusterStatus{KubectlAvailable: false}, nil
}

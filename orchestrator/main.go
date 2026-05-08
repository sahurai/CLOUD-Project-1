package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var (
		listenAddr   string
		configPath   string
		lbURL        string
		namespace    string
		kubectlBin   string
		disableKube  bool
		readTimeout  time.Duration
		writeTimeout time.Duration
	)

	flag.StringVar(&listenAddr, "listen", envOr("ORCH_LISTEN", ":9000"), "Address to listen on (env: ORCH_LISTEN)")
	flag.StringVar(&configPath, "config", envOr("ORCH_CONFIG", "/var/lib/orchestrator/config.json"), "Path to persisted config JSON (env: ORCH_CONFIG)")
	flag.StringVar(&lbURL, "lb-url", envOr("ORCH_LB_URL", "http://load-balancer:8080"), "Upstream load balancer URL (env: ORCH_LB_URL)")
	flag.StringVar(&namespace, "namespace", envOr("ORCH_NAMESPACE", "glaucoma"), "Kubernetes namespace (env: ORCH_NAMESPACE)")
	flag.StringVar(&kubectlBin, "kubectl", envOr("ORCH_KUBECTL", "kubectl"), "kubectl binary (env: ORCH_KUBECTL)")
	flag.BoolVar(&disableKube, "no-kube", envOr("ORCH_NO_KUBE", "") != "", "Disable kubectl integration; config-only mode (env: ORCH_NO_KUBE)")
	flag.DurationVar(&readTimeout, "read-timeout", 30*time.Second, "HTTP server read timeout")
	flag.DurationVar(&writeTimeout, "write-timeout", 120*time.Second, "HTTP server write timeout (long for load tests)")
	flag.Parse()

	store, err := NewConfigStore(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded from %s: %s", configPath, store.Snapshot().Summary())

	var kube KubeClient
	if disableKube {
		kube = noopKube{}
		log.Printf("kubectl integration disabled (config-only mode)")
	} else {
		kube = NewKubectl(kubectlBin, namespace)
		if err := kube.Probe(context.Background()); err != nil {
			log.Printf("WARN: kubectl probe failed (%v); cluster ops will return errors", err)
		}
	}

	srv := NewServer(ServerOptions{
		Store: store,
		LBURL: lbURL,
		Kube:  kube,
	})

	httpServer := &http.Server{
		Addr:           listenAddr,
		Handler:        srv,
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
		MaxHeaderBytes: 1 << 16,
	}

	idleClosed := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown error: %v", err)
		}
		close(idleClosed)
	}()

	log.Printf("orchestrator listening on %s (lb=%s namespace=%s)", listenAddr, lbURL, namespace)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	<-idleClosed
	log.Printf("bye")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

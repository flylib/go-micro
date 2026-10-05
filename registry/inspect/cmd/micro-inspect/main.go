// Command micro-inspect reports registry instances that do not answer
// (zombies) or report themselves unhealthy, using registry/inspect.
//
//	MICRO_REGISTRY                 etcd | consul | nacos
//	MICRO_REGISTRY_ADDRESS         comma-separated host:port
//	MICRO_REGISTRY_NAMESPACE       nacos namespace
//	MICRO_REGISTRY_GROUP           nacos group
//	MICRO_REGISTRY_USERNAME        nacos username (servers with auth on)
//	MICRO_REGISTRY_PASSWORD        nacos password
//	MICRO_INSPECT_INTERVAL         time between rounds (default 30s)
//	MICRO_INSPECT_TIMEOUT          per probe (default 3s)
//	MICRO_INSPECT_THRESHOLD        failed rounds before reporting (default 3)
//	MICRO_INSPECT_SERVICES         comma-separated services to inspect (default all)
//	MICRO_INSPECT_WEBHOOK          URL to post state changes to
//	MICRO_INSPECT_WEBHOOK_KIND     json (default) | feishu | dingtalk | wecom
//	MICRO_INSPECT_WEBHOOK_SECRET   signing secret of a feishu or dingtalk bot
//	MICRO_INSPECT_ADDRESS          HTTP address for /status and /health (default :8070)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/registry/consul"
	"github.com/flylib/go-micro/registry/etcd"
	"github.com/flylib/go-micro/registry/inspect"
	"github.com/flylib/go-micro/registry/nacos"
)

func main() {
	if err := run(); err != nil {
		logger.Logf(logger.ErrorLevel, "micro-inspect: %v", err)
		os.Exit(1)
	}
}

func run() error {
	reg, err := newRegistry()
	if err != nil {
		return err
	}
	opts := inspect.Options{Services: split(os.Getenv("MICRO_INSPECT_SERVICES"))}
	if opts.Interval, err = duration("MICRO_INSPECT_INTERVAL"); err != nil {
		return err
	}
	if opts.Timeout, err = duration("MICRO_INSPECT_TIMEOUT"); err != nil {
		return err
	}
	if v := os.Getenv("MICRO_INSPECT_THRESHOLD"); v != "" {
		if opts.Threshold, err = strconv.Atoi(v); err != nil || opts.Threshold < 1 {
			return fmt.Errorf("MICRO_INSPECT_THRESHOLD must be a positive integer, got %q", v)
		}
	}
	if u := os.Getenv("MICRO_INSPECT_WEBHOOK"); u != "" {
		w := &inspect.Webhook{URL: u, Kind: os.Getenv("MICRO_INSPECT_WEBHOOK_KIND"), Secret: os.Getenv("MICRO_INSPECT_WEBHOOK_SECRET")}
		switch w.Kind {
		case "", inspect.KindJSON, inspect.KindFeishu, inspect.KindDingTalk, inspect.KindWeCom:
		default:
			return fmt.Errorf("MICRO_INSPECT_WEBHOOK_KIND must be json, feishu, dingtalk or wecom, got %q", w.Kind)
		}
		opts.Notifier = w
	}
	in := inspect.New(reg, opts)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := os.Getenv("MICRO_INSPECT_ADDRESS")
	if addr == "" {
		addr = ":8070"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(in.Nodes())
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Logf(logger.ErrorLevel, "micro-inspect: http: %v", err)
			stop()
		}
	}()
	logger.Logf(logger.InfoLevel, "micro-inspect: inspecting %s, status on %s", reg, addr)
	in.Run(ctx)
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func duration(env string) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration, got %q", env, v)
	}
	return d, nil
}

func newRegistry() (registry.Registry, error) {
	opts := []registry.Option{registry.Addrs(split(os.Getenv("MICRO_REGISTRY_ADDRESS"))...)}
	switch r := os.Getenv("MICRO_REGISTRY"); r {
	case "etcd":
		return etcd.NewEtcdRegistry(opts...), nil
	case "consul":
		return consul.NewConsulRegistry(opts...), nil
	case "nacos":
		if ns := os.Getenv("MICRO_REGISTRY_NAMESPACE"); ns != "" {
			opts = append(opts, nacos.WithNamespaceId(ns))
		}
		if g := os.Getenv("MICRO_REGISTRY_GROUP"); g != "" {
			opts = append(opts, nacos.WithGroupName(g))
		}
		if u := os.Getenv("MICRO_REGISTRY_USERNAME"); u != "" {
			opts = append(opts, nacos.WithAuth(u, os.Getenv("MICRO_REGISTRY_PASSWORD")))
		}
		return nacos.NewRegistry(opts...), nil
	default:
		return nil, fmt.Errorf("MICRO_REGISTRY must be etcd, consul or nacos, got %q", r)
	}
}

func split(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

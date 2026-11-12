// tikscope collects MikroTik DNS logs, router metrics and reachability
// probes into SQLite and serves a dashboard for them.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aganet/tikscope/internal/api"
	"github.com/aganet/tikscope/internal/ingest"
	"github.com/aganet/tikscope/internal/probe"
	"github.com/aganet/tikscope/internal/routeros"
	"github.com/aganet/tikscope/internal/store"
	"github.com/aganet/tikscope/web"
)

var version = "dev"

type config struct {
	httpAddr     string
	syslogAddr   string
	databasePath string
	retention    store.Retention
	dnsServer    string
	logLevel     slog.Level

	mikrotikURL      string
	mikrotikUser     string
	mikrotikPassword string
	mikrotikInsecure bool
	pollInterval     time.Duration
	wanInterface     string

	probeTargets  []probe.Target
	probeInterval time.Duration
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envDuration(name string, def, min time.Duration) (time.Duration, error) {
	s := os.Getenv(name)
	if s == "" {
		return def, nil
	}
	d, err := api.ParseDuration(s)
	if err != nil || d < min {
		return 0, fmt.Errorf("%s must be a duration of at least %s, got %q", name, min, s)
	}
	return d, nil
}

func loadConfig() (config, error) {
	cfg := config{
		httpAddr:     env("HTTP_ADDR", ":3000"),
		syslogAddr:   env("SYSLOG_ADDR", ":5354"),
		databasePath: env("DATABASE_PATH", "./data/tikscope.db"),
		dnsServer:    os.Getenv("DNS_SERVER"),
		mikrotikURL:  strings.TrimRight(os.Getenv("MIKROTIK_URL"), "/"),
		mikrotikUser: os.Getenv("MIKROTIK_USER"),
		wanInterface: os.Getenv("WAN_INTERFACE"),
	}
	cfg.mikrotikPassword = os.Getenv("MIKROTIK_PASSWORD")
	var err error
	if cfg.retention.Queries, err = envDuration("RETENTION", 7*24*time.Hour, time.Hour); err != nil {
		return cfg, err
	}
	if cfg.retention.Metrics, err = envDuration("METRICS_RETENTION", 30*24*time.Hour, time.Hour); err != nil {
		return cfg, err
	}
	if cfg.retention.Events, err = envDuration("EVENTS_RETENTION", 30*24*time.Hour, time.Hour); err != nil {
		return cfg, err
	}
	if cfg.pollInterval, err = envDuration("POLL_INTERVAL", 15*time.Second, 5*time.Second); err != nil {
		return cfg, err
	}
	if cfg.probeInterval, err = envDuration("PROBE_INTERVAL", 10*time.Second, 2*time.Second); err != nil {
		return cfg, err
	}
	if s := os.Getenv("MIKROTIK_INSECURE"); s != "" {
		if cfg.mikrotikInsecure, err = strconv.ParseBool(s); err != nil {
			return cfg, fmt.Errorf("MIKROTIK_INSECURE must be a boolean, got %q", s)
		}
	}
	if cfg.mikrotikURL != "" && cfg.mikrotikUser == "" {
		return cfg, errors.New("MIKROTIK_USER is required when MIKROTIK_URL is set")
	}
	if s := os.Getenv("LOG_LEVEL"); s != "" {
		if err := cfg.logLevel.UnmarshalText([]byte(s)); err != nil {
			return cfg, fmt.Errorf("LOG_LEVEL: %w", err)
		}
	}

	// Probe targets: ICMP to public resolvers by default, plus a real DNS
	// lookup through the router when its address is known.
	routerHost := ""
	if u, err := url.Parse(cfg.mikrotikURL); err == nil && u.Host != "" {
		routerHost = u.Hostname()
	}
	for _, t := range splitList(env("PROBE_ICMP", "1.1.1.1,8.8.8.8")) {
		cfg.probeTargets = append(cfg.probeTargets, probe.Target{Kind: "icmp", Addr: t})
	}
	dnsDefault := ""
	if cfg.dnsServer != "" {
		dnsDefault = cfg.dnsServer
	} else if routerHost != "" {
		dnsDefault = routerHost
	}
	name := env("PROBE_DNS_NAME", "example.com")
	for _, t := range splitList(env("PROBE_DNS", dnsDefault)) {
		cfg.probeTargets = append(cfg.probeTargets, probe.Target{Kind: "dns", Addr: t, Name: name})
	}
	return cfg, nil
}

// splitList parses a comma separated list; "none" disables it.
func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" && p != "none" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(log)
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	webFS, err := fsSub(web.FS)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.databasePath, log)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	log.Info("database ready", "path", cfg.databasePath, "version", version)

	// The store outlives the producers so their final flushes land.
	storeCtx, stopStore := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.Run(storeCtx, cfg.retention)
	}()

	in := ingest.New(st, log)
	errc := make(chan error, 2)
	go func() { errc <- in.Listen(ctx, cfg.syslogAddr) }()

	var poller *routeros.Poller
	if cfg.mikrotikURL != "" {
		client := routeros.NewClient(cfg.mikrotikURL, cfg.mikrotikUser, cfg.mikrotikPassword, cfg.mikrotikInsecure)
		poller = routeros.NewPoller(client, st, cfg.pollInterval, cfg.wanInterface, log)
		go poller.Run(ctx)
		log.Info("polling router", "url", cfg.mikrotikURL, "interval", cfg.pollInterval)
	}

	prober := probe.New(cfg.probeTargets, cfg.probeInterval, st, log)
	go prober.Run(ctx)
	if len(cfg.probeTargets) > 0 {
		log.Info("probing", "targets", len(cfg.probeTargets), "interval", cfg.probeInterval)
	}

	handler := api.New(api.Deps{
		Store: st, Ingest: in, Poller: poller, Prober: prober, Web: webFS,
		Resolver: newResolver(cfg.dnsServer), Version: version, Retention: cfg.retention, Log: log,
	}).Handler()
	srv := &http.Server{Addr: cfg.httpAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		log.Info("http listening", "addr", cfg.httpAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errc:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	in.Close()
	stopStore()
	wg.Wait()
	return runErr
}

func newResolver(server string) *net.Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, server)
		},
	}
}

// fsSub exposes the embedded assets rooted at the web directory.
func fsSub(f embed.FS) (fs.FS, error) { return fs.Sub(f, ".") }

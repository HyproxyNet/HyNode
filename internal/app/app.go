package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"hynode/internal/config"
	"hynode/internal/kernel"
	mierukernel "hynode/internal/kernel/mieru"
	singkernel "hynode/internal/kernel/singbox"
	appmetrics "hynode/internal/metrics"
	"hynode/internal/panel"
	"hynode/internal/state"
)

type App struct {
	config config.Config
	log    *slog.Logger
	client *panel.Client
	stats  *state.Store
	speed  *appmetrics.SpeedSnapshot
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	return &App{
		config: cfg,
		log:    logger,
		client: panel.NewClient(cfg.Panel.URL, cfg.Panel.Token),
		stats:  state.New(),
		speed:  appmetrics.NewSpeedSnapshot(),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if err := a.serveHealth(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(a.config.Nodes))
	for _, configured := range a.config.Nodes {
		if !configured.IsEnabled() {
			continue
		}
		node := configured
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.runNode(ctx, node.ID); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("node %s: %w", node.ID, err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		<-done
		return nil
	case err := <-errs:
		return err
	case <-done:
		return errors.New("all node workers exited")
	}
}

func (a *App) runNode(ctx context.Context, nodeID string) error {
	var (
		configETag string
		usersETag  string
		currentCfg *panel.NodeConfig
		currentUsr []panel.User
		usersReady bool
		runtime    kernel.Runtime
		nodeType   string
	)
	defer func() {
		if runtime != nil {
			_ = runtime.Close()
		}
	}()

	apply := func() error {
		if currentCfg == nil || !usersReady {
			return nil
		}
		nodeType = currentCfg.NodeType()
		a.stats.ReplaceUsers(currentUsr)
		next, err := a.createRuntime(nodeID, *currentCfg, currentUsr)
		if err != nil {
			return err
		}
		if runtime != nil {
			_ = runtime.Close()
			runtime = nil
		}
		if err = next.Start(ctx); err != nil {
			_ = next.Close()
			return err
		}
		runtime = next
		a.log.Info("node runtime applied", "node", nodeID, "protocol", currentCfg.Protocol, "users", len(currentUsr))
		return nil
	}
	refresh := func() error {
		changed := false
		cfg, tag, update, err := a.client.Config(ctx, nodeID, nodeType, configETag)
		if err != nil {
			return fmt.Errorf("fetch config: %w", err)
		}
		configETag = tag
		if update {
			currentCfg, changed = cfg, true
			a.log.Info("config updated", "node", nodeID, "protocol", cfg.Protocol, "port", cfg.ServerPort)
		}
		users, tag, update, err := a.client.Users(ctx, nodeID, nodeType, usersETag)
		if err != nil {
			return fmt.Errorf("fetch users: %w", err)
		}
		usersETag = tag
		if update {
			currentUsr, usersReady, changed = users.Users, true, true
			a.log.Info("users updated", "node", nodeID, "count", len(users.Users))
		}
		if changed {
			return apply()
		}
		return nil
	}
	a.log.Info("connecting to panel", "node", nodeID, "url", a.config.Panel.URL)
	if err := retry(ctx, a.log, nodeID, refresh); err != nil {
		return err
	}
	pull := interval(a.config.Sync.PullInterval, currentCfg.BaseConfig.PullInterval)
	report := interval(a.config.Sync.ReportInterval, currentCfg.BaseConfig.PushInterval)
	pullTicker, reportTicker := time.NewTicker(pull), time.NewTicker(report)
	defer pullTicker.Stop()
	defer reportTicker.Stop()

	// Final report on shutdown
	defer func() {
		if currentCfg == nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.doReport(shutdownCtx, nodeID, nodeType)
		a.log.Info("final report sent", "node", nodeID)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pullTicker.C:
			if err := refresh(); err != nil {
				a.log.Error("pull node update", "node", nodeID, "error", err)
			}
		case <-reportTicker.C:
			a.doReport(ctx, nodeID, nodeType)
		}
	}
}

func (a *App) doReport(ctx context.Context, nodeID, nodeType string) {
	snapshot := a.stats.Snapshot(nodeID)

	status, metr := appmetrics.Collect(ctx, a.speed, a.stats.Metrics(), "running")

	payload := panel.Report{
		Traffic: snapshot.Traffic,
		Alive:   snapshot.Alive,
		Online:  snapshot.Online,
		Status:  toMap(status),
		Metrics: toMap(metr),
	}
	if err := a.client.Report(ctx, nodeID, nodeType, payload); err != nil {
		a.log.Error("report node status", "node", nodeID, "error", err)
		return
	}
	a.stats.Commit(nodeID, snapshot)
}

func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (a *App) createRuntime(nodeID string, cfg panel.NodeConfig, users []panel.User) (kernel.Runtime, error) {
	if cfg.Protocol == "mieru" {
		return mierukernel.Factory{}.New(nodeID, cfg, users, a.stats)
	}
	return singkernel.Factory{
		DataDir:  a.config.Runtime.DataDir,
		Fallback: a.config.CertificateFallback,
	}.New(nodeID, cfg, users, a.stats)
}

func (a *App) serveHealth(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	server := &http.Server{Addr: a.config.Runtime.HealthListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.log.Error("health server stopped", "error", err)
		}
	}()
	return nil
}

func retry(ctx context.Context, log *slog.Logger, nodeID string, action func() error) error {
	delay := time.Second
	for {
		err := action()
		if err == nil {
			return nil
		}
		log.Warn("panel request failed, retrying", "node", nodeID, "error", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func interval(local time.Duration, seconds int) time.Duration {
	if local > 0 {
		return local
	}
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// Command server is the KubePortal backend. It only wires dependencies;
// business logic lives in internal packages.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/R0cker64/kubeportal/backend/internal/auth"
	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/config"
	"github.com/R0cker64/kubeportal/backend/internal/httpapi"
	"github.com/R0cker64/kubeportal/backend/internal/k8s"
	"github.com/R0cker64/kubeportal/backend/internal/logs"
	"github.com/R0cker64/kubeportal/backend/internal/metrics"
	"github.com/R0cker64/kubeportal/backend/internal/store"
)

const (
	informerSyncTimeout = 2 * time.Minute
	sessionGCInterval   = 10 * time.Minute
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var static fs.FS
	if cfg.StaticDir != "" {
		root, err := os.OpenRoot(cfg.StaticDir)
		if err != nil {
			return err
		}
		defer root.Close()
		static = root.FS()
	}

	dbCtx, cancel := context.WithTimeout(ctx, cfg.UpstreamTimeout)
	db, err := store.Open(dbCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer db.Close()
	migrateCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = db.Migrate(migrateCtx)
	cancel()
	if err != nil {
		return err
	}
	go collectSessions(ctx, log, db, cfg)

	restCfg, err := k8s.RESTConfig(cfg.Kubeconfig, cfg.KubeContext, cfg.UpstreamTimeout)
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	workloads, err := k8s.NewCache(client, cfg.Environments, log)
	if err != nil {
		return err
	}
	if err := workloads.Start(ctx, informerSyncTimeout); err != nil {
		return err
	}

	authSvc, err := auth.NewService(auth.Config{
		IssuerURL:    cfg.OIDCIssuerURL.String(),
		DiscoveryURL: cfg.OIDCDiscoveryURL.String(),
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		PublicURL:    cfg.PublicURL,
		SessionKey:   cfg.SessionKey,
		SessionTTL:   cfg.SessionTTL,
		IdleTimeout:  cfg.SessionIdleTimeout,
		HTTPClient:   &http.Client{Timeout: cfg.UpstreamTimeout},
	}, db, log)
	if err != nil {
		return err
	}

	handler := httpapi.NewHandler(cfg, log, static, httpapi.Deps{
		Auth:      authSvc,
		Resolver:  authz.NewResolver(db, cfg.AdminGroup),
		Workloads: workloads,
		Logs:      logs.NewClient(cfg.LokiURL, cfg.UpstreamTimeout),
		Metrics:   metrics.NewClient(cfg.PrometheusURL, cfg.UpstreamTimeout, cfg.CadvisorNodeLabel),
		Teams:     db,
		Ready: func(ctx context.Context) error {
			if !workloads.Synced() {
				return errors.New("informers not synced")
			}
			return db.Ping(ctx)
		},
	})
	srv := httpapi.NewServer(cfg, log, handler)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// collectSessions deletes expired and idle sessions periodically.
func collectSessions(ctx context.Context, log *slog.Logger, db *store.Store, cfg *config.Config) {
	t := time.NewTicker(sessionGCInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			gcCtx, cancel := context.WithTimeout(ctx, cfg.UpstreamTimeout)
			n, err := db.DeleteExpiredSessions(gcCtx, time.Now(), cfg.SessionIdleTimeout)
			cancel()
			if err != nil {
				log.Warn("session cleanup", "error", err)
			} else if n > 0 {
				log.Info("session cleanup", "deleted", n)
			}
		}
	}
}

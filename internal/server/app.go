package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accountmanager"
	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/accounts/jsonstore"
	"chatgpt-codex-proxy/internal/anthropic"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/codexauth"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/devicelogin"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

type App struct {
	cfg             config.Config
	logger          *slog.Logger
	engine          *gin.Engine
	accounts        *accounts.Service
	deviceLogins    *devicelogin.DeviceLoginService
	accountMgr      *accountmanager.AccountManager
	httpClient      *codex.HTTPClient
	httpStream      func(context.Context, accounts.Record, codex.Request, string) (eventStream, error)
	compactCaller   func(context.Context, accounts.Record, codex.CompactRequest) (codex.CompactResponse, *accounts.QuotaSnapshot, error)
	imageOpener     func(*gin.Context, string, turn.NormalizedRequest) (openedRequest, bool)
	directImageOpen func(context.Context, accounts.Record, string, []byte, bool) (*http.Response, error)
	wsConnector     responsesWebSocketConnector
	continuations   *conversation.ContinuationManager
	claudeReplays   *anthropic.ReplayManager
	models          *models.Catalog
	cancel          context.CancelFunc
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	accountsStore := jsonstore.NewJSONAccountsStore(cfg.DataDir)
	accountsSvc, err := accounts.NewService(accountsStore, accounts.RotationLeastUsed)
	if err != nil {
		return nil, err
	}

	modelCatalog := models.NewCatalog(models.BootstrapEntries())
	if snapshot, err := models.LoadCache(cfg.DataDir); err == nil {
		modelCatalog.LoadCache(snapshot)
	} else if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		logger.Warn("load models cache failed", "error", err.Error())
	}

	httpClient := codex.NewHTTPClient(cfg)
	oauthSvc := codexauth.NewOAuthService(cfg)
	accountMgr := accountmanager.NewAccountManager(cfg, accountsSvc, oauthSvc, httpClient, modelCatalog)
	deviceLogins := devicelogin.NewDeviceLoginService(oauthSvc, accountsSvc, cfg.LoginTimeout)
	modelRefresher := models.NewFetcher(cfg, logger, accountsSvc, accountMgr, httpClient, modelCatalog)

	engine := gin.New()
	engine.SetTrustedProxies(nil)
	engine.Use(middleware.RequestID())
	engine.Use(middleware.RequestLogger(logger))
	engine.Use(middleware.Recovery(logger))

	app := &App{
		cfg:           cfg,
		logger:        logger,
		engine:        engine,
		accounts:      accountsSvc,
		deviceLogins:  deviceLogins,
		accountMgr:    accountMgr,
		httpClient:    httpClient,
		continuations: conversation.NewContinuationManager(cfg.ContinuationTTL),
		claudeReplays: anthropic.NewReplayManager(cfg.ContinuationTTL),
		models:        modelCatalog,
	}
	app.routes()

	ctx, cancel := context.WithCancel(context.Background())
	app.cancel = cancel
	go app.housekeeping(ctx)
	go modelRefresher.Run(ctx)

	return app, nil
}

func (a *App) Handler() http.Handler {
	return a.engine
}

func (a *App) Close() {
	a.cancel()
	a.httpClient.Close()
}

func (a *App) modelCatalog() *models.Catalog {
	if a != nil && a.models != nil {
		return a.models
	}
	return models.NewCatalog(models.BootstrapEntries())
}

func (a *App) housekeeping(ctx context.Context) {
	sweeps := time.Tick(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sweeps:
			a.continuations.Sweep()
			a.claudeReplays.Sweep()
			a.deviceLogins.DeleteExpired(time.Now().UTC())
		}
	}
}

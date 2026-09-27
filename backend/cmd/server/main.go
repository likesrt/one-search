package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/one-search/one-search/backend/internal/api"
	"github.com/one-search/one-search/backend/internal/config"
	"github.com/one-search/one-search/backend/internal/db"
	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/keypool"
	"github.com/one-search/one-search/backend/internal/logging"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
	"github.com/one-search/one-search/backend/internal/search"
	"github.com/one-search/one-search/backend/internal/security"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	log := logging.New()
	cfg, err := config.Load()
	if err != nil {
		log.Error("config_invalid", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database_connect_failed", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}
	defer pool.Close()

	if cfg.RunMigrations {
		if err := db.RunMigrations(ctx, pool, cfg.MigrationsDir); err != nil {
			log.Error("migration_failed", map[string]interface{}{"error": err.Error()})
			os.Exit(1)
		}
	}

	crypto := security.NewCrypto(cfg.EncryptionKey)
	store := db.NewStore(pool, crypto)
	adminExists, err := store.AdminExists(ctx, cfg.AdminUsername)
	if err != nil {
		log.Error("admin_lookup_failed", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}
	if !adminExists {
		if cfg.AdminPassword == "" {
			log.Error("admin_password_required", map[string]interface{}{"error": "ADMIN_PASSWORD is required when creating the initial admin user"})
			os.Exit(1)
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			log.Error("admin_password_hash_failed", map[string]interface{}{"error": err.Error()})
			os.Exit(1)
		}
		created, err := store.EnsureAdmin(ctx, cfg.AdminUsername, string(passwordHash))
		if err != nil {
			log.Error("ensure_admin_failed", map[string]interface{}{"error": err.Error()})
			os.Exit(1)
		}
		if created {
			log.Info("admin_created", map[string]interface{}{"username": cfg.AdminUsername})
		}
	}

	registry, err := buildProviderRegistry(cfg)
	if err != nil {
		log.Error("provider_registry_failed", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}
	keyPool := keypool.NewManager(store)
	orchestrator := search.NewOrchestrator(registry, keyPool, store)
	auth := api.NewAuthService(store, cfg.AdminSessionTTL, cfg.AdminLoginMaxAttempts, cfg.AdminLoginWindow, cfg.AdminLoginLockout)
	handler := api.NewHandler(store, auth, orchestrator)
	handler.SetLogger(log)
	if cfg.MCPEnabled {
		handler.EnableMCP(cfg.MCPPath)
	}
	enableFetch(handler, store, log)

	server := api.NewServer(cfg, log)
	server.SetHealth(func() bool {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return pool.Ping(pingCtx) == nil
	})
	server.Mount(handler.Mount)
	stopCleaner := startLogRetentionCleaner(store, log)
	defer stopCleaner()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.Router(),
		ReadHeaderTimeout: cfg.ServerReadHeaderTimeout,
		ReadTimeout:       cfg.ServerReadTimeout,
		WriteTimeout:      cfg.ServerWriteTimeout,
		IdleTimeout:       cfg.ServerIdleTimeout,
	}

	go func() {
		log.Info("server_starting", map[string]interface{}{"addr": cfg.HTTPAddr, "mcp_enabled": cfg.MCPEnabled, "mcp_path": cfg.MCPPath})
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server_failed", map[string]interface{}{"error": err.Error()})
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("server_shutdown_failed", map[string]interface{}{"error": err.Error()})
	}
}

// buildProviderRegistry 注册全部内置渠道的适配器工厂。
// 工厂而非实例：key 级 base_url / 代理差异要求每次请求按当前 key 重建适配器。
// cfg.UpstreamUserAgent 与 cfg.RequestTimeout 作为兜底注入到每个渠道配置里。
// 未在此注册的渠道名会让 providerRegistered 为 false，搜索直接返回 "provider is not registered"，
// 因此渠道清单（model.DefaultProviders）、迁移脚本与本函数必须同步增删。
func buildProviderRegistry(cfg config.Config) (*provider.Registry, error) {
	registry := provider.NewRegistry()
	registry.RegisterFactory(model.ProviderExa, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewExaProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderYou, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewYouProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderJina, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewJinaProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderTavily, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewTavilyProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderFirecrawl, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewFirecrawlProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderSerper, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewSerperProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderBrave, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewBraveProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderKeenable, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewKeenableProvider(providerCfg)
	})
	registry.RegisterFactory(model.ProviderContext7, func(providerCfg provider.Config) provider.Provider {
		providerCfg.UserAgent = cfg.UpstreamUserAgent
		if providerCfg.Timeout == 0 {
			providerCfg.Timeout = cfg.RequestTimeout
		}
		return provider.NewContext7Provider(providerCfg)
	})
	return registry, nil
}

// enableFetch 把数据库里的抓取开关推给 Handler，作为 MCP 侧的启动初值。
//
// 设计取舍：Handler 自身不查库决定 MCP 的工具清单（既有测试用零值 Handler 构造，
// store 为 nil 会 panic），因此启动期由 main 读一次配置并注入标志，运行期则由
// Handler.fetchSettings 在每次抓取/配置读写时刷新。
//
// 读取失败时保持启用：宁可多列出一次工具，也不要因为一次瞬时查询失败就让抓取
// 在 MCP 侧消失（REST 端点仍会按读到的实时配置拒绝请求）。
func enableFetch(handler *api.Handler, store *db.Store, log *logging.Logger) {
	settings, err := store.FetchSettings(context.Background())
	if err != nil {
		log.Error("fetch_settings_read_failed", map[string]interface{}{"error": err.Error()})
	}
	handler.EnableFetch(err != nil || settings.Enabled)
}

// startLogRetentionCleaner 启动日志保留清理任务，返回停止函数。
//
// 每小时执行一次 run()：清理过期日志与搜索缓存，并清扫网页抓取的本地文件缓存。
//
// 抓取缓存必须有一条与访问无关的清扫路径：一个抓取过之后再没人访问的 URL，
// 其缓存文件永远不会被读到，也就永远不会被删除，长期运行下磁盘只涨不跌。
// 这里刻意复用同一 ticker 与同一 goroutine，不新增生命周期管理。
//
// 参数 store 用于读取保留策略；log 用于记录失败。
// 返回值：幂等的停止函数，由 main 在退出时调用。
// 副作用：启动一个后台 goroutine；按保留策略删除日志、搜索缓存与抓取缓存文件。
func startLogRetentionCleaner(store *db.Store, log *logging.Logger) func() {
	stop := make(chan struct{})
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		settings, err := store.RuntimeSettings(ctx)
		if err != nil {
			log.Error("log_retention_settings_failed", map[string]interface{}{"error": err.Error()})
			return
		}
		searchDeleted, auditDeleted, err := store.DeleteOldLogs(ctx, settings.LogRetentionDays)
		if err != nil {
			log.Error("log_retention_cleanup_failed", map[string]interface{}{"error": err.Error(), "retention_days": settings.LogRetentionDays})
			return
		}
		if err := store.DeleteExpiredCache(ctx); err != nil {
			log.Error("cache_cleanup_failed", map[string]interface{}{"error": err.Error()})
		}
		cleanFetchCache(ctx, store, log)
		if searchDeleted > 0 || auditDeleted > 0 {
			log.Info("log_retention_cleanup", map[string]interface{}{"retention_days": settings.LogRetentionDays, "search_deleted": searchDeleted, "audit_deleted": auditDeleted})
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				run()
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

// cleanFetchCache 按当前抓取配置清扫本地文件缓存。
//
// 读配置失败时本轮直接跳过：TTL 与总量上限都来自配置，用零值代替会让所有条目
// 被判定为已过期而整目录清空。宁可持续一小时不清理，也不能因为一次读库失败清掉全部缓存。
// 这里刻意不在 main 里重复一份默认值，避免与 db 层的默认值各自漂移。
//
// 参数 ctx 用于读配置；log 用于记录失败。
// 副作用：删除抓取缓存目录中已过期或超出总量上限的文件；失败只记日志。
func cleanFetchCache(ctx context.Context, store *db.Store, log *logging.Logger) {
	settings, err := store.FetchSettings(ctx)
	if err != nil {
		log.Error("fetch_cache_cleanup_skipped", map[string]interface{}{"error": err.Error()})
		return
	}
	dir := fetch.DefaultCacheDir()
	if err := fetch.CleanCache(dir, settings.CacheTTLSeconds, settings.CacheErrorTTLSeconds, settings.CacheMaxTotalBytes); err != nil {
		log.Error("fetch_cache_cleanup_failed", map[string]interface{}{"dir": dir, "error": err.Error()})
	}
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	d_notification "github.com/nuomiiiii/lite/database/notification"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/corn"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/nuomiiiii/lite/pkg/migrations"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/utils/cloudflared"
	"github.com/nuomiiiii/lite/utils/geoip"
	"github.com/nuomiiiii/lite/utils/httpsserver"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"github.com/nuomiiiii/lite/utils/notifier"
	"github.com/nuomiiiii/lite/web/api"
	installweb "github.com/nuomiiiii/lite/web/install"
	"github.com/nuomiiiii/lite/web/oauth"
	frontendpublic "github.com/nuomiiiii/lite/web/public"
	"github.com/nuomiiiii/lite/web/router"
	"github.com/nuomiiiii/lite/web/security"
	storageupdateweb "github.com/nuomiiiii/lite/web/storageupdate"
	upgradeweb "github.com/nuomiiiii/lite/web/update"
	"github.com/nuomiiiii/lite/web/upload"
	"gorm.io/gorm"
)

// cleanupFunc 是一个关闭阶段执行的清理函数。
type cleanupFunc struct {
	name string
	fn   func(ctx context.Context) error
}

// App 显式建模服务端的启动生命周期。
//
// 过去 RunServer 把目录创建、数据库、metric store、GeoIP、定时任务、通知、OAuth、
// Gin 中间件、路由、HTTP 启动与 shutdown 全部混在一个函数里，
// 启动顺序只能靠通读整段代码推断，异步初始化过早且吞掉错误，关闭也不完整。
//
// App 把这些拆成有序阶段：
//
//	Bootstrap    基础设施：目录、数据库、配置快照
//	InitStores   存储：metric store
//	InitProviders 外部 provider：OAuth（同步，路由依赖）、GeoIP、消息发送
//	StartBackground 后台：定时任务
//	BuildRouter  构建 Gin 引擎与路由
//	Run          启动 HTTP 服务并阻塞直到收到信号
//	Shutdown     反序执行已登记的清理函数
//
// 每个阶段返回错误即可让上层决定是否中止启动；各资源在创建时把对应清理登记到
// cleanup 栈，关闭时按后进先出（LIFO）顺序释放。
type App struct {
	settings   *config.Settings
	engine     *gin.Engine
	server     *http.Server
	reload     *ReloadManager
	dbReady    bool
	oauthReady bool

	cleanups []cleanupFunc
}

// NewApp 构造一个空的 App。真正的初始化在各阶段方法中完成。
func NewApp() *App {
	return &App{
		reload: NewReloadManager(),
	}
}

// addCleanup 登记一个关闭阶段的清理函数（后进先出执行）。
func (a *App) addCleanup(name string, fn func(ctx context.Context) error) {
	a.cleanups = append(a.cleanups, cleanupFunc{name: name, fn: fn})
}

// Bootstrap 初始化基础设施：数据目录、数据库、配置快照。
func (a *App) Bootstrap() error {
	if err := os.MkdirAll("./data/theme", os.ModePerm); err != nil {
		return fmt.Errorf("failed to create theme directory: %w", err)
	}

	// 注入版本标识，供 dbcore 在检测到版本升级时自动备份 ./data。
	// 必须在 Initialize() 之前设置。
	dbcore.SetVersionID(utils.CurrentVersion + "-" + utils.VersionHash)

	// 显式初始化数据库（返回错误而非在 getter 里 log.Fatal）。
	if err := dbcore.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	a.dbReady = true
	a.addCleanup("database", func(context.Context) error {
		return dbcore.Close()
	})
	if err := frontendpublic.EnsureBundledThemes(); err != nil {
		return fmt.Errorf("failed to initialize public themes: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)

	if err := normalizeMetricStorageSettings(); err != nil {
		return fmt.Errorf("failed to normalize metric storage settings: %w", err)
	}
	if err := normalizeSiteFactoryDefaults(); err != nil {
		return fmt.Errorf("failed to normalize site factory defaults: %w", err)
	}

	conf, err := config.GetManyAs[config.Settings]()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	a.settings = conf
	return nil
}

// normalizeMetricStorageSettings makes the active storage policy independent
// of settings saved by older upstream, stable, fix, or snapshot releases.
// Per-metric retention values are deliberately left untouched.
func normalizeMetricStorageSettings() error {
	return config.SetMany(map[string]any{
		config.LowResourceModeKey:                false,
		metricstore.MetricDownsamplingEnabledKey: true,
	})
}

func normalizeSiteFactoryDefaults() error {
	all, err := config.GetAll()
	if err != nil {
		return err
	}
	updates := map[string]any{
		config.ReduceMotionKey: false,
	}
	migrated, _ := all[config.SiteFactoryDefaultsKey].(bool)
	if !migrated {
		if description, ok := all[config.DescriptionKey].(string); ok && config.IsLegacyDefaultDescription(description) {
			updates[config.DescriptionKey] = config.DefaultSiteDescription
		}
		if pageSize, ok := configValueAsInt(all[config.AdminDefaultPageSizeKey]); ok && pageSize == 10 {
			updates[config.AdminDefaultPageSizeKey] = config.AdminDefaultPageSize
		}
		updates[config.SiteFactoryDefaultsKey] = true
	}
	return config.SetMany(updates)
}

func configValueAsInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

// InitStores 初始化独立存储组件（metric store）并执行 metrics 迁移。
//
// metric store 现在始终启用（旧的 metric_store_enabled 开关已废弃）：
// 未显式配置时使用 SQLite（./data/metrics.db），否则使用配置的 MySQL/PostgreSQL。
// 初始化失败即启动失败，不再静默 fallback 到旧 records 表。
//
// 初始化成功后先执行需要 metric store 的一次性迁移，再执行启动迁移：当 metrics
// 存储后端发生变化（例如从默认 SQLite 切换到 MySQL/PostgreSQL）时，把上一个
// metrics 目标库的数据搬运到当前目标。迁移失败同样让启动失败，并打印明确错误。
func (a *App) InitStores() error {
	if err := metricstore.InitializeStore(); err != nil {
		auditlog.EventLog("error", fmt.Sprintf("Failed to initialize metric store: %v", err))
		return fmt.Errorf("failed to initialize metric store: %w", err)
	}
	a.addCleanup("metric-store", func(ctx context.Context) error {
		return metricstore.CloseStoreContext(ctx)
	})

	if err := migrations.RunMetricStoreMigrations(migrations.MetricContext{
		DB:    dbcore.GetDBInstance(),
		Store: metricstore.GetStore(),
	}); err != nil {
		auditlog.EventLog("error", fmt.Sprintf("Metric store one-shot migrations failed: %v", err))
		return fmt.Errorf("metric store one-shot migrations failed: %w", err)
	}

	// 存储后端切换时把上一个 metrics 目标库的数据搬运到当前目标（失败即启动失败）。
	if err := metricstore.RunStartupMigration(); err != nil {
		auditlog.EventLog("error", fmt.Sprintf("Metrics startup migration failed: %v", err))

		return fmt.Errorf("metrics startup migration failed: %w", err)
	}
	processPendingMetricCleanupAtStartup(dbcore.GetDBInstance())
	var registeredClients []models.Client
	if err := dbcore.GetDBInstance().Select("uuid").Find(&registeredClients).Error; err != nil {
		return fmt.Errorf("failed to list clients for metrics orphan cleanup: %w", err)
	}
	validEntities := make(map[string]struct{}, len(registeredClients))
	for _, client := range registeredClients {
		validEntities[client.UUID] = struct{}{}
	}
	var pingTasks []models.PingTask
	if err := dbcore.GetDBInstance().Select("id", "clients").Find(&pingTasks).Error; err != nil {
		return fmt.Errorf("failed to list ping tasks for metrics orphan cleanup: %w", err)
	}
	validPingAssignments := make(map[uint]map[string]struct{}, len(pingTasks))
	for _, task := range pingTasks {
		clients := make(map[string]struct{}, len(task.Clients))
		for _, client := range task.Clients {
			clients[client] = struct{}{}
		}
		validPingAssignments[task.Id] = clients
	}
	orphanResult, err := metricstore.CleanupOrphanedData(context.Background(), validEntities, validPingAssignments)
	if err != nil {
		return fmt.Errorf("failed to clean orphaned metrics: %w", err)
	}
	if orphanResult.Entities > 0 || orphanResult.PingTasks > 0 || orphanResult.PingAssignments > 0 {
		logger.Infof("metricstore", "Removed orphaned metric data (entities=%d ping_tasks=%d ping_assignments=%d)", orphanResult.Entities, orphanResult.PingTasks, orphanResult.PingAssignments)
	}
	metricstore.StartReportBatcher()
	a.addCleanup("metric-report-batcher", func(ctx context.Context) error {
		return metricstore.StopReportBatcher(ctx)
	})
	return nil
}

func processPendingMetricCleanupAtStartup(db *gorm.DB) {
	if err := metricstore.ProcessPendingCleanupJobs(context.Background(), db); err != nil {
		logger.Errorf("metricstore", "Pending metric cleanup failed during startup and will be retried in the background: %v", err)
	}
}

// CommitRestore marks a staged backup as fully usable only after the main
// database, metric store, providers, and router all initialized. Background
// tasks start afterwards so a candidate restore cannot emit notifications or
// perform scheduled writes before it becomes durable.
func (a *App) CommitRestore() error {
	if err := dbcore.CommitPendingRestore(); err != nil {
		return fmt.Errorf("commit verified backup restore: %w", err)
	}
	return nil
}

// InitProviders 初始化外部 provider。
//
// OAuth 必须在 HTTP 服务开始接收请求之前同步完成，否则 oauth.CurrentProvider()
// 存在空指针风险；GeoIP 与消息发送允许后台初始化。
func (a *App) InitProviders() error {
	a.initOAuth()

	// GeoIP：可能涉及下载/加载 mmdb，放后台执行避免拖慢启动。
	go geoip.InitGeoIp()
	a.addCleanup("geoip", func(context.Context) error {
		return geoip.Shutdown()
	})

	// 消息发送 provider。
	messageSender.Initialize()
	a.addCleanup("message-sender", func(context.Context) error {
		return messageSender.Shutdown()
	})

	return nil
}

// initOAuth initializes the provider once. The restricted upgrade server also
// needs OAuth login, so provider setup may happen before the normal provider
// stage without registering duplicate cleanup work.
func (a *App) initOAuth() {
	if a.oauthReady {
		return
	}
	if err := oauth.Initialize(); err != nil {
		// Keep password login available when an OAuth provider is misconfigured.
		logger.Errorf("server", "Failed to initialize OAuth provider: %v", err)
		auditlog.EventLog("error", fmt.Sprintf("Failed to initialize OAuth provider: %v", err))
	}
	a.oauthReady = true
	a.addCleanup("oauth", func(context.Context) error {
		return oauth.Shutdown()
	})
}

func (a *App) LegacyUpgradeRequired() (bool, migrations.LegacyMonitoringSummary, error) {
	return migrations.LegacyMonitoringMigrationRequired(dbcore.GetDBInstance())
}

func (a *App) MetricStorageUpgradeRequired() (bool, metric.SQLiteMigrationSummary, error) {
	summary, err := metricstore.InspectSQLiteStorageMigration(context.Background())
	return summary.Required, summary, err
}

// InstallRequired reports whether the instance still needs the first-run guide.
// A zero-user database is deliberately treated as incomplete so an interrupted
// guide can be resumed after a restart.
func (a *App) InstallRequired() (bool, error) {
	var count int64
	if err := dbcore.GetDBInstance().Model(&models.User{}).Count(&count).Error; err != nil {
		return false, err
	}
	return count == 0, nil
}

func firstRunInstallRedirect() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if c.Request.Method == http.MethodGet &&
			!strings.HasPrefix(requestPath, "/api") &&
			requestPath != installweb.PagePath &&
			filepath.Ext(requestPath) == "" {
			c.Redirect(http.StatusTemporaryRedirect, installweb.PagePath)
			c.Abort()
			return
		}
		c.Next()
	}
}

// RunInstallGuide starts the restricted first-run HTTP server.
func (a *App) RunInstallGuide() (bool, error) {
	controller := installweb.NewController(dbcore.GetDBInstance())
	controller.Activate()
	defer controller.Deactivate()

	r := gin.New()
	r.Use(logger.GinLogger())
	r.Use(logger.GinRecovery())
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	r.Use(firstRunInstallRedirect())
	controller.Register(r)
	frontendpublic.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
		r.NoRoute(func(c *gin.Context) {
			requestPath := c.Request.URL.Path
			if strings.HasPrefix(requestPath, "/api") {
				api.RespondError(c, http.StatusNotFound, "Not found in install mode")
				return
			}
			for _, handler := range handlers {
				handler(c)
				if c.IsAborted() {
					return
				}
			}
		})
	})

	a.engine = r
	a.server = &http.Server{Addr: flags.Listen, Handler: r}
	serverErr := make(chan error, 1)
	logger.Infof("server", "First-run installation guide is available on %s", flags.Listen)
	go func() {
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)
	select {
	case err := <-serverErr:
		return false, fmt.Errorf("listen in install mode: %w", err)
	case <-quit:
		return false, a.Shutdown()
	case <-controller.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.server.Shutdown(ctx); err != nil {
			return false, fmt.Errorf("stop install server: %w", err)
		}
		a.server = nil
		a.engine = nil
		return true, nil
	}
}

// RunLegacyUpgrade starts a restricted HTTP server on the normal listen
// address. It returns only after the migration finishes and the restricted
// listener has released the port, or after shutdown/interruption.
func (a *App) RunLegacyUpgrade(summary migrations.LegacyMonitoringSummary) (bool, error) {
	a.initOAuth()
	controller := upgradeweb.NewController(dbcore.GetDBInstance(), summary)
	controller.Activate()
	defer controller.Deactivate()

	r := gin.New()
	r.Use(logger.GinLogger())
	r.Use(logger.GinRecovery())
	cors := security.NewCorsController(a.settings.CorsOriginCheckEnabled, a.settings.CorsAllowedOrigins)
	r.Use(cors.Middleware())
	r.Use(api.IdentityMiddleware())
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})

	controller.Register(r)
	frontendpublic.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
		r.NoRoute(func(c *gin.Context) {
			requestPath := c.Request.URL.Path
			if strings.HasPrefix(requestPath, "/api") {
				api.RespondError(c, http.StatusNotFound, "Not found in upgrade mode")
				return
			}
			if c.Request.Method == http.MethodGet && requestPath != upgradeweb.PagePath && filepath.Ext(requestPath) == "" {
				c.Redirect(http.StatusTemporaryRedirect, upgradeweb.PagePath)
				return
			}
			for _, handler := range handlers {
				handler(c)
				if c.IsAborted() {
					return
				}
			}
		})
	})

	a.engine = r
	a.server = &http.Server{Addr: flags.Listen, Handler: r}
	serverErr := make(chan error, 1)
	logger.Infof("server", "Legacy monitoring data requires the 1.2.7 upgrade wizard on %s", flags.Listen)
	go func() {
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-serverErr:
		return false, fmt.Errorf("listen in upgrade mode: %w", err)
	case <-quit:
		return false, a.Shutdown()
	case <-controller.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.server.Shutdown(ctx); err != nil {
			return false, fmt.Errorf("stop upgrade server: %w", err)
		}
		a.server = nil
		a.engine = nil
		return true, nil
	}
}

// RunMetricStorageUpgrade exposes only authentication and SQLite migration
// progress. Normal application routes remain unavailable until validation
// succeeds and the restricted listener releases the port.
func (a *App) RunMetricStorageUpgrade(summary metric.SQLiteMigrationSummary) (bool, error) {
	a.initOAuth()
	controller := storageupdateweb.NewController(summary)
	controller.Activate()
	defer controller.Deactivate()

	r := gin.New()
	r.Use(logger.GinLogger())
	r.Use(logger.GinRecovery())
	cors := security.NewCorsController(a.settings.CorsOriginCheckEnabled, a.settings.CorsAllowedOrigins)
	r.Use(cors.Middleware())
	r.Use(api.IdentityMiddleware())
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})

	controller.Register(r)
	frontendpublic.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
		r.NoRoute(func(c *gin.Context) {
			requestPath := c.Request.URL.Path
			if strings.HasPrefix(requestPath, "/api") {
				api.RespondError(c, http.StatusNotFound, "Not found in metric storage upgrade mode")
				return
			}
			if c.Request.Method == http.MethodGet && requestPath != storageupdateweb.PagePath && filepath.Ext(requestPath) == "" {
				c.Redirect(http.StatusTemporaryRedirect, storageupdateweb.PagePath)
				return
			}
			for _, handler := range handlers {
				handler(c)
				if c.IsAborted() {
					return
				}
			}
		})
	})

	a.engine = r
	a.server = &http.Server{Addr: flags.Listen, Handler: r}
	serverErr := make(chan error, 1)
	logger.Infof("server", "Metric storage upgrade progress is available on %s", flags.Listen)
	go func() {
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-serverErr:
		return false, fmt.Errorf("listen in metric storage upgrade mode: %w", err)
	case <-quit:
		return false, a.Shutdown()
	case <-controller.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.server.Shutdown(ctx); err != nil {
			return false, fmt.Errorf("stop metric storage upgrade server: %w", err)
		}
		a.server = nil
		a.engine = nil
		return true, nil
	}
}

// StartBackground 启动后台工作：定时任务。
func (a *App) StartBackground() error {
	stopMetricCleanup := metricstore.StartPendingCleanupWorker(dbcore.GetDBInstance())
	a.addCleanup("metric-cleanup", func(context.Context) error {
		stopMetricCleanup()
		return nil
	})

	stopReturnRouteRules := tasks.StartReturnRouteRuleWatcher()
	a.addCleanup("return-route-rules", func(context.Context) error {
		stopReturnRouteRules()
		return nil
	})

	registerScheduledWork()
	a.addCleanup("scheduler", func(context.Context) error {
		corn.StopAll()
		return nil
	})

	if err := cloudflared.AutoStart(GetEnvFirst("", "LITE_CLOUDFLARED_TOKEN", "KOMARI_CLOUDFLARED_TOKEN")); err != nil {
		logger.Errorf("cloudflared", "failed to auto start: %v", err)
	}
	a.addCleanup("cloudflared", func(context.Context) error {
		cloudflared.Shutdown()
		return nil
	})
	return nil
}

// registerReloadHandlers 把此前散落各处的 config.Subscribe 统一登记到 reload 管理器。
func (a *App) registerReloadHandlers(cors *security.CorsController) {
	// OAuth provider 切换。
	a.reload.Register("oauth-provider", func(event config.ConfigEvent) {
		if ok, t := config.IsChangedT[string](event, config.OAuthProviderKey); ok {
			if t == "" || t == "none" {
				t = "github"
			}
			oidcProvider, err := database.GetOidcConfigByName(t)
			if err != nil {
				logger.Errorf("server", "Failed to get OIDC provider config: %v", err)
				return
			}
			logger.Infof("server", "Using %s as OIDC provider", oidcProvider.Name)
			if err := oauth.LoadProvider(oidcProvider.Name, oidcProvider.Addition); err != nil {
				auditlog.EventLog("error", fmt.Sprintf("Failed to load OIDC provider: %v", err))
			}
		}
	})

	// GeoIP provider 切换。
	a.reload.Register("geoip-provider", func(event config.ConfigEvent) {
		if event.IsChanged(config.GeoIpProviderKey) {
			go geoip.InitGeoIp()
		}
	})

	// 消息发送方式切换。
	a.reload.Register("message-sender", func(event config.ConfigEvent) {
		if event.IsChanged(config.NotificationMethodKey) {
			go messageSender.Initialize()
		}
	})

	// 流量报告发送时间切换（固定按北京时间解释）。
	a.reload.Register("traffic-report-schedule", func(event config.ConfigEvent) {
		if event.IsChanged(config.TrafficReportTimeKey) {
			if err := notifier.ReloadTrafficReportSchedule(); err != nil {
				logger.Errorf("server", "Failed to reload traffic report schedule: %v", err)
			}
		}
	})

	// CORS 配置热更新。
	a.reload.Register("cors", func(event config.ConfigEvent) {
		cors.Update(event)
	})
}

// BuildRouter 构建 Gin 引擎、中间件与全部路由，并登记热重载处理器。
func (a *App) BuildRouter() error {
	if err := upload.DefaultStore.CleanupAll(); err != nil {
		logger.Errorf("upload", "Failed to clean interrupted uploads: %v", err)
	}
	r := gin.New()
	r.Use(logger.GinLogger())
	r.Use(logger.GinRecovery())

	cors := security.NewCorsController(a.settings.CorsOriginCheckEnabled, a.settings.CorsAllowedOrigins)
	r.Use(cors.Middleware())

	r.Use(api.IdentityMiddleware())
	r.Use(api.PrivateSiteMiddleware())

	r.Use(func(c *gin.Context) {
		if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})

	router.Register(r)

	// 集中登记并启动热重载订阅。
	a.registerReloadHandlers(cors)
	a.reload.Start()

	a.engine = r
	return nil
}

// Run 启动 HTTP 服务并阻塞直到收到中断信号或服务异常退出。
func (a *App) Run() error {
	a.server = &http.Server{
		Addr:    flags.Listen,
		Handler: httpsserver.Default.HTTPRedirectHandler(a.engine),
	}
	listener, err := listenAndFinalizeStartup(
		flags.Listen,
		a.CommitRestore,
		a.StartBackground,
	)
	if err != nil {
		a.onFatal(err)
		return err
	}

	httpsSettings, err := httpsserver.LoadSettings()
	if err != nil {
		logger.Errorf("https", "Failed to load built-in HTTPS settings: %v", err)
	} else if err := httpsserver.Default.Start(a.engine, httpsSettings, flags.Listen); err != nil {
		// Keep HTTP available so an invalid certificate or occupied HTTPS port
		// can still be corrected from the admin page.
		logger.Errorf("https", "Built-in HTTPS did not start: %v", err)
	}
	a.addCleanup("https-server", func(ctx context.Context) error {
		return httpsserver.Default.Shutdown(ctx)
	})

	serverErr := make(chan error, 1)
	logger.Infof("server", "Starting server on %s ...", flags.Listen)
	go func() {
		if err := a.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		a.onFatal(err)
		return fmt.Errorf("listen: %w", err)
	case <-quit:
		return a.Shutdown()
	}
}

func listenAndFinalizeStartup(
	address string,
	commitRestore func() error,
	startBackground func() error,
) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = listener.Close()
		}
	}()
	if err := commitRestore(); err != nil {
		return nil, fmt.Errorf("commit verified backup restore: %w", err)
	}
	if err := startBackground(); err != nil {
		return nil, fmt.Errorf("start background work: %w", err)
	}
	closeOnError = false
	return listener, nil
}

// Shutdown 优雅关闭：先停止接收新请求，再反序执行已登记的清理函数。
func (a *App) Shutdown() error {
	if a.dbReady {
		auditlog.Log("", "", "server is shutting down", "info")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 先关闭 HTTP 服务，停止接收新请求。
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			logger.Infof("server", "HTTP server forced to shutdown: %v", err)
		}
	}

	// 反序释放资源（后进先出）。
	for i := len(a.cleanups) - 1; i >= 0; i-- {
		c := a.cleanups[i]
		if err := c.fn(ctx); err != nil {
			logger.Errorf("server", "cleanup %q failed: %v", c.name, err)
		}
	}
	return nil
}

// onFatal 处理 HTTP 服务致命错误：尽力释放已登记资源。
func (a *App) onFatal(err error) {
	if a.dbReady {
		auditlog.Log("", "", "server encountered a fatal error: "+err.Error(), "error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := len(a.cleanups) - 1; i >= 0; i-- {
		c := a.cleanups[i]
		if cerr := c.fn(ctx); cerr != nil {
			logger.Errorf("server", "cleanup %q failed: %v", c.name, cerr)
		}
	}
}

// registerScheduledWork 注册所有定时任务与首启动同步逻辑。
func registerScheduledWork() {
	if err := tasks.ReloadPingSchedule(); err != nil {
		logger.ErrorArgs("server", "Failed to reload ping schedule:", err)
	}
	if err := tasks.ReloadReturnRouteSchedule(); err != nil {
		logger.ErrorArgs("server", "Failed to reload return route schedule:", err)
	}
	if err := d_notification.ReloadLoadNotificationSchedule(); err != nil {
		logger.ErrorArgs("server", "Failed to reload load notification schedule:", err)
	}
	// Finish the one-time traffic-ledger backfill before compaction starts so a
	// low-resource server never performs both history scans concurrently.
	if err := d_notification.EnsureTrafficReportMetricRetention(context.Background()); err != nil {
		logger.Errorf("server", "Failed to ensure traffic report metric retention: %v", err)
	}

	if err := corn.AddFunc("records:cleanup", "@every 30m", cleanupScheduledData); err != nil {
		logger.ErrorArgs("server", "Failed to add cleanup scheduled task:", err)
	}
	if err := corn.AddContextFunc("metrics:compact", "@every 10s", true, compactMetricStore); err != nil {
		logger.ErrorArgs("server", "Failed to add metric compact scheduled task:", err)
	}
	if err := corn.AddFunc("notifier:traffic", "@every 1m", notifier.CheckTraffic); err != nil {
		logger.ErrorArgs("server", "Failed to add traffic notification task:", err)
	}
	if err := corn.AddFunc("notifier:expire", "0 0 9 * * *", notifier.CheckExpireScheduledWork); err != nil {
		logger.ErrorArgs("server", "Failed to add expire notification scheduled task:", err)
	}
	if err := corn.AddContextFunc("billing:fx-refresh", "@every 6h", true, func(ctx context.Context) {
		if _, refreshErr := billing.RefreshFX(ctx, dbcore.GetDBInstance(), nil, ""); refreshErr != nil {
			logger.Errorf("billing", "Failed to refresh reference rates: %v", refreshErr)
		}
	}); err != nil {
		logger.ErrorArgs("billing", "Failed to add reference-rate task:", err)
	}
	if err := corn.AddFuncInLocation("billing:accrue", "0 10 0 * * *", billing.BeijingLocation, func() {
		now := time.Now().UTC()
		if accrueErr := billing.EnsureAccruedThrough(context.Background(), dbcore.GetDBInstance(), billing.BeijingDay(now).AddDate(0, 0, -1)); accrueErr != nil {
			logger.Errorf("billing", "Failed to accrue the previous billing day: %v", accrueErr)
		}
	}); err != nil {
		logger.ErrorArgs("billing", "Failed to add daily accrual task:", err)
	}

	notifier.InitTrafficReportSchedule()
	notifier.InitPingLossNotificationSchedule()
}

const taskResultRetentionDays = 30

func cleanupScheduledData() {
	before := time.Now().UTC().Add(-24 * time.Hour * taskResultRetentionDays)
	if err := tasks.ClearTaskResultsByTimeBefore(before); err != nil {
		logger.Errorf("server", "Failed to clean expired task results: %v", err)
	}

	auditlog.RemoveOldLogs()
	accounts.RemoveExpiredSessions()
}

func compactMetricStore(ctx context.Context) {
	compactCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	written, cycleCompleted, err := metricstore.CompactStep(compactCtx, time.Now().UTC())
	if errors.Is(err, metricstore.ErrCompactInProgress) {
		return
	}
	metricCompactCycle.add(written, err)
	if !cycleCompleted {
		return
	}
	cycleWritten, cycleErr := metricCompactCycle.finish()
	if cycleErr != nil {
		logger.Errorf("server", "Metric store compact cycle finished after writing %d rollup buckets: %v", cycleWritten, cycleErr)
		return
	}
	if cycleWritten > 0 {
		logger.Infof("server", "Metric store compacted %d rollup buckets", cycleWritten)
	}
}

type metricCompactCycleState struct {
	sync.Mutex
	written int
	errors  []error
}

var metricCompactCycle metricCompactCycleState

func (s *metricCompactCycleState) add(written int, err error) {
	s.Lock()
	defer s.Unlock()
	s.written += written
	if err != nil {
		s.errors = append(s.errors, err)
	}
}

func (s *metricCompactCycleState) finish() (int, error) {
	s.Lock()
	defer s.Unlock()
	written := s.written
	err := errors.Join(s.errors...)
	s.written = 0
	s.errors = nil
	return written, err
}

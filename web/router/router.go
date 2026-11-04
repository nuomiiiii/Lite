package router

import (
	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/api/admin"
	"github.com/nuomiiiii/lite/web/api/client"
	public_api "github.com/nuomiiiii/lite/web/api/public"
	"github.com/nuomiiiii/lite/web/api/remote"
	installweb "github.com/nuomiiiii/lite/web/install"
	"github.com/nuomiiiii/lite/web/mcp"
	"github.com/nuomiiiii/lite/web/public"
	jsonRpc "github.com/nuomiiiii/lite/web/rpc/jsonrpc"
)

// Register binds all HTTP, WebSocket, JSON-RPC and static frontend routes.
//
// 设计：JSON 类接口统一经声明式路由桥 jsonRpc.Bind 绑定到对应 RPC2 方法，
// 不再有 per-resource gin handler 层。仅二进制/流/重定向/特殊鉴权类接口保留为 REST handler。
func Register(r *gin.Engine) {
	r.Any("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})

	registerPublicRoutes(r)
	mcp.RegisterPublic(r)
	registerAgentRoutes(r)
	registerAdminRoutes(r)

	public.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
		r.NoRoute(handlers...)
	})
}

// registerPublicRoutes 公开路由。JSON 读接口经 Bind 绑定到 public: 命名空间方法。
func registerPublicRoutes(r *gin.Engine) {
	installweb.RegisterCompleted(r)

	// 非 JSON / 特殊流程，保留 REST handler。
	r.POST("/api/login", public_api.Login)
	r.POST("/api/session/touch", api.RejectAPIKey(), public_api.TouchSession)
	r.POST("/api/passkeys/login/options", public_api.PasskeyLoginOptions)
	r.POST("/api/passkeys/login/verify", public_api.PasskeyLoginVerify)
	r.GET("/api/logout", public_api.Logout)
	r.GET("/api/oauth", public_api.OAuth)
	r.GET("/api/oauth_callback", public_api.OAuthCallback)
	r.GET("/api/mjpeg_live", public_api.MjpegLiveHandler)
	// /api/clients 是 WebSocket 端点（客户端发 "get"/"get <uuid>" 拉取在线列表与最新上报），
	// 非 JSON-RPC，保留为 WS handler。
	r.GET("/api/clients", api.GetClients)

	// JSON 接口 -> RPC2。
	r.GET("/api/me", jsonRpc.Bind("public:getMe", jsonRpc.WithRaw()))
	r.GET("/api/nodes", jsonRpc.Bind("public:getNodesInformation"))
	r.GET("/api/public", jsonRpc.Bind("public:getPublicSettings"))
	r.GET("/api/version", jsonRpc.Bind("public:getVersion"))
	r.GET("/api/recent/:uuid", jsonRpc.Bind("public:getClientRecentRecords", jsonRpc.WithPath("uuid")))
	r.GET("/api/records/load", jsonRpc.Bind("public:getRecordsByUUID", jsonRpc.WithQuery("uuid", "load_type", "hours")))
	r.GET("/api/records/ping", jsonRpc.Bind("public:getPingRecords", jsonRpc.WithQuery("uuid", "task_id", "hours")))
	r.GET("/api/task/ping", jsonRpc.Bind("public:getPublicPingTasks"))
	r.GET("/api/task/return-route", jsonRpc.Bind("public:getPublicReturnRoutes"))

	// JSON-RPC 直连入口。
	r.GET("/api/rpc2", jsonRpc.OnRpcRequest)
	r.POST("/api/rpc2", jsonRpc.OnRpcRequest)
}

// registerAgentRoutes agent（客户端）上报与拉取路由。
func registerAgentRoutes(r *gin.Engine) {
	agentOnly := r.Group("/api/clients", api.RequireRole(api.RoleClient))
	{
		agentOnly.GET("/v2/rpc", client.WebSocketV2RPC)
		agentOnly.POST("/v2/rpc", client.UploadV2RPC)
		agentOnly.GET("/remote", remote.EstablishAgent)
	}
}

// registerAdminRoutes 管理员路由。除二进制/流类外全部经 Bind 绑定到 admin: 命名空间方法。
func registerAdminRoutes(r *gin.Engine) {
	g := r.Group("/api/admin", api.RequireRole(api.RoleAdmin))
	g.GET("/dashboard", jsonRpc.Bind("admin:getDashboard", jsonRpc.WithQuery("sections", "limit"), jsonRpc.WithRaw()))
	g.GET("/dashboard/charts", jsonRpc.Bind("admin:getDashboardCharts", jsonRpc.WithQuery("sections", "limit"), jsonRpc.WithRaw()))
	g.GET("/dashboard/alerts", jsonRpc.Bind("admin:getDashboardAlertItems", jsonRpc.WithQuery("kind"), jsonRpc.WithRaw()))
	g.GET("/dashboard/traffic-day", jsonRpc.Bind("admin:getDashboardTrafficDay", jsonRpc.WithQuery("day"), jsonRpc.WithRaw()))
	billingGroup := g.Group("/billing")
	{
		billingGroup.GET("/overview", jsonRpc.Bind("admin:getBillingOverview", jsonRpc.WithQuery("currency")))
		billingGroup.GET("/servers", jsonRpc.Bind("admin:getBillingServers", jsonRpc.WithQuery("currency", "q", "native_currencies", "regions", "groups", "expiry", "page", "page_size")))
		billingGroup.GET("/periods/monthly", jsonRpc.Bind("admin:getBillingMonthly", jsonRpc.WithQuery("currency", "years", "months", "clients", "types", "native_currencies", "page", "page_size")))
		billingGroup.GET("/periods/yearly", jsonRpc.Bind("admin:getBillingYearly", jsonRpc.WithQuery("currency", "years", "clients", "types", "native_currencies", "page", "page_size")))
		billingGroup.GET("/entries", jsonRpc.Bind("admin:getBillingEntries", jsonRpc.WithQuery("currency", "client", "from", "to", "types", "q", "page", "page_size")))
		billingGroup.POST("/entries/:id/void", jsonRpc.Bind("admin:voidBillingEntry", jsonRpc.WithPath("id")))
	}

	// --- 二进制/流/重定向类，保留 REST handler ---
	g.GET("/download/backup", api.RejectAPIKey(), admin.DownloadBackup)
	uploadHandler := admin.NewArchiveUploadHandler()
	uploadGroup := g.Group("/upload")
	uploadGroup.Use(api.RejectAPIKey())
	{
		uploadGroup.POST("/init", uploadHandler.Init)
		uploadGroup.POST("/chunk", uploadHandler.Chunk)
		uploadGroup.POST("/merge", uploadHandler.Merge)
		uploadGroup.POST("/cancel", uploadHandler.Cancel)
	}
	g.GET("/test/geoip", jsonRpc.Bind("admin:testGeoip", jsonRpc.WithQuery("ip")))
	g.POST("/test/sendMessage", jsonRpc.Bind("admin:testSendMessage"))
	g.POST("/update/mmdb", admin.UpdateMmdbGeoIP)
	g.POST("/update/user", api.RejectAPIKey(), admin.UpdateUser)
	g.PUT("/update/favicon", admin.UploadFavicon)
	g.POST("/update/favicon", admin.DeleteFavicon)
	account := g.Group("/account")
	account.Use(api.RejectAPIKey())
	{
		account.POST("/avatar", admin.UploadAccountAvatar)
		account.GET("/avatar/:version", admin.GetAccountAvatar)
		account.DELETE("/avatar", admin.DeleteAccountAvatar)
		account.GET("/passkeys", admin.ListPasskeys)
		account.POST("/passkeys/confirm/options", admin.PasskeyConfirmOptions)
		account.POST("/passkeys/register/options", admin.PasskeyRegisterOptions)
		account.POST("/passkeys/register/verify", admin.PasskeyRegisterVerify)
		account.PATCH("/passkeys/:id", admin.RenamePasskey)
		account.DELETE("/passkeys/:id", admin.DeletePasskey)
	}
	g.GET("/settings/https", admin.GetHTTPSSettings)
	g.POST("/settings/https", admin.UpdateHTTPSSettings)
	g.POST("/settings/https/reload", admin.ReloadHTTPSCertificate)

	// theme 含文件操作，保留 REST handler。API Key 不能改主题。
	theme := g.Group("/theme")
	theme.Use(api.RejectAPIKey())
	{
		theme.GET("/list", admin.ListThemes)
		theme.POST("/delete", admin.DeleteTheme)
		theme.GET("/set", admin.SetTheme)
		theme.POST("/update", admin.UpdateTheme)
		theme.POST("/update-all", admin.UpdateAllThemes)
		theme.GET("/auto-update", admin.GetThemeAutoUpdate)
		theme.POST("/auto-update", admin.SetThemeAutoUpdate)
		theme.POST("/import", admin.ImportTheme)
		theme.POST("/settings", admin.UpdateThemeSettings)
		theme.GET("/market/sources", admin.ListThemeMarketSources)
		theme.POST("/market/sources", admin.CreateThemeMarketSource)
		theme.PUT("/market/sources/:id", admin.UpdateThemeMarketSource)
		theme.DELETE("/market/sources/:id", admin.DeleteThemeMarketSource)
		theme.GET("/market/catalog", admin.ListThemeMarketCatalog)
		theme.GET("/market/preview", admin.ServeThemeMarketPreview)
		theme.POST("/market/install", admin.InstallThemeFromMarket)
	}

	// 2FA 含二维码 PNG / 敏感操作，保留 REST handler。API Key 不能改登录验证。
	twoFactor := g.Group("/2fa")
	twoFactor.Use(api.RejectAPIKey())
	{
		twoFactor.GET("/generate", admin.Generate2FA)
		twoFactor.POST("/enable", admin.Enable2FA)
		twoFactor.POST("/disable", api.RequireSensitive2FA(), admin.Disable2FA)
	}

	// oauth2 绑定走重定向，保留 REST handler。API Key 不能改登录绑定。
	oauth2 := g.Group("/oauth2")
	oauth2.Use(api.RejectAPIKey())
	{
		oauth2.GET("/bind", admin.BindingExternalAccount)
		oauth2.GET("/confirm-passkey", admin.ConfirmPasskeyExternalAccount)
		oauth2.POST("/unbind", admin.UnbindExternalAccount)
	}

	// --- 以下全部 JSON -> RPC2 ---

	// tasks（远程执行）
	task := g.Group("/task")
	{
		task.GET("/all", jsonRpc.Bind("admin:getTasks"))
		task.POST("/exec", jsonRpc.Bind("admin:exec"))
		task.GET("/:task_id", jsonRpc.Bind("admin:getTaskById", jsonRpc.WithPath("task_id")))
		task.GET("/:task_id/result", jsonRpc.Bind("admin:getTaskResultsByTaskId", jsonRpc.WithPath("task_id")))
		task.GET("/:task_id/result/:uuid", jsonRpc.Bind("admin:getSpecificTaskResult", jsonRpc.WithPath("task_id", "uuid")))
		task.GET("/client/:uuid", jsonRpc.Bind("admin:getTasksByClientId", jsonRpc.WithPath("uuid")))
	}
	g.GET("/scheduled-exec", jsonRpc.Bind("admin:listScheduledExec"))
	g.POST("/scheduled-exec", jsonRpc.Bind("admin:createScheduledExec"))
	g.POST("/scheduled-exec/order", jsonRpc.Bind("admin:reorderScheduledExec"))
	g.PUT("/scheduled-exec/:id", jsonRpc.Bind("admin:updateScheduledExec", jsonRpc.WithPath("id")))
	g.DELETE("/scheduled-exec/:id", jsonRpc.Bind("admin:deleteScheduledExec", jsonRpc.WithPath("id")))
	g.POST("/scheduled-exec/:id/enabled", jsonRpc.Bind("admin:setScheduledExecEnabled", jsonRpc.WithPath("id")))
	g.POST("/scheduled-exec/:id/run", jsonRpc.Bind("admin:runScheduledExec", jsonRpc.WithPath("id")))
	g.GET("/scheduled-exec/:id/runs", jsonRpc.Bind("admin:listScheduledExecRuns", jsonRpc.WithPath("id")))

	// settings
	settings := g.Group("/settings")
	{
		settings.GET("/", jsonRpc.Bind("admin:getSettings"))
		settings.POST("/", jsonRpc.Bind("admin:editSettings"))
		settings.GET("/xtermjs", jsonRpc.Bind("admin:getXtermjsSettings"))
		settings.POST("/xtermjs", jsonRpc.Bind("admin:setXtermjsSettings", jsonRpc.WithMessage("settings saved")))
		settings.GET("/dashboard", jsonRpc.Bind("admin:getDashboardSettings"))
		settings.POST("/dashboard", jsonRpc.Bind("admin:setDashboardSettings", jsonRpc.WithMessage("settings saved")))
		settings.POST("/oidc", jsonRpc.Bind("admin:setOidcProvider"))
		settings.GET("/oidc", jsonRpc.Bind("admin:getOidcProvider", jsonRpc.WithQuery("provider")))
		settings.POST("/message-sender", jsonRpc.Bind("admin:setMessageSenderProvider"))
		settings.GET("/message-sender", jsonRpc.Bind("admin:getMessageSenderProvider", jsonRpc.WithQuery("provider")))
		settings.GET("/cloudflared", jsonRpc.Bind("admin:getCloudflaredStatus"))
		settings.POST("/cloudflared/start", jsonRpc.Bind("admin:startCloudflared"))
		settings.POST("/cloudflared/stop", jsonRpc.Bind("admin:stopCloudflared"))
		settings.POST("/cloudflared/remove-token", jsonRpc.Bind("admin:removeCloudflaredToken"))
	}

	// database storage inspection and maintenance
	databaseGroup := g.Group("/database")
	{
		databaseGroup.GET("/size", jsonRpc.Bind("admin:getDatabaseSize"))
		databaseGroup.POST("/vacuum", jsonRpc.Bind("admin:vacuumDatabase"))
	}

	// clients
	clientGroup := g.Group("/client")
	{
		clientGroup.POST("/remote/authorize", remote.Authorize)
		clientGroup.POST("/remote/session", remote.CreateSession)
		clientGroup.POST("/remote/session/cancel", remote.CancelSession)
		clientGroup.POST("/remote/revoke", remote.RevokeGrant)
		clientGroup.GET("/remote", remote.ConnectBrowser)
		clientGroup.POST("/add", jsonRpc.Bind("admin:addClient", jsonRpc.WithFlat()))
		clientGroup.GET("/list", jsonRpc.Bind("admin:listClients", jsonRpc.WithRaw()))
		clientGroup.GET("/:uuid", jsonRpc.Bind("admin:getClient", jsonRpc.WithPath("uuid"), jsonRpc.WithRaw()))
		clientGroup.POST("/:uuid/edit", jsonRpc.Bind("admin:editClient", jsonRpc.WithPath("uuid")))
		clientGroup.POST("/:uuid/billing/traffic-reset", jsonRpc.Bind("admin:createBillingTrafficReset", jsonRpc.WithPath("uuid")))
		clientGroup.POST("/:uuid/billing/ip-change", jsonRpc.Bind("admin:createBillingIPChange", jsonRpc.WithPath("uuid")))
		clientGroup.POST("/:uuid/billing/one-time", jsonRpc.Bind("admin:createBillingOneTimeFee", jsonRpc.WithPath("uuid")))
		clientGroup.POST("/:uuid/remove", jsonRpc.Bind("admin:removeClient", jsonRpc.WithPath("uuid")))
		clientGroup.GET("/:uuid/token", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:getClientToken", jsonRpc.WithPath("uuid"), jsonRpc.WithFlat()))
		clientGroup.POST("/:uuid/token", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:getClientToken", jsonRpc.WithPath("uuid"), jsonRpc.WithFlat()))
		clientGroup.GET("/:uuid/deployment-profile", jsonRpc.Bind("admin:getClientDeploymentProfile", jsonRpc.WithPath("uuid"), jsonRpc.WithRaw()))
		clientGroup.POST("/:uuid/deployment-profile", jsonRpc.Bind("admin:saveClientDeploymentProfile", jsonRpc.WithPath("uuid"), jsonRpc.WithRaw()))
		clientGroup.GET("/:uuid/traffic-calibration", admin.GetTrafficCalibration)
		clientGroup.POST("/:uuid/traffic-calibration", admin.UpdateTrafficCalibration)
		clientGroup.GET("/:uuid/traffic-daily", jsonRpc.Bind("admin:getClientTrafficDaily", jsonRpc.WithPath("uuid"), jsonRpc.WithRaw()))
		clientGroup.POST("/token/rotate", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:rotateClientToken"))
		clientGroup.POST("/order", jsonRpc.Bind("admin:orderClients"))
	}

	// records
	record := g.Group("/record")
	{
		record.POST("/clear", jsonRpc.Bind("admin:clearRecords"))
		record.POST("/clear/all", jsonRpc.Bind("admin:clearAllRecords"))
	}

	// sessions
	session := g.Group("/session")
	{
		session.GET("/get", jsonRpc.Bind("admin:getSessions", jsonRpc.WithFlat()))
		session.POST("/remove", jsonRpc.Bind("admin:deleteSession"))
		session.POST("/remove/all", jsonRpc.Bind("admin:deleteAllSessions"))
	}

	g.GET("/logs", jsonRpc.Bind("admin:getLogs", jsonRpc.WithQuery("limit", "page", "q", "msg_type", "day")))

	mcp.RegisterAdmin(g)

	// clipboard
	clipboardGroup := g.Group("/clipboard")
	{
		clipboardGroup.GET("/:id", jsonRpc.Bind("admin:getClipboard", jsonRpc.WithPath("id")))
		clipboardGroup.GET("", jsonRpc.Bind("admin:listClipboard"))
		clipboardGroup.POST("", jsonRpc.Bind("admin:createClipboard"))
		clipboardGroup.POST("/:id", jsonRpc.Bind("admin:updateClipboard", jsonRpc.WithPath("id")))
		clipboardGroup.POST("/remove", jsonRpc.Bind("admin:batchDeleteClipboard"))
		clipboardGroup.POST("/:id/remove", jsonRpc.Bind("admin:deleteClipboard", jsonRpc.WithPath("id")))
	}

	// notifications
	notificationGroup := g.Group("/notification")
	{
		notificationGroup.GET("/center", jsonRpc.Bind("admin:listAlertCenter"))
		notificationGroup.POST("/center/silence", jsonRpc.Bind("admin:setAlertCenterSilence"))
		notificationGroup.GET("/offline", jsonRpc.Bind("admin:listOfflineNotifications"))
		notificationGroup.POST("/offline/edit", jsonRpc.Bind("admin:editOfflineNotification"))
		notificationGroup.POST("/offline/enable", jsonRpc.Bind("admin:enableOfflineNotification"))
		notificationGroup.POST("/offline/disable", jsonRpc.Bind("admin:disableOfflineNotification"))
		notificationGroup.GET("/offline/default", jsonRpc.Bind("admin:getOfflineNotificationDefault"))
		notificationGroup.POST("/offline/default", jsonRpc.Bind("admin:setOfflineNotificationDefault"))
		loadAlert := notificationGroup.Group("/load")
		{
			loadAlert.GET("/", jsonRpc.Bind("admin:getAllLoadNotifications"))
			loadAlert.POST("/add", jsonRpc.Bind("admin:addLoadNotification"))
			loadAlert.POST("/delete", jsonRpc.Bind("admin:deleteLoadNotification"))
			loadAlert.POST("/edit", jsonRpc.Bind("admin:editLoadNotification"))
			loadAlert.GET("/current", jsonRpc.Bind("admin:listCurrentLoadAlerts"))
			loadAlert.POST("/silence", jsonRpc.Bind("admin:setLoadAlertSilence"))
		}
		trafficReport := notificationGroup.Group("/traffic-report")
		{
			trafficReport.GET("/", jsonRpc.Bind("admin:listTrafficReportNotifications"))
			trafficReport.POST("/edit", jsonRpc.Bind("admin:editTrafficReportNotifications"))
			trafficReport.POST("/enable", jsonRpc.Bind("admin:enableTrafficReportNotifications"))
			trafficReport.POST("/disable", jsonRpc.Bind("admin:disableTrafficReportNotifications"))
			trafficReport.GET("/default", jsonRpc.Bind("admin:getTrafficReportDefault"))
			trafficReport.POST("/default", jsonRpc.Bind("admin:setTrafficReportDefault"))
			trafficReport.POST("/send-daily", jsonRpc.Bind("admin:sendDailyTrafficReport"))
		}
		pingLoss := notificationGroup.Group("/ping-loss")
		{
			pingLoss.GET("/", jsonRpc.Bind("admin:listPingLossNotifications"))
			pingLoss.POST("/add", jsonRpc.Bind("admin:addPingLossNotification"))
			pingLoss.POST("/edit", jsonRpc.Bind("admin:editPingLossNotifications"))
			pingLoss.POST("/batch", jsonRpc.Bind("admin:upsertPingLossNotifications"))
			pingLoss.POST("/delete", jsonRpc.Bind("admin:deletePingLossNotifications"))
			pingLoss.GET("/default", jsonRpc.Bind("admin:getPingLossNotificationDefault"))
			pingLoss.POST("/default", jsonRpc.Bind("admin:setPingLossNotificationDefault"))
			pingLoss.GET("/latency-default", jsonRpc.Bind("admin:getLatencyAnomalyNotificationDefault"))
			pingLoss.POST("/latency-default", jsonRpc.Bind("admin:setLatencyAnomalyNotificationDefault"))
		}
	}

	// ping tasks
	pingTask := g.Group("/ping")
	{
		pingTask.GET("/", jsonRpc.Bind("admin:getAllPingTasks"))
		pingTask.POST("/add", jsonRpc.Bind("admin:addPingTask"))
		pingTask.POST("/delete", jsonRpc.Bind("admin:deletePingTask"))
		pingTask.POST("/edit", jsonRpc.Bind("admin:editPingTask"))
		pingTask.POST("/order", jsonRpc.Bind("admin:orderPingTask"))
	}

	returnRoute := g.Group("/return-route")
	{
		returnRoute.GET("/", jsonRpc.Bind("admin:getReturnRouteOverview"))
		returnRoute.GET("/summary", jsonRpc.Bind("admin:getReturnRouteSummary"))
		returnRoute.POST("/tasks/query", jsonRpc.Bind("admin:queryReturnRouteTasks"))
		returnRoute.POST("/events/query", jsonRpc.Bind("admin:queryReturnRouteEvents"))
		returnRoute.POST("/add", jsonRpc.Bind("admin:addReturnRouteTask"))
		returnRoute.POST("/edit", jsonRpc.Bind("admin:editReturnRouteTask"))
		returnRoute.POST("/edit/batch", jsonRpc.Bind("admin:batchEditReturnRouteTasks"))
		returnRoute.POST("/delete", jsonRpc.Bind("admin:deleteReturnRouteTask"))
		returnRoute.POST("/probe", jsonRpc.Bind("admin:probeReturnRouteNow"))
		returnRoute.GET("/rules", jsonRpc.Bind("admin:getReturnRouteRules"))
		returnRoute.POST("/rules/reload", jsonRpc.Bind("admin:reloadReturnRouteRules"))
		returnRoute.POST("/rules/update", jsonRpc.Bind("admin:updateReturnRouteRules"))
		returnRoute.POST("/rules/refresh", jsonRpc.Bind("admin:refreshReturnRouteBGPRules"))
		returnRoute.GET("/templates", jsonRpc.Bind("admin:listReturnRouteTemplates"))
		returnRoute.POST("/templates/add", jsonRpc.Bind("admin:addReturnRouteTemplate"))
		returnRoute.POST("/templates/edit", jsonRpc.Bind("admin:editReturnRouteTemplate"))
		returnRoute.POST("/templates/edit/batch", jsonRpc.Bind("admin:batchEditReturnRouteTemplates"))
		returnRoute.POST("/templates/delete", jsonRpc.Bind("admin:deleteReturnRouteTemplates"))
		returnRoute.POST("/templates/apply", jsonRpc.Bind("admin:applyReturnRouteTemplates"))
	}
}

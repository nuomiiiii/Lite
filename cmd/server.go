package cmd

import (
	"log"
	"time"

	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/api/admin"
	"github.com/komari-monitor/komari/api/client"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/public"
	"github.com/komari-monitor/komari/utils/geoip"
	"github.com/komari-monitor/komari/ws"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server",
	Long:  `Start the server`,
	Run: func(cmd *cobra.Command, args []string) {
		InitDatabase()

		go DoRecordsWork()

		r := gin.Default()
		cfg, err := config.Get()
		if err != nil {
			log.Fatalln("Failed to get config:", err)
		}

		// 动态 CORS 中间件：每次请求时读取最新配置并设置 CORS 头
		r.Use(func(c *gin.Context) {
			conf, err := config.Get()
			if err == nil && conf.AllowCors {
				c.Header("Access-Control-Allow-Origin", "*")
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Origin, Content-Length, Content-Type, Authorization, Accept, X-CSRF-Token, X-Requested-With, Set-Cookie")
				c.Header("Access-Control-Expose-Headers", "Content-Length, Authorization, Set-Cookie")
				c.Header("Access-Control-Allow-Credentials", "true")
				c.Header("Access-Control-Max-Age", "43200") // 12 hours
				if c.Request.Method == "OPTIONS" {
					c.AbortWithStatus(204)
					return
				}
			}
			c.Next()
		})

		if cfg.GeoIpEnabled {
			geoip.InitGeoIp()
			go func() {
				ticker := time.NewTicker(time.Hour * 24)
				for range ticker.C {
					geoip.UpdateGeoIpDatabase()
				}
			}()
		}

		r.Use(func(c *gin.Context) {
			if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
				c.Header("Cache-Control", "no-store")
			}
			c.Next()
		})

		r.Any("/ping", func(c *gin.Context) {
			c.String(200, "pong")
		})

		r.POST("/api/login", api.Login)
		r.GET("/api/me", api.GetMe)
		r.GET("/api/clients", ws.GetClients)
		r.GET("/api/nodes", api.GetNodesInformation)
		r.GET("/api/public", api.GetPublicSettings)
		r.GET("/api/oauth", api.OAuth)
		r.GET("/api/oauth_callback", api.OAuthCallback)
		r.GET("/api/logout", api.Logout)
		r.GET("/api/version", api.GetVersion)
		r.GET("/api/recent/:uuid", api.GetClientRecentRecords)

		tokenAuthrized := r.Group("/api/clients", api.TokenAuthMiddleware())
		{
			tokenAuthrized.GET("/report", client.WebSocketReport) // websocket
			tokenAuthrized.POST("/uploadBasicInfo", client.UploadBasicInfo)
			tokenAuthrized.POST("/report", client.UploadReport)
			tokenAuthrized.GET("/terminal", client.EstablishConnection)
			tokenAuthrized.POST("/task/result", client.TaskResult)
		}

		adminAuthrized := r.Group("/api/admin", api.AdminAuthMiddleware())
		{
			// tasks
			taskGroup := adminAuthrized.Group("/task")
			{
				taskGroup.GET("/all", admin.GetTasks)
				taskGroup.POST("/exec", admin.Exec)
				taskGroup.GET("/:task_id", admin.GetTaskById)
				taskGroup.GET("/:task_id/result", admin.GetTaskResultsByTaskId)
				taskGroup.GET("/:task_id/result/:uuid", admin.GetSpecificTaskResult)
				taskGroup.GET("/client/:uuid", admin.GetTasksByClientId)
			}
			// settings
			adminAuthrized.GET("/settings", admin.GetSettings)
			adminAuthrized.POST("/settings", admin.EditSettings)
			// clients
			clientGroup := adminAuthrized.Group("/client")
			{
				clientGroup.POST("/add", admin.AddClient)
				clientGroup.GET("/list", admin.ListClients)
				clientGroup.GET("/:uuid", admin.GetClient)
				clientGroup.POST("/:uuid/edit", admin.EditClient)
				clientGroup.POST("/:uuid/remove", admin.RemoveClient)
				clientGroup.GET("/:uuid/token", admin.GetClientToken)
				clientGroup.POST("/order", admin.OrderWeight)
				// client terminal
				clientGroup.GET("/:uuid/terminal", api.RequestTerminal)
			}

			// records
			recordGroup := adminAuthrized.Group("/record")
			{
				recordGroup.POST("/clear", admin.ClearRecord)
			}
			// oauth2
			oauth2Group := adminAuthrized.Group("/oauth2")
			{
				oauth2Group.GET("/bind", admin.BindingExternalAccount)
				oauth2Group.POST("/unbind", admin.UnbindExternalAccount)
			}
			sessionGroup := adminAuthrized.Group("/session")
			{
				sessionGroup.GET("/get", admin.GetSessions)
				sessionGroup.POST("/remove", admin.DeleteSession)
				sessionGroup.POST("/remove/all", admin.DeleteAllSession)
			}
		}

		public.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
			r.NoRoute(handlers...)
		})

		go func() {
			cfg, err := config.Get()
			if err != nil {
				log.Fatalln("Failed to get config:", err)
			}
			public.UpdateIndex(cfg)
		}()

		r.Run(flags.Listen)

	},
}

func init() {
	// 从环境变量获取监听地址
	listenAddr := getEnv("KOMARI_LISTEN", "0.0.0.0:25774")
	ServerCmd.PersistentFlags().StringVarP(&flags.Listen, "listen", "l", listenAddr, "监听地址 [env: KOMARI_LISTEN]")
	RootCmd.AddCommand(ServerCmd)
}

func InitDatabase() {
	// // 打印数据库类型和连接信息
	// if flags.DatabaseType == "mysql" {
	// 	log.Printf("使用 MySQL 数据库连接: %s@%s:%s/%s",
	// 		flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)
	// 	log.Printf("环境变量配置: [KOMARI_DB_TYPE=%s] [KOMARI_DB_HOST=%s] [KOMARI_DB_PORT=%s] [KOMARI_DB_USER=%s] [KOMARI_DB_NAME=%s]",
	// 		os.Getenv("KOMARI_DB_TYPE"), os.Getenv("KOMARI_DB_HOST"), os.Getenv("KOMARI_DB_PORT"),
	// 		os.Getenv("KOMARI_DB_USER"), os.Getenv("KOMARI_DB_NAME"))
	// } else {
	// 	log.Printf("使用 SQLite 数据库文件: %s", flags.DatabaseFile)
	// 	log.Printf("环境变量配置: [KOMARI_DB_TYPE=%s] [KOMARI_DB_FILE=%s]",
	// 		os.Getenv("KOMARI_DB_TYPE"), os.Getenv("KOMARI_DB_FILE"))
	// }
	var count int64 = 0
	if dbcore.GetDBInstance().Model(&models.User{}).Count(&count); count == 0 {
		user, passwd, err := accounts.CreateDefaultAdminAccount()
		if err != nil {
			panic(err)
		}
		log.Println("Default admin account created. Username:", user, ", Password:", passwd)
	}
}

func DoRecordsWork() {
	ticker := time.NewTicker(time.Minute * 30)
	ticker1 := time.NewTicker(60 * time.Second)
	records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
	records.CompactRecord()
	for {
		select {
		case <-ticker.C:
			records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
			records.CompactRecord()
		case <-ticker1.C:
			api.SaveClientReportToDB()
		}
	}

}

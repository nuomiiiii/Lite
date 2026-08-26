package cmd

import (
	"path/filepath"
	"strings"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/spf13/cobra"
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server",
	Long:  `Start the server`,
	Run: func(cmd *cobra.Command, args []string) {
		applyHTTPListenDefault(cmd)
		RunServer()
	},
}

func init() {
	// -l / LITE_LISTEN / KOMARI_LISTEN 原样保留。未指定时：新安装 27777，
	// 从 Komari / Komari Lite 升级则继续用原来的端口，不改到 Lite 默认口。
	ServerCmd.PersistentFlags().StringVarP(&flags.Listen, "listen", "l", "0.0.0.0:27777", "监听地址 [env: LITE_LISTEN]")
	RootCmd.AddCommand(ServerCmd)
}

func applyHTTPListenDefault(cmd *cobra.Command) {
	if cmd != nil && cmd.Flags().Changed("listen") {
		return
	}
	if env := GetEnvFirst("", "LITE_LISTEN", "KOMARI_LISTEN"); env != "" {
		flags.Listen = env
		return
	}
	dataDir := filepath.Join(".", "data")
	if strings.TrimSpace(flags.DatabaseFile) != "" {
		if dir := filepath.Dir(flags.DatabaseFile); dir != "" && dir != "." {
			dataDir = dir
		}
	}
	flags.Listen = dbcore.ResolveDefaultHTTPListen(dataDir)
}

// RunServer 按显式的生命周期阶段启动服务端。
//
// 具体各阶段的职责与顺序见 App（cmd/app.go）。这里只负责串联：
// 任一初始化阶段失败即中止启动，避免在半初始化状态下对外提供服务。
func RunServer() {
	app := NewApp()
	if err := app.Bootstrap(); err != nil {
		_ = app.Shutdown()
		logger.Fatalf("server", "server startup failed at %q: %v", "bootstrap", err)
	}

	installRequired, err := app.InstallRequired()
	if err != nil {
		_ = app.Shutdown()
		logger.Fatalf("server", "server startup failed at %q: %v", "detect-first-run-install", err)
	}
	if installRequired {
		completed, err := app.RunInstallGuide()
		if err != nil {
			_ = app.Shutdown()
			logger.Fatalf("server", "server startup failed at %q: %v", "run-first-run-install", err)
		}
		if !completed {
			return
		}
	}

	required, summary, err := app.LegacyUpgradeRequired()
	if err != nil {
		_ = app.Shutdown()
		logger.Fatalf("server", "server startup failed at %q: %v", "detect-1.2.7-upgrade", err)
	}
	if required {
		completed, err := app.RunLegacyUpgrade(summary)
		if err != nil {
			_ = app.Shutdown()
			logger.Fatalf("server", "server startup failed at %q: %v", "run-1.2.7-upgrade", err)
		}
		if !completed {
			return
		}
	}

	storageUpgradeRequired, storageSummary, err := app.MetricStorageUpgradeRequired()
	if err != nil {
		_ = app.Shutdown()
		logger.Fatalf("server", "server startup failed at %q: %v", "detect-metric-storage-upgrade", err)
	}
	if storageUpgradeRequired {
		completed, err := app.RunMetricStorageUpgrade(storageSummary)
		if err != nil {
			_ = app.Shutdown()
			logger.Fatalf("server", "server startup failed at %q: %v", "run-metric-storage-upgrade", err)
		}
		if !completed {
			return
		}
	}

	// 初始化阶段：任一步失败都不应继续对外服务。
	type stage struct {
		name string
		fn   func() error
	}
	stages := []stage{
		{"init-stores", app.InitStores},
		{"init-providers", app.InitProviders},
		{"build-router", app.BuildRouter},
	}
	for _, s := range stages {
		if err := s.fn(); err != nil {
			// 已登记的资源尽力回收，再退出。
			_ = app.Shutdown()
			logger.Fatalf("server", "server startup failed at %q: %v", s.name, err)
		}
	}

	if err := app.Run(); err != nil {
		logger.Fatalf("server", "server exited with error: %v", err)
	}
}

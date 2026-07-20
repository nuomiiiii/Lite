package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/komari-monitor/komari/cmd"
	"github.com/komari-monitor/komari/utils"
	logutil "github.com/komari-monitor/komari/utils/log"
)

func main() {
	// Version probes are consumed by the updater and must contain no log prefix.
	if len(os.Args) > 1 && os.Args[1] == "version" {
		cmd.Execute()
		return
	}
	if utils.VersionHash == "unknown" {
		logutil.SetupGlobalLogger(slog.LevelDebug)
	} else {
		logutil.SetupGlobalLogger(slog.LevelInfo)
	}

	log.Printf("Komari Monitor %s (%s)", utils.CurrentVersion, utils.VersionHash)

	cmd.Execute()
}

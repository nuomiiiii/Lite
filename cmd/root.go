package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/nuomiiiii/lite/cmd/flags"

	"github.com/spf13/cobra"
)

func GetEnv(key, defaultValue string) string {
	return GetEnvFirst(defaultValue, key)
}

func GetEnvFirst(defaultValue string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultValue
}

var RootCmd = &cobra.Command{
	Use:   "Lite",
	Short: "Lite is a simple server monitoring tool",
	Long: `Lite is a simple server monitoring tool. 
Made by Nomi with love.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.SetArgs([]string{"server"})
		cmd.Execute()
	},
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseType, "db-type", "t", "sqlite", "Database type (sqlite)")
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseFile, "database", "d", "./data/lite.db", "SQLite database file path")
}

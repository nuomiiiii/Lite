package cmd

import (
	"fmt"
	"os"

	"github.com/komari-monitor/komari/cmd/flags"

	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "Komari",
	Short: "Komari is a simple server monitoring tool",
	Long: `Komari is a simple server monitoring tool. 
Made by komari-monitor with love.`,
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
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseFile, "database", "d", "komari.db", "Database file")
	RootCmd.PersistentFlags().StringVarP(&flags.Listen, "listen", "l", "0.0.0.0:5000", "Listen address")
}

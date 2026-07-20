package cmd

import (
	"github.com/komari-monitor/komari/pkg/selfupdate"
	"github.com/spf13/cobra"
)

var SelfUpdateHelperCmd = &cobra.Command{
	Use:    "_self-update-helper CONFIG",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return selfupdate.RunHelper(args[0])
	},
}

func init() {
	RootCmd.AddCommand(SelfUpdateHelperCmd)
}

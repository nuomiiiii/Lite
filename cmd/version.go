package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/komari-monitor/komari/utils"
	"github.com/spf13/cobra"
)

var versionJSON bool

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the Komari version",
	RunE: func(_ *cobra.Command, _ []string) error {
		if versionJSON {
			return json.NewEncoder(RootCmd.OutOrStdout()).Encode(map[string]string{
				"version": utils.CurrentVersion,
				"hash":    utils.VersionHash,
			})
		}
		fmt.Fprintf(RootCmd.OutOrStdout(), "%s (%s)\n", utils.CurrentVersion, utils.VersionHash)
		return nil
	},
}

func init() {
	VersionCmd.Flags().BoolVar(&versionJSON, "json", false, "print machine-readable JSON")
	RootCmd.AddCommand(VersionCmd)
}

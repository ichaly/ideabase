package cmd

import (
	"path/filepath"

	"github.com/ichaly/ideabase/ioc"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

var configFile string

var runCmd = &cobra.Command{
	Use:   "start",
	Short: "Start Service.",

	Run: func(cmd *cobra.Command, args []string) {
		if configFile == "" {
			configFile = filepath.Join("./cfg", "config.yml")
		}
		fx.New(
			ioc.Dependencies,
			fx.Supply(configFile),
		).Run()
	},
}

func init() {
	runCmd.PersistentFlags().StringVarP(
		&configFile, "config", "c", "", "start app with config file",
	)
	rootCmd.AddCommand(runCmd)
}

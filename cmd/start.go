package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ichaly/ideabase/utl"

	"github.com/ichaly/ideabase/ioc"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

const configFlag = "config"

var runCmd = &cobra.Command{
	Use:     "start",
	Aliases: []string{"run", "s", "r"},
	Short:   "Start Service.",
	Run: func(cmd *cobra.Command, args []string) {
		configFile, _ := cmd.Flags().GetString(configFlag)
		if configFile == "" {
			configFile = filepath.Join(utl.Root(), "cfg", "config.yml")
		}
		fx.New(
			ioc.Get(),
			fx.Supply(configFile),
		).Run()
	},
}

func init() {
	runCmd.Flags().StringP(
		configFlag, "c", "", "start app with config file",
	)
}

func Execute() {
	if err := runCmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

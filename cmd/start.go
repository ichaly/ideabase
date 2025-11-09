package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ichaly/ideabase/ioc"
	"github.com/ichaly/ideabase/utl"
	"github.com/spf13/cobra"
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
		ioc.Run(ioc.Supply(configFile))
	},
}

func init() {
	runCmd.Flags().StringP(configFlag, "c", "", "start app with config file")
}

func Execute() {
	if err := runCmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func GetRunCommand() *cobra.Command {
	return runCmd
}

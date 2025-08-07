package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "install",
		Short: "Install CLI 工具",
		Long:  `Install CLI 工具用于安装 IdeaBase 项目的各种命令行工具`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Install CLI 工具")
			fmt.Println("使用 'install --help' 查看更多选项")
		},
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var (
	modules       []string
	bumpType      string
	customVersion string
	dryRun        bool
)

func main() {
	var debug bool
	rootCmd := &cobra.Command{
		Use:   "release",
		Short: "多模块项目发布工具",
		Long: `多模块项目发布工具
使用 Git 标签进行版本管理，基于 go.work 统一管理`,
		RunE: run,
	}

	rootCmd.Flags().StringSliceVarP(&modules, "module", "m", []string{}, "指定要发布的模块 (多个用逗号分隔)")
	rootCmd.Flags().StringVarP(&bumpType, "type", "t", "patch", "版本类型: major, minor, patch (默认: patch)")
	rootCmd.Flags().StringVarP(&customVersion, "version", "v", "", "指定精确版本号")
	rootCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "模拟运行，不实际提交更改")
	rootCmd.Flags().BoolVar(&debug, "debug", false, "调试模式")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// 检查是否在Git仓库中且在正确的分支上
	if err := checkGit(); err != nil {
		return err
	}

	// 1. 首先使用go list -m 获取所有模块
	releaseModules, err := getAllModules()
	if err != nil {
		return fmt.Errorf("获取所有模块失败: %v", err)
	}

	// 2. 如果自定义了发布模块则从所有模块中过滤出待发布模块,否则全部模块为发布模块
	if len(modules) > 0 {
		for name, info := range releaseModules {
			var exits bool
			for _, module := range modules {
				if info.Name == module {
					exits = true
				}
			}
			if !exits {
				delete(releaseModules, name)
			}
		}
	}

	// 3. 遍历所有待发布模块从git获取当前版本号,同时在当前版本号的基础上计算待发布模块的最新版本号
	fmt.Printf("\n===[ 计算最新版本 ]===\n")

	if customVersion != "" {
		// 使用指定的版本号
		version, err := ParseVersion(customVersion)
		if err != nil {
			return fmt.Errorf("无效的版本号格式: %v", err)
		}
		for _, info := range releaseModules {
			info.Version = &version
		}
		fmt.Printf("使用指定版本: %s\n", customVersion)
	} else {
		// 根据类型升级版本
		for name, info := range releaseModules {
			currentVersion, err := getCurrentVersion(name)
			if err != nil {
				return fmt.Errorf("获取模块 %s 的当前版本失败: %v", info, err)
			}
			info.Version = currentVersion.Upgrade(bumpType)
			fmt.Printf("%s: %s -> %s\n", info, currentVersion.String(), info.Version.String())
		}
	}

	fmt.Printf("\n===[ 发布准备 ]===\n")
	fmt.Printf("发布模块: %v\n", releaseModules)

	// 4. 使用计算好的模块版本利用 go mod edit 命令更新待发布模块的依赖版本号
	if err := updateModuleDependencies(releaseModules, dryRun); err != nil {
		return fmt.Errorf("更新模块依赖失败: %v", err)
	}

	// 生成变更日志（使用每个模块的新版本号）
	if err := generateChangelog(releaseModules, dryRun); err != nil {
		return fmt.Errorf("生成变更日志失败: %v", err)
	}

	// 发布每个模块（使用各自的新版本号）
	for name, info := range releaseModules {
		if err := releaseModule(name, info.Version); err != nil {
			return fmt.Errorf("发布模块 %s 失败: %v", name, err)
		}
	}

	// 提交所有变更
	//if err := commitChanges(newVersions[releaseModules[0]], dryRun); err != nil {
	//	return fmt.Errorf("提交变更失败: %v", err)
	//}

	// 推送所有变更
	//if err := pushChanges(dryRun); err != nil {
	//	return fmt.Errorf("推送变更失败: %v", err)
	//}

	return nil
}

// releaseModule 使用指定版本发布单个模块
func releaseModule(module string, version *Version) error {
	// 检查模块目录是否存在
	if _, err := os.Stat(module); os.IsNotExist(err) {
		return fmt.Errorf("模块 '%s' 不存在", module)
	}

	fmt.Printf("\n===[ 发布 %s 模块 ]===\n", module)
	fmt.Printf("版本: %s\n", version.String())

	// 执行发布步骤
	if err := createTag(module, version, dryRun); err != nil {
		return fmt.Errorf("创建标签失败: %v", err)
	}

	return nil
}

// checkGit 确保在正确的分支上
func checkGit() error {
	err := exec.Command("git", "rev-parse", "--git-dir").Run()
	if err != nil {
		return fmt.Errorf("错误：当前目录不是Git仓库根目录")
	}

	output, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return fmt.Errorf("检查当前分支失败: %v", err)
	}

	if currentBranch := strings.TrimSpace(string(output)); currentBranch != Branch {
		return fmt.Errorf("必须在 '%s' 分支上执行发布操作，当前分支是 '%s'", Branch, currentBranch)
	}

	return nil
}

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
	allModules    bool
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
	rootCmd.Flags().BoolVarP(&allModules, "all", "a", false, "发布所有模块")
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

	// 检查是否在Git仓库中
	if err := checkGitRepo(); err != nil {
		return err
	}

	// 验证参数
	if len(modules) == 0 && !allModules {
		return fmt.Errorf("错误: 必须指定至少一个模块 (-m) 或使用全部模块 (-a)")
	}

	// 确保在正确的分支上
	if err := checkBranch(); err != nil {
		return err
	}

	// 如果指定了-a，则获取所有模块
	var releaseModules []string
	if allModules {
		var err error
		releaseModules, err = getAllModules()
		if err != nil {
			return err
		}
	} else {
		releaseModules = modules
	}

	// 1. 获取所有待发布模块的当前版本号
	fmt.Printf("\n===[ 获取当前版本 ]===\n")
	currentVersions := make(map[string]Version)
	for _, module := range releaseModules {
		currentVersion, err := getCurrentVersion(module)
		if err != nil {
			return fmt.Errorf("获取模块 %s 的当前版本失败: %v", module, err)
		}
		currentVersions[module] = currentVersion
		fmt.Printf("%s: %s\n", module, currentVersion.String())
	}

	// 2. 计算所有模块的新版本号
	fmt.Printf("\n===[ 计算新版本 ]===\n")
	newVersions := make(map[string]Version)
	if customVersion != "" {
		// 使用指定的版本号
		version, err := ParseVersion(customVersion)
		if err != nil {
			return fmt.Errorf("无效的版本号格式: %v", err)
		}
		for _, module := range releaseModules {
			newVersions[module] = version
		}
		fmt.Printf("使用指定版本: %s\n", customVersion)
	} else {
		// 根据类型升级版本
		for _, module := range releaseModules {
			newVersion := currentVersions[module].Upgrade(bumpType)
			newVersions[module] = newVersion
			fmt.Printf("%s: %s -> %s\n", module, currentVersions[module].String(), newVersion.String())
		}
	}

	fmt.Printf("\n===[ 发布准备 ]===\n")
	fmt.Printf("发布模块: %v\n", releaseModules)

	// 3. 更新所有模块间的依赖版本
	repoRoot, err := getRepoRoot()
	if err != nil {
		return fmt.Errorf("获取仓库根路径失败: %v", err)
	}

	if err := updateModuleDependencies(newVersions, repoRoot, dryRun); err != nil {
		return fmt.Errorf("更新模块依赖失败: %v", err)
	}

	// 生成变更日志（使用每个模块的新版本号）
	if err := generateChangelog(releaseModules, newVersions, dryRun); err != nil {
		return fmt.Errorf("生成变更日志失败: %v", err)
	}

	// 4. 发布每个模块（使用各自的新版本号）
	for _, module := range releaseModules {
		if err := releaseModule(module, newVersions[module]); err != nil {
			return fmt.Errorf("发布模块 %s 失败: %v", module, err)
		}
	}

	// 提交所有变更（使用第一个模块的新版本号作为整体版本号）
	if err := commitChanges(releaseModules, newVersions[releaseModules[0]], dryRun); err != nil {
		return fmt.Errorf("提交变更失败: %v", err)
	}

	// 推送所有变更
	if err := pushChanges(dryRun); err != nil {
		return fmt.Errorf("推送变更失败: %v", err)
	}

	return nil
}

// releaseModule 使用指定版本发布单个模块
func releaseModule(module string, version Version) error {
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

// checkBranch 确保在正确的分支上
func checkBranch() error {
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("检查当前分支失败: %v", err)
	}

	currentBranch := strings.TrimSpace(string(output))
	if currentBranch != Branch {
		return fmt.Errorf("必须在 '%s' 分支上执行发布操作，当前分支是 '%s'", Branch, currentBranch)
	}

	return nil
}

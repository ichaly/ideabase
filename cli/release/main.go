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
	if err := checkAndSwitchToMainBranch(); err != nil {
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

	// 获取当前版本（使用第一个模块的版本作为参考）
	currentVersion, err := getCurrentVersion(releaseModules[0])
	if err != nil {
		return fmt.Errorf("获取当前版本失败: %v", err)
	}

	// 确定新版本
	var newVersion Version
	if customVersion != "" {
		newVersion, err = ParseVersion(customVersion)
		if err != nil {
			return fmt.Errorf("无效的版本号格式: %v", err)
		}
	} else {
		newVersion = currentVersion.Bump(bumpType)
	}

	fmt.Printf("\n===[ 发布准备 ]===\n")
	fmt.Printf("当前版本: %s\n", currentVersion.String())
	fmt.Printf("新版本: %s\n", newVersion.String())
	fmt.Printf("发布模块: %v\n", releaseModules)

	// 更新所有模块间的依赖版本
	repoURL, err := getRepoURL()
	if err != nil {
		return fmt.Errorf("获取仓库URL失败: %v", err)
	}

	repoRoot := strings.TrimSuffix(repoURL, "/"+strings.Split(repoURL, "/")[len(strings.Split(repoURL, "/"))-1])
	if err := updateModuleDependencies(newVersion, repoRoot, dryRun); err != nil {
		return fmt.Errorf("更新模块依赖失败: %v", err)
	}

	// 生成变更日志
	if err := generateChangelog(releaseModules, newVersion, dryRun); err != nil {
		return fmt.Errorf("生成变更日志失败: %v", err)
	}

	// 发布每个模块
	for _, module := range releaseModules {
		if err := releaseModule(module, newVersion); err != nil {
			return fmt.Errorf("发布模块 %s 失败: %v", module, err)
		}
	}

	// 提交所有变更
	if err := commitChanges(releaseModules, newVersion, dryRun); err != nil {
		return fmt.Errorf("提交变更失败: %v", err)
	}

	// 推送所有变更
	if err := pushChanges(dryRun); err != nil {
		return fmt.Errorf("推送变更失败: %v", err)
	}

	return nil
}

// releaseModule 发布单个模块
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

// checkAndSwitchToMainBranch 确保在正确的分支上
func checkAndSwitchToMainBranch() error {
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("检查当前分支失败: %v", err)
	}

	currentBranch := strings.TrimSpace(string(output))
	if currentBranch != MainBranch {
		fmt.Printf("⚠️  当前分支不是 %s，切换到 %s 分支\n", MainBranch, MainBranch)

		if !dryRun {
			// 切换分支
			cmd = exec.Command("git", "checkout", MainBranch)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("切换分支失败: %v", err)
			}

			// 拉取最新代码
			cmd = exec.Command("git", "pull", "origin", MainBranch)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("拉取最新代码失败: %v", err)
			}
		}
	}

	return nil
}

// getRepoURL 获取仓库URL
func getRepoURL() (string, error) {
	cmd := exec.Command("go", "list", "-m")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("无法获取仓库URL")
	}

	return strings.TrimSpace(lines[0]), nil
}

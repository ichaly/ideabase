package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	MainBranch    = "main"
	ChangeLogFile = "CHANGELOG.md"
)

// Version 表示语义化版本
type Version struct {
	Major int
	Minor int
	Patch int
}

// String 返回版本字符串
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// ParseVersion 从字符串解析版本
func ParseVersion(version string) (Version, error) {
	re := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	matches := re.FindStringSubmatch(version)
	if len(matches) != 4 {
		return Version{}, fmt.Errorf("无效的版本号格式: %s", version)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
	}, nil
}

// Bump 根据类型升级版本
func (v Version) Bump(bumpType string) Version {
	switch bumpType {
	case "major":
		return Version{Major: v.Major + 1, Minor: 0, Patch: 0}
	case "minor":
		return Version{Major: v.Major, Minor: v.Minor + 1, Patch: 0}
	case "patch":
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	default:
		return v
	}
}

// checkGitRepo 检查是否在Git仓库中
func checkGitRepo() error {
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return fmt.Errorf("错误：当前目录不是Git仓库根目录")
	}
	return nil
}

// getAllModules 获取所有本地模块
func getAllModules() ([]string, error) {
	cmd := exec.Command("go", "list", "-m")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("无法获取模块列表: %v", err)
	}

	modules := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(modules) == 0 || (len(modules) == 1 && modules[0] == "") {
		return nil, fmt.Errorf("未找到任何模块")
	}

	// 获取第一个模块作为基准来确定仓库根路径
	baseModule := modules[0]
	repoRoot := regexp.MustCompile(`/[^/]*$`).ReplaceAllString(baseModule, "")

	var result []string
	for _, modulePath := range modules {
		if strings.HasPrefix(modulePath, repoRoot+"/") {
			relativePath := strings.TrimPrefix(modulePath, repoRoot+"/")
			result = append(result, relativePath)
		} else if modulePath == repoRoot {
			result = append(result, ".") // 根模块
		}
	}

	return result, nil
}

// getCurrentVersion 获取模块的当前版本（仅从Git标签）
func getCurrentVersion(module string) (Version, error) {
	// 尝试从Git标签获取
	tagPattern := fmt.Sprintf("*%s/v*", module)
	cmd := exec.Command("git", "describe", "--tags", "--match", tagPattern, "--abbrev=0")
	output, err := cmd.Output()

	if err == nil && len(output) > 0 {
		tag := strings.TrimSpace(string(output))
		// 从标签中提取版本号
		versionStr := strings.TrimPrefix(tag, tag[:strings.LastIndex(tag, "/v")+2])
		return ParseVersion(versionStr)
	}

	// 如果没有标签，返回0.0.0
	return Version{Major: 0, Minor: 0, Patch: 0}, nil
}

// createTag 创建标签
func createTag(module string, version Version, dryRun bool) error {
	tag := fmt.Sprintf("%s/v%s", module, version.String())

	if dryRun {
		fmt.Printf("[模拟] git tag -a %s -m \"Release %s\"\n", tag, tag)
		return nil
	}

	cmd := exec.Command("git", "tag", "-a", tag, "-m", fmt.Sprintf("Release %s", tag))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("创建标签失败: %v", err)
	}

	fmt.Printf("🏷️  创建标签: %s\n", tag)
	return nil
}

// updateModuleDependencies 更新所有模块的依赖版本
func updateModuleDependencies(version Version, repoRoot string, dryRun bool) error {
	// 获取所有模块
	modules, err := getAllModules()
	if err != nil {
		return err
	}

	fmt.Printf("更新所有模块间的依赖版本到 %s\n", version.String())

	// 遍历每个模块目录
	for _, module := range modules {
		goModPath := module + "/go.mod"
		if module == "." {
			goModPath = "go.mod"
		}

		if _, err := os.Stat(goModPath); os.IsNotExist(err) {
			fmt.Printf("警告: %s 模块中未找到 go.mod 文件\n", module)
			continue
		}

		if dryRun {
			fmt.Printf("[模拟] 更新 %s 模块的依赖\n", module)

			// 显示将要更新的依赖
			for _, depModule := range modules {
				if module != depModule {
					fmt.Printf("[模拟]   更新依赖: %s/%s v%s\n", repoRoot, depModule, version.String())
				}
			}
		} else {
			// 实际更新依赖
			for _, depModule := range modules {
				if module != depModule {
					// 使用 go mod edit 更新依赖版本
					cmd := exec.Command("go", "mod", "edit", "-require", fmt.Sprintf("%s/%s@v%s", repoRoot, depModule, version.String()))
					cmd.Dir = module
					if module == "." {
						cmd.Dir = "."
					}
					if err := cmd.Run(); err != nil {
						fmt.Printf("警告: 更新 %s 模块的 %s 依赖失败: %v\n", module, depModule, err)
						continue
					}

					// 运行 go mod tidy
					cmd = exec.Command("go", "mod", "tidy")
					cmd.Dir = module
					if module == "." {
						cmd.Dir = "."
					}
					if err := cmd.Run(); err != nil {
						fmt.Printf("警告: 运行 go mod tidy 失败: %v\n", err)
					}
				}
			}

			fmt.Printf("🔗 已更新 %s 模块的依赖\n", module)
		}
	}

	return nil
}

// generateChangelog 生成变更日志
func generateChangelog(modules []string, version Version, dryRun bool) error {
	changes := ""

	// 为每个模块生成变更记录
	for _, module := range modules {
		var rangeStr string

		// 获取历史标签范围
		cmd := exec.Command("git", "tag", "--list", fmt.Sprintf("*%s/v*", module))
		output, err := cmd.Output()
		if err != nil || len(output) == 0 {
			rangeStr = "HEAD"
		} else {
			cmd = exec.Command("git", "describe", "--tags", "--match", fmt.Sprintf("*%s/v*", module), "--abbrev=0")
			output, err := cmd.Output()
			if err != nil {
				rangeStr = "HEAD"
			} else {
				prevTag := strings.TrimSpace(string(output))
				rangeStr = fmt.Sprintf("%s..HEAD", prevTag)
			}
		}

		// 获取模块提交记录
		cmd = exec.Command("git", "log", rangeStr, "--pretty=format:- %s", "--", module)
		output, err = cmd.Output()
		moduleChanges := ""
		if err == nil && len(output) > 0 {
			moduleChanges = strings.TrimSpace(string(output))
		}

		if moduleChanges != "" {
			changes += fmt.Sprintf("\n### %s\n%s", module, moduleChanges)
		}
	}

	// 生成Markdown内容
	changelogEntry := fmt.Sprintf("\n## v%s (%s)%s", version.String(), getCurrentDate(), changes)
	if changes == "" {
		changelogEntry += "\n- 无变更记录"
	}

	if !dryRun {
		f, err := os.OpenFile(ChangeLogFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("无法打开变更日志文件: %v", err)
		}
		defer f.Close()

		if _, err := f.WriteString(changelogEntry); err != nil {
			return fmt.Errorf("写入变更日志文件失败: %v", err)
		}

		// 添加到git
		cmd := exec.Command("git", "add", ChangeLogFile)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("添加变更日志到git失败: %v", err)
		}

		fmt.Println("📝 更新变更日志")
	} else {
		fmt.Printf("[模拟] 更新变更日志 %s\n", ChangeLogFile)
	}

	return nil
}

// commitChanges 提交版本变更
func commitChanges(modules []string, version Version, dryRun bool) error {
	message := fmt.Sprintf("chore(release): release v%s", version.String())

	if dryRun {
		fmt.Printf("[模拟] git commit -m \"%s\"\n", message)
		return nil
	}

	// 检查是否有需要提交的更改
	cmd := exec.Command("git", "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("检查git状态失败: %v", err)
	}

	if len(output) > 0 {
		cmd = exec.Command("git", "commit", "-m", message)
	} else {
		// 如果没有更改，创建空提交
		cmd = exec.Command("git", "commit", "--allow-empty", "-m", message)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit 失败: %v", err)
	}

	return nil
}

// pushChanges 推送变更
func pushChanges(dryRun bool) error {
	if dryRun {
		fmt.Printf("[模拟] git push origin %s\n", MainBranch)
		fmt.Printf("[模拟] git push origin --tags\n")
		return nil
	}

	// 推送分支
	cmd := exec.Command("git", "push", "origin", MainBranch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("推送分支失败: %v", err)
	}

	// 推送标签
	cmd = exec.Command("git", "push", "origin", "--tags")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("推送标签失败: %v", err)
	}

	fmt.Println("🚀 已推送变更到仓库")
	return nil
}

// getCurrentDate 获取当前日期
func getCurrentDate() string {
	cmd := exec.Command("date", "+%Y-%m-%d")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

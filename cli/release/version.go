package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samber/lo"
	"golang.org/x/mod/modfile"
)

const (
	Branch    = "main"
	ChangeLog = "CHANGELOG.md"
)

// ModuleInfo 表示模块信息
type ModuleInfo struct {
	Name    string   // 模块名（去除公共前缀部分）
	Path    string   // 模块完整路径
	Root    string   // 根路径
	Version *Version // 版本信息
}

// Version 表示语义化版本
type Version struct {
	Major int
	Minor int
	Patch int
}

// String 返回版本字符串
func (my *Version) String() string {
	return fmt.Sprintf("%d.%d.%d", my.Major, my.Minor, my.Patch)
}

// Upgrade 根据类型升级版本
func (my *Version) Upgrade(upgradeType string) *Version {
	switch upgradeType {
	case "major":
		return &Version{Major: my.Major + 1, Minor: 0, Patch: 0}
	case "minor":
		return &Version{Major: my.Major, Minor: my.Minor + 1, Patch: 0}
	case "patch":
		return &Version{Major: my.Major, Minor: my.Minor, Patch: my.Patch + 1}
	default:
		return my
	}
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

// getCurrentDate 获取当前日期
func getCurrentDate() string {
	// 使用Go标准库替代外部命令调用，提高性能和可移植性
	return time.Now().Format("2006-01-02")
}

// findCommonPrefix 查找字符串数组的公共前缀
func findCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}

	// 基准字符串
	base := strs[0]
	index := len(base) // 默认结尾位置

	// 比较其他字符串
	for _, text := range strs[1:] {
		i := 0
		for i < len(base) && i < len(text) && base[i] == text[i] {
			i++
		}

		// 确保字符边界
		for i > 0 {
			if utf8.RuneStart(base[i-1]) {
				break
			}
			i--
		}

		if i < index {
			index = i
		}
	}

	return base[:index]
}

// getAllModules 获取模块信息
func getAllModules() (map[string]*ModuleInfo, error) {
	modules := make(map[string]*ModuleInfo)

	// 执行go list -m获取模块列表
	output, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		return modules, fmt.Errorf("无法获取模块列表: %v", err)
	}

	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "") {
		return modules, fmt.Errorf("未找到任何模块")
	}

	// 查找公共前缀
	prefix := findCommonPrefix(paths)
	if len(prefix) == 0 {
		return modules, fmt.Errorf("无法确定公共前缀")
	}

	// 构造ModuleInfo字典
	for _, module := range paths {
		// 从完整路径中提取模块名并移除可能的前导斜杠
		name := strings.TrimPrefix(strings.TrimPrefix(module, prefix), "/")
		modules[module] = &ModuleInfo{Name: name, Path: module, Root: prefix}
	}

	return modules, nil
}

// getCurrentVersion 获取模块的当前版本（仅从Git标签）
func getCurrentVersion(module string) (Version, error) {
	// 尝试从Git标签获取
	tagPattern := fmt.Sprintf("*%s/v*", module)
	cmd := exec.Command("git", "describe", "--tags", "--match", tagPattern, "--abbrev=0")

	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		tag := strings.TrimSpace(string(output))
		// 从标签中提取版本号
		versionStr := strings.TrimPrefix(tag, tag[:strings.LastIndex(tag, "/v")+2])
		return ParseVersion(versionStr)
	}

	// 如果没有标签，返回0.0.0
	return Version{Major: 0, Minor: 0, Patch: 0}, nil
}

// createTag 创建标签
func createTag(module string, version *Version, dryRun bool) error {
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

// updateModuleDependencies 使用指定版本更新所有模块的依赖版本
func updateModuleDependencies(modules map[string]*ModuleInfo, dryRun bool) error {
	fmt.Printf("更新所有模块间的依赖版本:\n")
	for name, info := range modules {
		fmt.Printf("  %s: %s\n", name, info.Version.String())
	}

	// 遍历每个模块目录
	for name, info := range modules {
		// 使用模块名作为目录路径（相对于项目根目录）
		basePath := filepath.Join("..", "..", info.Name)

		if _, err := os.Stat(filepath.Join(basePath, "go.mod")); os.IsNotExist(err) {
			fmt.Printf("警告: %s 模块中未找到 go.mod 文件\n", name)
			continue
		}

		if dryRun {
			fmt.Printf("[模拟] 更新 %s 模块的依赖\n", name)

			// 显示将要更新的依赖
			for depName, depInfo := range modules {
				if name != depName {
					fmt.Printf("[模拟]   更新依赖: %s v%s\n", depName, depInfo.Version.String())
				}
			}
		} else {
			// 实际更新依赖
			for depName, depInfo := range modules {
				if name != depName {
					// 使用 go mod edit 更新依赖版本
					cmd := exec.Command("go", "mod", "edit", "-require", fmt.Sprintf("%s@v%s", depName, depInfo.Version.String()))
					cmd.Dir = basePath
					if err := cmd.Run(); err != nil {
						fmt.Printf("警告: 更新 %s 模块的 %s 依赖失败: %v\n", name, depName, err)
						continue
					}

					// 运行 go mod tidy
					cmd = exec.Command("go", "mod", "tidy")
					cmd.Dir = basePath
					if err := cmd.Run(); err != nil {
						fmt.Printf("警告: 运行 go mod tidy 失败: %v\n", err)
					}
				}
			}

			fmt.Printf("🔗 已更新 %s 模块的依赖\n", name)
		}
	}

	return nil
}

// generateChangelog 生成变更日志（使用每个模块的版本号）
func generateChangelog(modules map[string]*ModuleInfo, dryRun bool) error {
	changes := ""

	// 为每个模块生成变更记录
	for _, module := range modules {
		var rangeStr string

		// 构建模块的标签模式
		tagPattern := fmt.Sprintf("%s/v*", module.Name)

		// 获取最新标签
		cmd := exec.Command("git", "describe", "--tags", "--match", tagPattern, "--abbrev=0")
		output, err := cmd.Output()

		if err == nil && len(output) > 0 {
			prevTag := strings.TrimSpace(string(output))
			// 确保标签确实匹配模块
			if strings.HasPrefix(prevTag, module.Name+"/v") {
				rangeStr = fmt.Sprintf("%s..HEAD", prevTag)
			} else {
				rangeStr = "HEAD"
			}
		} else {
			rangeStr = "HEAD"
		}

		// 获取模块提交记录
		cmd = exec.Command("git", "log", rangeStr, "--pretty=format:- %s", "--", module.Name)
		output, err = cmd.Output()
		moduleChanges := ""
		if err == nil && len(output) > 0 {
			moduleChanges = strings.TrimSpace(string(output))
		}

		if moduleChanges != "" {
			changes += fmt.Sprintf("\n## %s v%s (%s)\n%s", module.Name, module.Version.String(), getCurrentDate(), moduleChanges)
		} else {
			changes += fmt.Sprintf("\n## %s v%s (%s)\n- 无变更记录", module.Name, module.Version.String(), getCurrentDate())
		}
	}

	if !dryRun {
		f, err := os.OpenFile(ChangeLog, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("无法打开变更日志文件: %v", err)
		}
		defer f.Close()

		if _, err := f.WriteString(changes); err != nil {
			return fmt.Errorf("写入变更日志文件失败: %v", err)
		}

		// 添加到git
		cmd := exec.Command("git", "add", ChangeLog)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("添加变更日志到git失败: %v", err)
		}

		fmt.Println("📝 更新变更日志")
	} else {
		fmt.Printf("[模拟] 更新变更日志 %s\n", ChangeLog)
	}

	return nil
}

// commitChanges 提交版本变更
func commitChanges(version Version, dryRun bool) error {
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
		fmt.Printf("[模拟] git push origin %s\n", Branch)
		fmt.Printf("[模拟] git push origin --tags\n")
		return nil
	}

	// 推送分支
	cmd := exec.Command("git", "push", "origin", Branch)
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

// hasDependency 检查模块是否直接依赖指定的模块
func hasDependency(path, module string) bool {
	if content, err := os.ReadFile(filepath.Join(path, "go.mod")); err == nil {
		if file, err := modfile.Parse("go.mod", content, nil); err == nil {
			return lo.ContainsBy(file.Require, func(req *modfile.Require) bool {
				return req.Mod.Path == module && !req.Indirect
			})
		}
	}
	return false
}

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
func ParseVersion(version string) (*Version, error) {
	re := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	matches := re.FindStringSubmatch(version)
	if len(matches) != 4 {
		return nil, fmt.Errorf("无效的版本号格式: %s", version)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])

	return &Version{
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

// getProjectRoot 获取项目根目录
func getProjectRoot() string {
	currentDir, _ := os.Getwd()
	projectRoot := currentDir
	for {
		if _, err := os.Stat(filepath.Join(projectRoot, "go.work")); err == nil {
			break
		}
		parent := filepath.Dir(projectRoot)
		if parent == projectRoot {
			projectRoot = currentDir
			break
		}
		projectRoot = parent
	}
	return projectRoot
}

// getModulePath 获取模块的绝对路径
func getModulePath(projectRoot, moduleName string) string {
	return filepath.Join(projectRoot, moduleName)
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
func getCurrentVersion(module string) (*Version, error) {
	// 尝试从Git标签获取
	cmd := exec.Command("git", "describe", "--tags", "--match", fmt.Sprintf("%s/v*", module), "--abbrev=0")
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		if tag := strings.TrimSpace(string(output)); strings.HasPrefix(tag, module+"/v") {
			return ParseVersion(strings.TrimPrefix(tag, module+"/v"))
		}
	}
	// 如果没有标签，返回0.0.0
	return &Version{Major: 0, Minor: 0, Patch: 0}, nil
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
func updateDependencies(modules map[string]*ModuleInfo, projectRoot string, dryRun bool) error {
	fmt.Printf("更新所有模块间的依赖版本:\n")

	// 计算依赖关系
	dependencies := make(map[string][]string)
	for name, info := range modules {
		basePath := getModulePath(projectRoot, info.Name)
		content, err := os.ReadFile(filepath.Join(basePath, "go.mod"))
		if err != nil {
			fmt.Printf("警告: %s 模块中未找到 go.mod 文件\n", name)
			continue
		}

		if file, err := modfile.Parse("go.mod", content, nil); err == nil {
			if deps := lo.FilterMap(file.Require, func(req *modfile.Require, _ int) (string, bool) {
				if req.Indirect {
					return "", false
				}
				return req.Mod.Path, lo.ContainsBy(lo.Values(modules), func(m *ModuleInfo) bool { return m.Path == req.Mod.Path })
			}); len(deps) > 0 {
				dependencies[name] = deps
			}
		}
	}

	// 更新依赖
	for name, deps := range dependencies {
		fmt.Printf("%s更新 %s 模块的依赖\n", lo.Ternary(dryRun, "[模拟] ", ""), name)

		basePath := getModulePath(projectRoot, modules[name].Name)
		for _, depPath := range deps {
			depInfo := modules[depPath]
			if dryRun {
				fmt.Printf("[模拟]   更新依赖: %s v%s\n", depInfo.Name, depInfo.Version.String())
				continue
			}
			cmd := exec.Command("go", "mod", "edit", "-require", fmt.Sprintf("%s@v%s", depPath, depInfo.Version.String()))
			cmd.Dir = basePath
			if err := cmd.Run(); err != nil {
				fmt.Printf("警告: 更新 %s 模块的 %s 依赖失败: %v\n", name, depInfo.Name, err)
			}
		}

		// 将修改后的go.mod文件添加到git暂存区
		if !dryRun {
			goModPath := filepath.Join(basePath, "go.mod")
			if err := exec.Command("git", "add", goModPath).Run(); err != nil {
				fmt.Printf("警告: 添加 %s 模块的 go.mod 到git失败: %v\n", name, err)
			}
		}

		fmt.Printf("%s已更新 %s 模块的依赖\n", lo.Ternary(dryRun, "[模拟] ", "🔗 "), name)
	}

	return nil
}

// generateChangelog 生成变更日志（使用每个模块的版本号）
func generateChangelog(modules map[string]*ModuleInfo, dryRun bool) error {
	var changes strings.Builder

	for _, module := range modules {
		// 获取Git标签范围和提交记录
		moduleChanges, rangeStr := "", "HEAD"
		cmd := exec.Command("git", "describe", "--tags", "--match", fmt.Sprintf("%s/v*", module.Name), "--abbrev=0")
		if output, err := cmd.Output(); err == nil && len(output) > 0 {
			if prevTag := strings.TrimSpace(string(output)); strings.HasPrefix(prevTag, module.Name+"/v") {
				rangeStr = fmt.Sprintf("%s..HEAD", prevTag)
			}
		}

		cmd = exec.Command("git", "log", rangeStr, "--pretty=format:- %s", "--", module.Name)
		if output, err := cmd.Output(); err == nil && len(output) > 0 {
			moduleChanges = strings.TrimSpace(string(output))
		}

		changes.WriteString(fmt.Sprintf("\n## %s v%s (%s)\n%s",
			module.Name, module.Version.String(), getCurrentDate(),
			lo.Ternary(moduleChanges != "", moduleChanges, "- 无变更记录")))
	}

	// 写入文件并处理Git操作
	if f, err := os.OpenFile(ChangeLog, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644); err != nil {
		return fmt.Errorf("无法打开变更日志文件: %v", err)
	} else {
		defer f.Close()
		if _, err := f.WriteString(changes.String()); err != nil {
			return fmt.Errorf("写入变更日志文件失败: %v", err)
		}
	}

	fmt.Printf("%s\n", lo.Ternary(dryRun, "[模拟] 已生成变更日志 "+ChangeLog+"（未提交到Git）", "📝 更新变更日志"))
	if !dryRun {
		if err := exec.Command("git", "add", ChangeLog).Run(); err != nil {
			return fmt.Errorf("添加变更日志到git失败: %v", err)
		}
	}

	return nil
}

// commitChanges 提交版本变更
func commitChanges(dryRun bool) error {
	message := "chore(release): 发布新版本"

	fmt.Printf("%sgit commit -m \"%s\"\n", lo.Ternary(dryRun, "[模拟] ", ""), message)
	if dryRun {
		return nil
	}

	// 直接提交，因为此时应该已经有文件变更（CHANGELOG.md 和 go.mod）
	if err := exec.Command("git", "commit", "-m", message).Run(); err != nil {
		return fmt.Errorf("git commit 失败: %v", err)
	}

	fmt.Printf("💾 已提交变更\n")
	return nil
}

// pushChanges 推送变更
func pushChanges(dryRun bool) error {
	prefix := lo.Ternary(dryRun, "[模拟] ", "")
	fmt.Printf("%sgit push origin %s --follow-tags\n", prefix, Branch)
	if dryRun {
		return nil
	}

	// 同时推送分支和标签
	cmd := exec.Command("git", "push", "origin", Branch, "--follow-tags")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("推送变更失败: %v", err)
	}

	fmt.Printf("🚀 已推送变更到仓库\n")
	return nil
}

// refreshWorkspaceDependencies 刷新工作区依赖，使用direct模式获取最新版本
func refreshWorkspaceDependencies(dryRun bool) error {
	fmt.Printf("\n===[ 刷新工作区依赖 ]===\n")
	
	if dryRun {
		fmt.Printf("[模拟] GOPROXY=direct GOSUMDB=off go get -u ./...\n")
		fmt.Printf("[模拟] go mod tidy\n")
		return nil
	}

	projectRoot := getProjectRoot()
	
	// 使用direct模式更新所有依赖
	fmt.Printf("🔄 使用direct模式更新依赖...\n")
	cmd := exec.Command("go", "get", "-u", "./...")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "GOPROXY=direct", "GOSUMDB=off")
	
	if err := cmd.Run(); err != nil {
		fmt.Printf("警告: 依赖更新失败: %v\n", err)
		fmt.Printf("💡 建议手动执行: GOPROXY=direct GOSUMDB=off go get -u ./...\n")
		return nil // 不中断发布流程，只是警告
	}

	// 清理依赖
	fmt.Printf("🧹 清理依赖...\n")
	cmd = exec.Command("go", "mod", "tidy")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		fmt.Printf("警告: go mod tidy失败: %v\n", err)
		return nil // 不中断发布流程，只是警告
	}

	fmt.Printf("✅ 工作区依赖已刷新\n")
	fmt.Printf("💡 如果遇到goproxy缓存问题，请使用: GOPROXY=direct GOSUMDB=off go get -u ./...\n")
	return nil
}

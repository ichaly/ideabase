#!/bin/bash

#=======================================
# 多模块项目发布脚本
# 使用 Git 标签进行版本管理，基于 go.work 统一管理
#=======================================

# 配置
MAIN_BRANCH="main"
MODULE_PREFIX=""
DRY_RUN=0
CHANGELOG_FILE="CHANGELOG.md"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # 清除颜色

# 帮助信息
show_help() {
    echo "使用方法: $0 [选项]"
    echo
    echo "选项:"
    echo "  -m, --module <模块>   指定要发布的模块 (多个用逗号分隔)"
    echo "  -a, --all             发布所有模块"
    echo "  -t, --type <类型>     版本类型: major, minor, patch (默认: patch)"
    echo "  -v, --version <版本>  指定精确版本号"
    echo "  -d, --dry-run         模拟运行，不实际提交更改"
    echo "  -h, --help            显示帮助信息"
    echo
    echo "示例:"
    echo "  $0 -m core,app -t minor   # 升级核心和应用模块的次版本"
    echo "  $0 -m cli -v 1.2.3        # 将CLI模块升级到指定版本"
    echo "  $0 -a -t minor            # 将所有模块升级到次版本"
    exit 0
}

# 检查是否在Git仓库中
check_git_repo() {
    if [ ! -d .git ]; then
        echo "${RED}错误：当前目录不是Git仓库根目录${NC}"
        exit 1
    fi
}

# 获取所有本地模块
get_all_modules() {
    # 使用 go list -m 获取所有模块
    local modules
    modules=$(go list -m 2>/dev/null)
    
    if [ -n "$modules" ]; then
        # 获取第一个模块作为基准来确定仓库根路径
        local base_module
        base_module=$(echo "$modules" | head -n 1)
        
        # 提取仓库根路径（去掉最后一个路径段）
        local repo_root
        repo_root=$(echo "$base_module" | sed 's|/[^/]*$||')
        
        # 过滤并转换为相对路径
        echo "$modules" | while read -r module_path; do
            if [[ $module_path == "$repo_root"/* ]]; then
                echo "$module_path" | sed "s|^$repo_root/||"
            elif [[ $module_path == "$repo_root" ]]; then
                echo "."  # 根模块
            fi
        done
    else
        echo "${RED}错误: 无法获取模块列表${NC}" >&2
        exit 1
    fi
}

# 获取模块的当前版本（仅从Git标签）
get_current_version() {
    local module="$1"
    
    # 模块路径已经为正确格式，无需转换
    local module_path="$module"
    
    # 尝试从Git标签获取
    # shellcheck disable=SC2155
    local latest_tag=$(git describe --tags --match "${MODULE_PREFIX}${module_path}/v*" --abbrev=0 2>/dev/null)

    if [ -n "$latest_tag" ]; then
        # 从标签中提取版本号
        # shellcheck disable=SC2001
        echo "$latest_tag" | sed "s#${MODULE_PREFIX}${module_path}/v##"
        return
    fi

    # 如果没有标签，返回0.0.0
    echo "0.0.0"
}

# 计算新版本
calculate_new_version() {
    local current="$1"
    local bump_type="$2"
    local version_parts

    IFS='.' read -ra version_parts <<< "$current"
    major="${version_parts[0]}"
    minor="${version_parts[1]}"
    patch="${version_parts[2]}"

    case "$bump_type" in
        major)
            new_version="$((major + 1)).0.0"
            ;;
        minor)
            new_version="$major.$((minor + 1)).0"
            ;;
        patch)
            new_version="$major.$minor.$((patch + 1))"
            ;;
        *)
            echo "${RED}错误: 无效的版本类型: $bump_type${NC}"
            exit 1
            ;;
    esac

    echo "$new_version"
}

# 验证版本号格式
validate_version() {
    local version="$1"
    if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo "${RED}错误: 无效的版本号格式: $version${NC}"
        echo "版本号必须符合语义化版本规范 (例如：1.2.3)"
        exit 1
    fi
}

# 更新所有模块的依赖版本
update_module_dependencies() {
    local version="$1"
    local repo_prefix="$2"

    # 获取所有模块
    local modules=$(get_all_modules)

    echo "${GREEN}更新所有模块间的依赖版本到 $version${NC}"

    # 遍历每个模块目录
    for module in $modules; do
        if [ ! -f "$module/go.mod" ]; then
            echo "${YELLOW}警告: $module 模块中未找到 go.mod 文件${NC}"
            continue
        fi

        if [ "$DRY_RUN" -eq 1 ]; then
            echo "${YELLOW}[模拟] 更新 $module 模块的依赖${NC}"

            # 显示将要更新的依赖
            for dep_module in $modules; do
                if [ "$module" != "$dep_module" ]; then
                    echo "${YELLOW}[模拟]   更新依赖: $repo_prefix/$dep_module v$version${NC}"
                fi
            done
        else
            # 实际更新依赖
            for dep_module in $modules; do
                if [ "$module" != "$dep_module" ]; then
                    # 使用 go mod edit 更新依赖版本
                    (cd "$module" && go mod edit -require="$repo_prefix/$dep_module@$version" && go mod tidy)
                fi
            done

            echo "🔗 ${GREEN}已更新 $module 模块的依赖${NC}"
        fi
    done
}

# 提交版本变更
commit_changes() {
    local modules="$1"
    local version="$2"
    local message="chore(release): release v$version"

    if [ "$DRY_RUN" -eq 1 ]; then
        echo "${YELLOW}[模拟] git commit -m \"$message\"${NC}"
    else
        # 检查是否有需要提交的更改
        if [ -n "$(git status --porcelain)" ]; then
            git commit -m "$message"
        else
            # 如果没有更改，创建空提交
            git commit --allow-empty -m "$message"
        fi
    fi
}

# 创建标签
create_tag() {
    local module="$1"
    local version="$2"
    local tag="${MODULE_PREFIX}${module}/v${version}"

    if [ "$DRY_RUN" -eq 1 ]; then
        echo "${YELLOW}[模拟] git tag -a $tag -m \"Release $tag\"${NC}"
    else
        git tag -a "$tag" -m "Release $tag"
        echo "🏷️  ${GREEN}创建标签: $tag${NC}"
    fi
}

# 推送变更
push_changes() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "${YELLOW}[模拟] git push origin $MAIN_BRANCH${NC}"
        echo "${YELLOW}[模拟] git push origin --tags${NC}"
    else
        git push origin "$MAIN_BRANCH"
        git push origin --tags
        echo "🚀 ${GREEN}已推送变更到仓库${NC}"
    fi
}

# 生成变更日志
generate_changelog() {
    local modules="$1"
    local version="$2"
    local changes=""

    # 为每个模块生成变更记录
    for module in $modules; do
        local tag="${MODULE_PREFIX}${module}/v${version}"
        local prev_tag range module_changes

        # 获取历史标签范围
        if [ -z "$(git tag --list "${MODULE_PREFIX}${module}/v*")" ]; then
            range="HEAD"
        else
            prev_tag=$(git describe --tags --match "${MODULE_PREFIX}${module}/v*" --abbrev=0 2>/dev/null)
            range="${prev_tag}..HEAD"
        fi

        # 获取模块提交记录
        module_changes=$(git log "$range" --pretty=format:"- %s" -- "$module" 2>/dev/null)
        
        if [ -n "$module_changes" ]; then
            changes="$changes
### $module
$module_changes"
        fi
    done

    # 生成Markdown内容（始终执行）
    {
        echo "\n## v$version ($(date +%Y-%m-%d))"
        if [ -n "$changes" ]; then
            echo "$changes"
        else
            echo "- 无变更记录"
        fi
    } >> "$CHANGELOG_FILE"

    # 处理Git操作（仅在非Dry Run时执行）
    if [ "$DRY_RUN" -eq 0 ]; then
        git add "$CHANGELOG_FILE"
        echo "📝 ${GREEN}更新变更日志${NC}"
    else
        echo "${YELLOW}[模拟] 更新变更日志 $CHANGELOG_FILE${NC}"
    fi
}

# 解析命令行参数
parse_args() {
    modules=""
    all_modules=0
    bump_type="patch"
    custom_version=""

    while [ $# -gt 0 ]; do
        case "$1" in
            -m|--module)
                modules="${2//,/ }"  # 转换逗号为空格
                shift 2
                ;;
            -a|--all)
                all_modules=1
                shift
                ;;
            -t|--type)
                bump_type="$2"
                shift 2
                ;;
            -v|--version)
                custom_version="$2"
                shift 2
                ;;
            -d|--dry-run)
                DRY_RUN=1
                shift
                ;;
            -h|--help)
                show_help
                ;;
            *)
                echo "${RED}错误: 未知选项: $1${NC}"
                show_help
                ;;
        esac
    done

    # 验证参数
    if [ -z "$modules" ] && [ "$all_modules" -eq 0 ]; then
        echo "${RED}错误: 必须指定至少一个模块 (-m) 或使用全部模块 (-a)${NC}"
        show_help
    fi

    if [ -n "$custom_version" ] && [ -n "$bump_type" ] && [ "$bump_type" != "patch" ]; then
        echo "${YELLOW}警告: 同时指定版本和类型，类型参数将被忽略${NC}"
    fi
}

# 主发布函数
release_module() {
    local module="$1"
    local version="$2"

    # 检查模块目录是否存在
    if [ ! -d "$module" ]; then
        echo "${RED}错误: 模块 '$module' 不存在${NC}"
        return
    fi

    echo "\n${GREEN}===[ 发布 $module 模块 ]===${NC}"
    echo "版本: $version"

    # 执行发布步骤
    create_tag "$module" "$version"
}

# 主函数
main() {
    check_git_repo
    parse_args "$@"

    # 确保在正确的分支上
    if [ "$(git branch --show-current)" != "$MAIN_BRANCH" ]; then
        echo "${YELLOW}⚠️  当前分支不是 $MAIN_BRANCH，切换到 $MAIN_BRANCH 分支${NC}"

        if [ "$DRY_RUN" -eq 0 ]; then
            git checkout "$MAIN_BRANCH"
            git pull origin "$MAIN_BRANCH"
        fi
    fi

    # 如果指定了-a，则获取所有模块
    if [ "$all_modules" -eq 1 ]; then
        modules=$(get_all_modules)
    fi

    # 获取当前版本（使用第一个模块的版本作为参考）
    first_module=$(echo $modules | awk '{print $1}')
    current_version=$(get_current_version "$first_module")

    # 确定新版本
    if [ -n "$custom_version" ]; then
        new_version="$custom_version"
    else
        new_version=$(calculate_new_version "$current_version" "$bump_type")
    fi

    validate_version "$new_version"

    echo "\n${GREEN}===[ 发布准备 ]===${NC}"
    echo "当前版本: $current_version"
    echo "新版本: $new_version"
    echo "发布模块: $modules"

    # 更新所有模块间的依赖版本
    repo_url=$(go list -m | head -n 1)
    repo_root=$(echo "$repo_url" | sed 's|/[^/]*$||')
    update_module_dependencies "$new_version" "$repo_root"

    # 生成变更日志
    generate_changelog "$modules" "$new_version"

    # 发布每个模块
    for module in $modules; do
        release_module "$module" "$new_version"
    done

    # 提交所有变更
    commit_changes "$modules" "$new_version"

    # 推送所有变更
    push_changes
}

# 执行主函数
main "$@"
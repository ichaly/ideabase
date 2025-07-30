#!/bin/bash

#=======================================
# 混合版本管理发布脚本
# 支持可选 version.txt + Git 标签
#=======================================

# 配置
MAIN_BRANCH="main"
MODULE_PREFIX=""
DRY_RUN=0
CHANGELOG_FILE="CHANGELOG.md"
USE_VERSION_FILE=1  # 默认使用 version.txt

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
    echo "  -t, --type <类型>     版本类型: major, minor, patch (默认: patch)"
    echo "  -v, --version <版本>  指定精确版本号"
    echo "  -d, --dry-run         模拟运行，不实际提交更改"
    echo "  -n, --no-version-file 不使用 version.txt 文件"
    echo "  -c, --config <文件>   指定配置文件"
    echo "  -h, --help            显示帮助信息"
    echo
    echo "示例:"
    echo "  $0 -m core,app -t minor   # 升级核心和应用模块的次版本"
    echo "  $0 -m cli -v 1.2.3        # 将CLI模块升级到指定版本"
    echo "  $0 -m auth -n              # 发布auth模块但不使用version.txt"
    exit 0
}

# 检查是否在Git仓库中
check_git_repo() {
    if [ ! -d .git ]; then
        echo -e "${RED}错误：当前目录不是Git仓库根目录${NC}"
        exit 1
    fi
}

# 获取模块的当前版本
get_current_version() {
    local module="$1"

    # 1. 尝试从version.txt获取
    if [ "$USE_VERSION_FILE" -eq 1 ] && [ -f "$module/version.txt" ]; then
        current_version=$(head -n 1 "$module/version.txt")
        echo "$current_version"
        return
    fi

    # 2. 尝试从Git标签获取
    local latest_tag=$(git describe --tags --match "${MODULE_PREFIX}${module}/v*" --abbrev=0 2>/dev/null)

    if [ -n "$latest_tag" ]; then
        # 从标签中提取版本号
        echo "$latest_tag" | sed "s#${MODULE_PREFIX}${module}/v##"
        return
    fi

    # 3. 如果都没有，返回0.0.0
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
            echo -e "${RED}错误: 无效的版本类型: $bump_type${NC}"
            exit 1
            ;;
    esac

    echo "$new_version"
}

# 验证版本号格式
validate_version() {
    local version="$1"
    if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}错误: 无效的版本号格式: $version${NC}"
        echo "版本号必须符合语义化版本规范 (例如：1.2.3)"
        exit 1
    fi
}

# 更新版本文件（如果使用）
update_version_file() {
    local module="$1"
    local version="$2"

    if [ "$USE_VERSION_FILE" -eq 1 ]; then
        # 如果文件不存在，创建它
        if [ ! -f "$module/version.txt" ]; then
            echo "$version" > "$module/version.txt"
            echo -e "📄 ${GREEN}创建 $module/version.txt${NC}"
        else
            echo "$version" > "$module/version.txt"
        fi

        git add "$module/version.txt"
        echo -e "📝 ${GREEN}更新 $module 版本: $current_version -> $version${NC}"
    fi
}

# 提交版本变更
commit_changes() {
    local module="$1"
    local version="$2"
    local message="chore($module): release $version"

    if [ "$DRY_RUN" -eq 1 ]; then
        echo -e "${YELLOW}[模拟] git commit -m \"$message\"${NC}"
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
        echo -e "${YELLOW}[模拟] git tag -a $tag -m \"Release $tag\"${NC}"
    else
        git tag -a "$tag" -m "Release $tag"
        echo -e "🏷️  ${GREEN}创建标签: $tag${NC}"
    fi
}

# 推送变更
push_changes() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo -e "${YELLOW}[模拟] git push origin $MAIN_BRANCH${NC}"
        echo -e "${YELLOW}[模拟] git push origin --tags${NC}"
    else
        git push origin "$MAIN_BRANCH"
        git push origin --tags
        echo -e "🚀 ${GREEN}已推送变更到仓库${NC}"
    fi
}

# 生成变更日志
generate_changelog() {
    local module="$1"
    local version="$2"
    local tag="${MODULE_PREFIX}${module}/v${version}"
    local prev_tag range changes

    # 获取历史标签范围
    if [ -z "$(git tag --list "${MODULE_PREFIX}${module}/v*")" ]; then
        range="HEAD"
    else
        prev_tag=$(git describe --tags --match "${MODULE_PREFIX}${module}/v*" --abbrev=0 2>/dev/null)
        range="${prev_tag}..HEAD"
    fi

    # 获取模块提交记录
    changes=$(git log "$range" --pretty=format:"- %s" -- "$module")

    # 生成Markdown内容（始终执行）
    {
        echo "\n## $module v$version ($(date +%Y-%m-%d))"  # 添加版本标题
        echo "$changes"                                      # 插入提交记录
    } >> "$CHANGELOG_FILE"                                 # 直接追加到文件

    # 处理Git操作（仅在非Dry Run时执行）
    if [ "$DRY_RUN" -eq 0 ]; then
        git add "$CHANGELOG_FILE"                          # 只在实际运行时添加文件
        echo -e "📝 ${GREEN}更新 $module 变更日志${NC}"
    else
        echo -e "${YELLOW}[模拟] 更新变更日志 $CHANGELOG_FILE${NC}"
        echo -e "${YELLOW}新增内容:\n$changes\n${NC}"      # 模拟显示新增内容
    fi
}

# 解析命令行参数
parse_args() {
    modules=""
    bump_type="patch"
    custom_version=""

    while [ $# -gt 0 ]; do
        case "$1" in
            -m|--module)
                modules="${2//,/ }"  # 转换逗号为空格
                shift 2
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
            -n|--no-version-file)
                USE_VERSION_FILE=0
                shift
                ;;
            -c|--config)
                CONFIG_FILE="$2"
                if [ -f "$CONFIG_FILE" ]; then
                    source "$CONFIG_FILE"
                else
                    echo -e "${RED}错误: 配置文件 $CONFIG_FILE 未找到${NC}"
                    exit 1
                fi
                shift 2
                ;;
            -h|--help)
                show_help
                ;;
            *)
                echo -e "${RED}错误: 未知选项: $1${NC}"
                show_help
                ;;
        esac
    done

    # 验证参数
    if [ -z "$modules" ]; then
        echo -e "${RED}错误: 必须指定至少一个模块 (-m)${NC}"
        show_help
    fi

    if [ -n "$custom_version" ] && [ -n "$bump_type" ] && [ "$bump_type" != "patch" ]; then
        echo -e "${YELLOW}警告: 同时指定版本和类型，类型参数将被忽略${NC}"
    fi
}

# 主发布函数
release_module() {
    local module="$1"
    local bump_type="$2"
    local custom_version="$3"

    # 检查模块目录是否存在
    if [ ! -d "$module" ]; then
        echo -e "${RED}错误: 模块 '$module' 不存在${NC}"
        return
    fi

    # 获取当前版本
    current_version=$(get_current_version "$module")

    # 确定新版本
    if [ -n "$custom_version" ]; then
        new_version="$custom_version"
    else
        new_version=$(calculate_new_version "$current_version" "$bump_type")
    fi

    validate_version "$new_version"

    echo -e "\n${GREEN}===[ 发布 $module 模块 ]===${NC}"
    echo "当前版本: $current_version"
    echo "新版本: $new_version"

    # 显示版本来源
    if [ "$USE_VERSION_FILE" -eq 1 ] && [ -f "$module/version.txt" ]; then
        echo "版本来源: version.txt"
    else
        echo "版本来源: Git 标签"
    fi

    # 执行发布步骤
    update_version_file "$module" "$new_version"
    generate_changelog "$module" "$new_version"
    commit_changes "$module" "$new_version"
    create_tag "$module" "$new_version"
}

# 主函数
main() {
    check_git_repo
    parse_args "$@"

    # 确保在正确的分支上
    if [ "$(git branch --show-current)" != "$MAIN_BRANCH" ]; then
        echo -e "${YELLOW}⚠️  当前分支不是 $MAIN_BRANCH，切换到 $MAIN_BRANCH 分支${NC}"

        if [ "$DRY_RUN" -eq 0 ]; then
            git checkout "$MAIN_BRANCH"
            git pull origin "$MAIN_BRANCH"
        fi
    fi

    # 检查是否有未提交的更改
    if [ -n "$(git status --porcelain)" ] && [ "$DRY_RUN" -eq 0 ]; then
        echo -e "${RED}错误: 工作区有未提交的更改${NC}"
        git status --short
        exit 1
    fi

    # 发布每个模块
    for module in $modules; do
        release_module "$module" "$bump_type" "$custom_version"
    done

    # 推送所有变更
    push_changes
}

# 执行主函数
main "$@"

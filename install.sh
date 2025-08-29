#!/bin/bash

# 脚本名称: install.sh
# 功能: 同步Go工作区并安装各模块依赖
# 作者: IdeaBase团队
# 日期: 2025-03-27

# 检测终端是否支持颜色
if [ -t 1 ]; then
  # 终端支持颜色
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  BLUE='\033[0;34m'
  NC='\033[0m' # No Color
else
  # 终端不支持颜色或输出被重定向
  GREEN=''
  YELLOW=''
  RED=''
  BLUE=''
  NC=''
fi

echo -e "${YELLOW}IdeaBase 依赖安装工具${NC}"

# 确保在项目根目录执行
if [ ! -f "go.work" ]; then
    echo -e "${RED}错误: 未找到go.work文件，请确保在项目根目录执行此脚本!${NC}"
    exit 1
fi

# 从go.work文件中提取模块目录
MODULES=$(awk '/^use \(/{flag=1;next}/^\)/{flag=0}flag{gsub(/^[ \t]+/,"",$0);print $0}' go.work)

# 清理.sum文件
echo -e "${YELLOW}清理.sum文件...${NC}"

# 删除go.work.sum文件（如果存在）
if [ -f "go.work.sum" ]; then
    echo -e "${BLUE}删除: go.work.sum${NC}"
    rm go.work.sum
fi

# 删除各模块下的go.sum文件
for MODULE in $MODULES; do
    if [ -d "$MODULE" ] && [ -f "${MODULE}/go.mod" ]; then
        if [ -f "${MODULE}/go.sum" ]; then
            echo -e "${BLUE}删除: ${MODULE}/go.sum${NC}"
            rm "${MODULE}/go.sum"
        fi
    fi
done
echo -e "${GREEN}.sum文件清理完成!${NC}"

# 执行go work sync
echo -e "${YELLOW}执行: go work sync${NC}"
go work sync
if [ $? -ne 0 ]; then
    echo -e "${RED}go work sync 执行失败!${NC}"
    exit 1
fi
echo -e "${GREEN}go work sync 执行成功!${NC}"

# 安装模块依赖
echo -e "${YELLOW}安装模块依赖...${NC}"
# 为每个模块更新依赖
for MODULE in $MODULES; do
    echo -e "处理模块: ${MODULE}"
    if [ -d "$MODULE" ] && [ -f "${MODULE}/go.mod" ]; then
        (cd "$MODULE" && go mod tidy && go get -u ./...)
        if [ $? -ne 0 ]; then
            echo -e "${RED}模块 ${MODULE} 依赖安装失败!${NC}"
        else
            echo -e "${GREEN}模块 ${MODULE} 依赖安装成功!${NC}"
        fi
    else
        echo -e "${YELLOW}跳过: ${MODULE} (不是有效的Go模块)${NC}"
    fi
done

echo -e "\n${GREEN}所有模块依赖安装完成!${NC}"

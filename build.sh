#!/bin/bash

# 颜色设置 - 仅根据终端是否支持颜色来决定
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

# 加载.env文件中的环境变量
if [ -f .env ]; then
  source .env
  echo -e "${GREEN}加载.env文件中的环境变量...${NC}"
else
  echo -e "${YELLOW}警告: .env文件不存在，某些功能可能无法正常工作${NC}"
fi

# 设置版本信息
VERSION="v0.1.0"
GIT_COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date '+%Y-%m-%d %H:%M:%S')

# 构建参数
BUILD_DIR="out"
MAIN_FILE="app/main.go"
APP_NAME="ideabase"

# 确保构建目录存在
mkdir -p ${BUILD_DIR}

echo -e "${YELLOW}开始构建 ${APP_NAME} ${VERSION}-${GIT_COMMIT}${NC}"
echo -e "${BLUE}构建时间: ${BUILD_TIME}${NC}"

# 编译Mac版本
echo -e "${YELLOW}编译MacOS版本...${NC}"
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-darwin ${MAIN_FILE}
if [ $? -eq 0 ]; then
  echo -e "${GREEN}MacOS版本编译成功!${NC}"
else
  echo -e "${RED}MacOS版本编译失败!${NC}"
  exit 1
fi

# 编译Linux版本
echo -e "${YELLOW}编译Linux版本...${NC}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-linux ${MAIN_FILE}
if [ $? -eq 0 ]; then
  echo -e "${GREEN}Linux版本编译成功!${NC}"
else
  echo -e "${RED}Linux版本编译失败!${NC}"
  exit 1
fi

# 编译Windows版本
echo -e "${YELLOW}编译Windows版本...${NC}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-windows.exe ${MAIN_FILE}
if [ $? -eq 0 ]; then
  echo -e "${GREEN}Windows版本编译成功!${NC}"
else
  echo -e "${RED}Windows版本编译失败!${NC}"
  exit 1
fi

echo -e "${GREEN}构建完成！${NC}"

#删除所有旧镜像
# docker rmi -f $(docker images | grep "yugong" | awk '{print $3}')

#登录到阿里云镜像中心
# echo -e "${YELLOW}登录到阿里云镜像中心...${NC}"
# docker login -u ${DOCKER_USERNAME} -p ${DOCKER_PASSWORD} registry.cn-qingdao.aliyuncs.com
echo -e "${YELLOW}账号: ${DOCKER_USERNAME} 密码:${DOCKER_PASSWORD}登录到阿里云镜像中心...${NC}"

# Docker相关构建（取消注释使用）
# echo -e "${YELLOW}构建Docker镜像...${NC}"
# docker buildx build --platform linux/amd64 -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:latest \
#   -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:${VERSION}-${GIT_COMMIT} . --push
# if [ $? -eq 0 ]; then
#   echo -e "${GREEN}Docker镜像构建成功!${NC}"
# else
#   echo -e "${RED}Docker镜像构建失败!${NC}"
# fi

echo -e "${BLUE}版本信息：${NC}"
echo -e "版本号: ${VERSION}"
echo -e "Git提交: ${GIT_COMMIT}"
echo -e "构建时间: ${BUILD_TIME}"

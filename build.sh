#!/bin/bash

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

echo "开始构建 ${APP_NAME} ${VERSION}-${GIT_COMMIT}"
echo "构建时间: ${BUILD_TIME}"

# 编译Mac版本
echo "编译MacOS版本..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-darwin ${MAIN_FILE}

# 编译Linux版本
echo "编译Linux版本..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-linux ${MAIN_FILE}

# 编译Windows版本
echo "编译Windows版本..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -ldflags "-w -s \
  -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
  -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
  -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
  -o ${BUILD_DIR}/${APP_NAME}-windows.exe ${MAIN_FILE}

echo "构建完成！"


#删除所有旧镜像
# docker rmi -f $(docker images | grep "yugong" | awk '{print $3}')

#登录到阿里云镜像中心
# docker login -u 15210203617 -p docker123 registry.cn-qingdao.aliyuncs.com

# Docker相关构建（取消注释使用）
# echo "构建Docker镜像..."
# docker buildx build --platform linux/amd64 -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:latest \
#   -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:${VERSION}-${GIT_COMMIT} . --push

echo "版本信息："
echo "版本号: ${VERSION}"
echo "Git提交: ${GIT_COMMIT}"
echo "构建时间: ${BUILD_TIME}"

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

# 检查是否启用UPX压缩（默认不启用）
ENABLE_UPX=${ENABLE_UPX:-0}

# 检查是否安装了UPX
if [ "${ENABLE_UPX}" = "1" ]; then
    if ! command -v upx &> /dev/null; then
        echo -e "${YELLOW}警告: 未检测到UPX，将跳过二进制压缩步骤${NC}"
        echo -e "${YELLOW}可以通过以下命令安装UPX:${NC}"
        echo -e "${BLUE}Mac: brew install upx${NC}"
        echo -e "${BLUE}Linux: sudo apt-get install upx${NC}"
        echo -e "${BLUE}Windows: scoop install upx${NC}"
        echo -e "${YELLOW}如果不需要UPX压缩，可以设置 ENABLE_UPX=0 来禁用${NC}"
        ENABLE_UPX=0
    else
        echo -e "${GREEN}检测到UPX，将对二进制文件进行压缩${NC}"
    fi
else
    echo -e "${BLUE}UPX压缩已禁用${NC}"
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
BINARY_DIR="${BUILD_DIR}/bin"
RELEASE_DIR="${BUILD_DIR}/release"

# 确保构建目录存在
mkdir -p ${BINARY_DIR}
mkdir -p ${RELEASE_DIR}

echo -e "${YELLOW}开始构建 ${APP_NAME} ${VERSION}-${GIT_COMMIT}${NC}"
echo -e "${BLUE}构建时间: ${BUILD_TIME}${NC}"

# 编译函数
build_binary() {
    local os=$1
    local arch=$2
    local suffix=$3
    local output="${BINARY_DIR}/${APP_NAME}-${os}${suffix}"
    
    echo -e "${YELLOW}编译 ${os} 版本...${NC}"
    CGO_ENABLED=0 GOOS=${os} GOARCH=${arch} go build \
        -trimpath \
        -ldflags "-w -s \
        -X 'github.com/ichaly/ideabase/std.Version=${VERSION}' \
        -X 'github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT}' \
        -X 'github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}'" \
        -o ${output} ${MAIN_FILE}
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}${os} 版本编译成功!${NC}"
        # 如果启用了UPX且安装了UPX，进行压缩（跳过macOS）
        if [ "${ENABLE_UPX}" = "1" ] && [ "${os}" != "darwin" ]; then
            echo -e "${YELLOW}使用UPX压缩 ${os} 二进制文件...${NC}"
            upx -9 ${output}
        elif [ "${ENABLE_UPX}" = "1" ] && [ "${os}" = "darwin" ]; then
            echo -e "${YELLOW}跳过 macOS 版本的UPX压缩（不支持）${NC}"
        fi
        return 0
    else
        echo -e "${RED}${os} 版本编译失败!${NC}"
        return 1
    fi
}

# 编译各平台版本
build_binary "darwin" "amd64" "" || exit 1
build_binary "linux" "amd64" "" || exit 1
build_binary "windows" "amd64" ".exe" || exit 1

# 创建发布包
echo -e "${YELLOW}创建发布包...${NC}"

# 为每个平台创建压缩包
for platform in darwin linux windows; do
    if [ "${platform}" == "windows" ]; then
        suffix=".exe"
    else
        suffix=""
    fi
    
    archive_name="${APP_NAME}-${platform}-${VERSION}"
    temp_dir="${RELEASE_DIR}/${archive_name}"
    
    # 创建临时目录
    mkdir -p "${temp_dir}"
    
    # 复制二进制文件和其他必要文件
    cp "${BINARY_DIR}/${APP_NAME}-${platform}${suffix}" "${temp_dir}/"
    cp "README.md" "${temp_dir}/" 2>/dev/null || true
    cp "LICENSE" "${temp_dir}/" 2>/dev/null || true
    
    # 创建压缩包
    (cd "${RELEASE_DIR}" && \
    if [ "${platform}" == "windows" ]; then
        zip -r "${archive_name}.zip" "${archive_name}"
    else
        tar czf "${archive_name}.tar.gz" "${archive_name}"
    fi)
    
    # 清理临时目录
    rm -rf "${temp_dir}"
done

echo -e "${GREEN}构建完成！${NC}"
echo -e "${BLUE}版本信息：${NC}"
echo -e "版本号: ${VERSION}"
echo -e "Git提交: ${GIT_COMMIT}"
echo -e "构建时间: ${BUILD_TIME}"
echo -e "${GREEN}发布包已生成在 ${RELEASE_DIR} 目录下${NC}"
echo -e "${GREEN}二进制文件位于 ${BINARY_DIR} 目录下${NC}"

#删除所有旧镜像
# docker rmi -f $(docker images | grep "yugong" | awk '{print $3}')

#登录到阿里云镜像中心
# echo -e "${YELLOW}登录到阿里云镜像中心...${NC}"
# docker login -u ${DOCKER_USERNAME} -p ${DOCKER_PASSWORD} registry.cn-qingdao.aliyuncs.com
# echo -e "${YELLOW}账号: ${DOCKER_USERNAME} 密码:${DOCKER_PASSWORD}登录到阿里云镜像中心...${NC}"

# Docker相关构建（取消注释使用）
# echo -e "${YELLOW}构建Docker镜像...${NC}"
# docker buildx build --platform linux/amd64 -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:latest \
#   -t registry.cn-qingdao.aliyuncs.com/ichaly/ideabase:${VERSION}-${GIT_COMMIT} . --push
# if [ $? -eq 0 ]; then
#   echo -e "${GREEN}Docker镜像构建成功!${NC}"
# else
#   echo -e "${RED}Docker镜像构建失败!${NC}"
# fi

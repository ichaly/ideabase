#!/bin/bash

echo "开始下载测试所需的 Docker 镜像..."

# PostgreSQL 镜像
echo "下载 PostgreSQL 镜像..."
docker pull docker.io/library/postgres:latest
docker pull docker.io/library/postgres:16
docker pull docker.io/library/postgres:15
docker pull docker.io/library/postgres:14
docker pull docker.io/library/postgres:13

# MySQL 镜像
echo "下载 MySQL 镜像..."
docker pull docker.io/library/mysql:8.0
docker pull docker.io/library/mysql:5.7

echo "所有镜像下载完成！"

# 显示下载的镜像
echo "已下载的镜像列表："
docker images | grep -E 'postgres|mysql' 
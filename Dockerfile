ARG BASE_GOLANG_IMAGE=docker.m.daocloud.io/library/golang:1.25-alpine
ARG BASE_ALPINE_IMAGE=docker.m.daocloud.io/library/alpine:3.20
ARG VERSION=V0.0.0

FROM ${BASE_GOLANG_IMAGE} AS builder
ARG VERSION
WORKDIR /workspace
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off TZ=Asia/Shanghai
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache git tzdata \
    && ln -snf /usr/share/zoneinfo/${TZ} /etc/localtime \
    && echo ${TZ} > /etc/timezone
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://goproxy.cn,direct && go mod download
COPY . .
RUN set -eux; \
    GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo Unknown)"; \
    BUILD_TIME="$(date '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo 1970-01-01T00:00:00)"; \
    LD_FLAGS="-w -s \
        -X github.com/ichaly/ideabase/std.Version=${VERSION:-V0.0.0} \
        -X github.com/ichaly/ideabase/std.GitCommit=${GIT_COMMIT} \
        -X github.com/ichaly/ideabase/std.BuildTime=${BUILD_TIME}"; \
    go build -trimpath \
        -ldflags "$LD_FLAGS" \
        -o /out/main ./main.go

FROM ${BASE_ALPINE_IMAGE}
WORKDIR /opt/app
ENV TZ=Asia/Shanghai
RUN adduser -D -u 10001 bot && chown bot /opt/app \
    && apk add --no-cache ca-certificates tzdata \
    && ln -snf /usr/share/zoneinfo/${TZ} /etc/localtime \
    && echo ${TZ} > /etc/timezone
COPY --from=builder /out/main ./main
COPY cfg ./cfg
EXPOSE 8080
USER bot
ENTRYPOINT ["./main","start"]

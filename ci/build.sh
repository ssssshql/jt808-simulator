#!/bin/sh
# ci/build.sh <GOOS> <GOARCH> <输出文件>：注入版本号（git describe）、提交、构建时间
set -eu
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
GOOS="$1" GOARCH="$2" CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildTime=$BUILD_TIME" -o "$3" .
echo "built $3 ($1/$2) version=$VERSION commit=$COMMIT time=$BUILD_TIME"

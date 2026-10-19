# Agent GhostReaper 构建链:
#   Next.js 静态导出(web/out)→ 拷入 internal/webui/dist → go:embed → 单二进制。
# internal/webui/dist 内置占位 index.html,故 go build / go test 不依赖前端构建;
# make build 总是先重跑前端构建并覆盖 dist。

BINARY   := bin/ghostreaper
WEB_DIR  := web
WEB_DIST := internal/webui/dist
# 版本号(P3-13b ③:运行日志日志头与诊断包携带):默认日期版本,可
# 环境变量覆盖;经 -ldflags 注入 internal/buildinfo.Version。
VERSION  ?= v1.0.0-$(shell date -u +%Y%m%d)
LDFLAGS  := -X ghostreaper/internal/buildinfo.Version=$(VERSION)

.PHONY: all build build-all web test run clean \
        dvwa-up dvwa-down dvwa-status dvwa-reset dvwa-clean \
        vulhub-fetch vulhub-up vulhub-down

all: build

build: web
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ghostreaper

# 三平台交叉编译(P3-12;macOS 双架构,共四产物):纯 Go 驱动
# modernc.org/sqlite 无 CGO,交叉编译零工具链依赖;产物名带 os-arch
# 后缀,Windows 带 .exe。
build-all: web
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/ghostreaper
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/ghostreaper
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/ghostreaper
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-windows-amd64.exe ./cmd/ghostreaper

web:
	cd $(WEB_DIR) && { [ -f package-lock.json ] && npm ci || npm install; } && NEXT_TELEMETRY_DISABLED=1 npm run build
	rm -rf $(WEB_DIST) && cp -R $(WEB_DIR)/out $(WEB_DIST)

test:
	go test ./...

run: build
	./$(BINARY)

# ---- 本地靶场(P3-04,使用说明见 lab/README.md)----

# 主力 DVWA:一条命令起(缺镜像自动构建,自动初始化)
dvwa-up:
	lab/dvwa.sh up

dvwa-down:
	lab/dvwa.sh down

dvwa-status:
	lab/dvwa.sh status

dvwa-reset:
	lab/dvwa.sh reset

dvwa-clean:
	lab/dvwa.sh clean

# 补充 Vulhub:按需起 / 打完关(环境名见 lab/vulhub.sh list)
vulhub-fetch:
	lab/vulhub.sh fetch

vulhub-up:
	lab/vulhub.sh up $(VULHUB_ENV)

vulhub-down:
	lab/vulhub.sh down $(VULHUB_ENV)

# 不清理 WEB_DIST:embed 目录需恒存在,web 目标会整体覆盖它
clean:
	rm -rf bin $(WEB_DIR)/out $(WEB_DIR)/.next

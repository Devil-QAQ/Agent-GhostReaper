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

# 前置闸(P4-15 ②):未定义 JSX 引用检查——引用未导入/未声明的组件
# 构建器不报错、浏览器运行时才黑屏(v1.0.1/v1.0.2 连穿两版),同类
# 错误自此过不了构建。
# 前置闸(P4-19):界面禁用词检查——前端源码与 Go 界面串按审定词表
# (18 条映射,2026-10-23 造物主审定)扫字符串字面量,命中过不了构建。
# 前置闸(P4-26):旧色值巡检——换装前品牌色板(蓝系/碳蓝底/旧语义色)
# 在源码零残留,图页画布回退值与 globals.css token 同源对账。
# 前置闸(P4-27 ②b):任务状态全矩阵测试——9 后端态 × 受阻 × 冬眠
# 36 格唯一显示词零并排,取态改动过不了构建。
# 前置闸(P4-28 ⑥):旧状态词残留检查——显示层合并 12→6 后,旧状态词
# 不得再作徽标/筛选/时间线显示词,报告导出状态词与六词对齐。
# 前置闸(P4-31 ⑨):融合设计律扫描——字号五级/间距阶梯/组件高度/圆角
# 尺寸表/写死色值,覆盖 views 与 components 全部文件(含 ui/ 原件与
# 弹窗抽屉模板),越级值过不了构建。
# 前置闸(P4-60 ③):无保护成员方法调用扫描——属性链直调
# startsWith/map/length 等且成员名前无 ?.,上游字段缺失即整页白屏
# (v1.2.1 审批记录详情崩溃同类);存量安全点人工核定入白名单
# (web/scripts/unprotected-methods-whitelist.txt)随仓维护,新增违例
# 与白名单失配都过不了构建。
# 前置闸(P4-61):发现区四页签排序类级测试——时间倒序口径(立账时刻,
# 同刻版本号)/三区归类(过程置顶/正常/废弃沉底)/区优先于时间/重开再
# 确认位置不变/乱序落位确定/筛选与排序交换;比较器改动过不了构建。
# 前置闸(P4-74 ④):审批记录折叠类级测试——机器放行留痕连续段折叠
# 一行计数(明细可达零丢失)、操作者裁决与待决恒可见、空参数判定;
# 折叠逻辑改动过不了构建。
web:
	node $(WEB_DIR)/scripts/check-jsx-refs.mjs
	node $(WEB_DIR)/scripts/check-ui-words.mjs
	node $(WEB_DIR)/scripts/check-hardcoded-colors.mjs
	node $(WEB_DIR)/scripts/check-status-words.mjs
	node $(WEB_DIR)/scripts/check-fusion-rules.mjs
	node $(WEB_DIR)/scripts/check-unbounded-events.mjs
	node $(WEB_DIR)/scripts/check-unprotected-methods.mjs
	node $(WEB_DIR)/scripts/test-task-status-matrix.mjs
	node $(WEB_DIR)/scripts/test-finding-sort.mjs
	node $(WEB_DIR)/scripts/test-approval-fold.mjs
	cd $(WEB_DIR) && { [ -f package-lock.json ] && npm ci || npm install; } && NEXT_TELEMETRY_DISABLED=1 npm run build
	rm -rf $(WEB_DIST) && cp -R $(WEB_DIR)/out $(WEB_DIST)

test:
	node $(WEB_DIR)/scripts/check-ui-words.mjs
	node $(WEB_DIR)/scripts/check-hardcoded-colors.mjs
	node $(WEB_DIR)/scripts/check-status-words.mjs
	node $(WEB_DIR)/scripts/check-fusion-rules.mjs
	node $(WEB_DIR)/scripts/check-unbounded-events.mjs
	node $(WEB_DIR)/scripts/test-fusion-rules.mjs
	node $(WEB_DIR)/scripts/test-task-status-matrix.mjs
	node $(WEB_DIR)/scripts/test-finding-sort.mjs
	node $(WEB_DIR)/scripts/test-approval-fold.mjs
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

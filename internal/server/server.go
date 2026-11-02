// Package server 是面板后端 API 层。P1-01 建健康检查,P1-07 挂安全闸门
// 全套面板 API(任务、RoE(目标管理 + 操作约束)、审批弹门裁决),P1-09
// 挂面板成品:任务熔断(先控制事件后 cancel 的次序由 Manager 保证)、
// 建任务资产种子表单(12 类,"资产同步"v1)、资产树(读投影);P1-10
// 挂任务级 md 简版报告(确定性组装,零 LLM)。
// 全部操控走账本(R7 无旁路):RoE/目标变更记账后唤醒任务主宰,
// 按生效规则下一轮生效;审批裁决先记账后送达执行方。
// P3-08 起挂实时推送:账本提交钩子 → 主题广播 → /api/ws(server_live.go)。
// P3-13b 挂会话管理与诊断包(①发现溯源装配 + ②会话标记端点 + ④诊断包与日志级别);错误响应与崩溃兑底落运行日志。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghostreaper/internal/buildinfo"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/interject"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/uapolicy"
	"ghostreaper/internal/websearch"
)

// Health 报告进程存活,供面板与监控探活(P1-01 起,保持不动)。
func Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"service":"ghostreaper","status":"ok"}`))
}

// Version 面板版本号(P4-26 ④):返回 buildinfo.Version——与运行日志头、
// 诊断包同一来源(Makefile 经 ldflags 注入,裸构建为 dev),三处一处
// 来源,排查混版本问题一眼可辨。
func Version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	body, _ := json.Marshal(map[string]string{"version": buildinfo.Version})
	_, _ = w.Write(body)
}

// Server 面板后端:账本 + Manager + 审批门的装配视图。零值不可用。
type Server struct {
	Store   *rings.Store
	Manager *manager.Manager
	Gate    swarm.ApprovalGate
	// Helm 舵杆(P2-05,第八章第 8 节):kill_work 的下达入口(steer_work
	// 的面板入口是插话 /interject/messages,直连 /steer 已按 P3-14b ③
	// 删除——通用规则 10 禁旁路)。New 自建无执行器接线的默认实例(未配
	// LLM 时 kill 如实报无单位可停);装配层有执行器时替换为执行器的
	// 同一实例(与审批门同一装配形态,非旁路)。
	Helm *swarm.Helm
	// Swarm 虫群执行器(P2-06b 研判后放行:拒绝线记录的逐次强制执行
	// 走它的工具集与镜装配)。装配层有执行器时注入;nil = 无执行器,
	// 放行端点如实报不可用(不降级)。
	Swarm *swarm.Executor
	// Chain 主备熔断链(P2-06e):模型库写路径经它重载(装配层注入,
	// 与执行器同一 Provider 实例);nil = 未装配(库照常可写,链在
	// 下次装配时消费)。
	Chain *failover.Chain
	// Shepherd 牧者会话服务(P2-07,第四章 §1 牧者:每任务一个总对话
	// 入口)。装配层注入(main.go);nil = 未装配(会话端点如实报
	// 不可用,不降级)。
	Shepherd *shepherd.Service
	// Interject 插话子会话服务(P2-08,第四章 §7:问/改的旁路子会话)。
	// 装配层注入;nil = 未装配(插话端点如实报不可用,不降级)。
	Interject *interject.Service
	// HiberGate 任务冬眠门(P2-08):审批冻结与任务状态判定的运行时
	// 权威(与 Manager/执行器共持同一实例)。nil = 冬眠端点如实报不可用。
	HiberGate *hiber.Gate
	// Loops 在跑主宰循环注册表(P2-08 单位控制的主宰循环粒度:暂停/
	// 继续经注册表找到 Loop 实例)。nil = 主宰循环控制如实报不可用。
	Loops *overmind.Registry
	// RootDir 任务工作目录根(P2-08b 会话原文读:任务的 transcripts/
	// 子目录,单位执行轨迹与研判原文下钻的数据源)。空 = 转录读端点
	// 如实报 exists=false。
	RootDir string
	// Config 系统装配信息(设置页视图的静态部分:LLM/并发/纠偏节奏);
	// 零值=LLM 未注入,main.go 装配时填入真实值。
	Config ConfigView

	// releaseMu 研判后放行的并发互斥(同请求并发放行只许一次执行;
	// 跨重启由账本 forced 记录的存在性兜底)。
	releaseMu sync.Mutex

	// live 实时推送枢纽(P3-08):账本 OnCommit → 主题广播 → /api/ws
	// 在线连接。New 时创建并挂上账本钩子(单 Store 单观察者,后建覆盖
	// 先建);nil 不会发生(New 恒建)。
	live *liveHub

	// assemMu 运行时装配互斥(P3-05b ⑤ 模型激活免重启):模型库写路径
	// 触发装配层换入 Swarm/Helm 指针,读侧一律经 swarmExec()/helmOf()
	// 取快照,不直读字段。
	assemMu sync.Mutex

	// RuntimeAssemble 运行时装配钩子(P3-05b ⑤):链经模型库写路径重载
	// 后变就绪时触发(复用 06e 配置写触发链重载的既有路径),装配层
	// 注入——拉起镜/主宰循环挂接/虫群执行器并经 AttachRuntime 换入。
	// nil = 无装配层(单测);须幂等(装配层自判已装配即返回)。
	RuntimeAssemble func()
}

// ConfigView 设置最简(8.3 治理域)的读模型:模型经环境变量运行时注入;
// 虫群并发/纠偏节奏为装配常量。执行预算的正式读写入口在 Agent 配置栏
// (/api/agents,P2-08d 迁入;旧 /api/config/budget 旁路写已随 P4-01 删)。
type ConfigView struct {
	LLM struct {
		Configured bool   `json:"configured"` // GR_* 环境变量齐备与否
		Model      string `json:"model,omitempty"`
		Format     string `json:"format,omitempty"` // openai|anthropic
	} `json:"llm"`
	Swarm struct {
		MaxConcurrentUnits int `json:"max_concurrent_units"` // 虫群并发上限(overmind 默认 3)
	} `json:"swarm"`
	// Deviation 纠偏纵深(P2-05,第八章第 8 节):重锚定默认间隔(面板
	// 建任务表单可按任务调整)与心跳巡检节奏(看门狗,机器预筛)。
	Deviation struct {
		AnchorEverySteps   int `json:"anchor_every_steps"`   // 重锚定默认间隔(步)
		HeartbeatSeconds   int `json:"heartbeat_seconds"`    // 心跳巡检间隔(秒)
		StallWindowSeconds int `json:"stall_window_seconds"` // 进度停滞判定窗口(秒)
		BurnAnomalyTokens  int `json:"burn_anomaly_tokens"`  // 预算消耗异常阈值(token/拍)
	} `json:"deviation"`
	// System 系统级参数(系统设置页只读呈现;P2-08c 设置收窄为系统级,
	// 装配层注入)。
	System struct {
		Addr     string `json:"addr"`      // HTTP 监听地址
		DBPath   string `json:"db_path"`   // 年轮账本 SQLite 路径
		TasksDir string `json:"tasks_dir"` // 任务工作目录根
	} `json:"system"`
	// Search 搜索配置(P3-03,系统设置「搜索配置」卡):key 与代理凭据
	// 永不回显明文——只给打码视图与"是否已配置"。
	Search SearchView `json:"search"`
	// UA 出站 UA 策略(P3-13c,系统设置「UA 策略」卡):http_probe /
	// web_fetch 的 User-Agent(随机池/固定自定义/模拟工具)。
	UA uapolicy.Config `json:"ua"`
}

// SearchView 搜索配置的服务端视图(key 打码 / 代理去凭据;设置表原值
// 仅工具运行时读取)。
type SearchView struct {
	Backend       string `json:"backend"` // ddgs|brave|tavily
	BraveKeyMask  string `json:"brave_key_mask,omitempty"`
	BraveHasKey   bool   `json:"brave_has_key"`
	TavilyKeyMask string `json:"tavily_key_mask,omitempty"`
	TavilyHasKey  bool   `json:"tavily_has_key"`
	ProxyMask     string `json:"proxy_mask,omitempty"`
	ProxySet      bool   `json:"proxy_set"`
}

// effectiveSearch 读设置表拼搜索配置视图(打码;handleConfig 每次重算,
// 写路径保存后同源返回)。
func (s *Server) effectiveSearch(ctx context.Context) SearchView {
	cfg := websearch.Load(ctx, s.Store)
	return SearchView{
		Backend:       websearch.NormalizeBackend(cfg.Backend),
		BraveKeyMask:  failover.MaskKey(cfg.BraveKey),
		BraveHasKey:   cfg.BraveKey != "",
		TavilyKeyMask: failover.MaskKey(cfg.TavilyKey),
		TavilyHasKey:  cfg.TavilyKey != "",
		ProxyMask:     failover.MaskProxy(cfg.Proxy),
		ProxySet:      cfg.Proxy != "",
	}
}

// DefaultConfig 零值 Config 的补全:并发取包默认值,LLM 未注入。
func DefaultConfig() ConfigView {
	c := ConfigView{}
	c.Swarm.MaxConcurrentUnits = overmind.DefaultMaxConcurrentUnits
	c.Deviation.AnchorEverySteps = swarm.DefaultAnchorEverySteps
	c.Deviation.HeartbeatSeconds = int(overmind.DefaultHeartbeatInterval.Seconds())
	c.Deviation.StallWindowSeconds = overmind.DefaultStallSeconds
	c.Deviation.BurnAnomalyTokens = overmind.DefaultBurnAnomalyTokens
	return c
}

// New 构建面板后端。
func New(store *rings.Store, mgr *manager.Manager, gate swarm.ApprovalGate) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("server: 缺少年轮账本句柄")
	}
	if mgr == nil {
		return nil, fmt.Errorf("server: 缺少 Manager")
	}
	if gate == nil {
		return nil, fmt.Errorf("server: 缺少审批门")
	}
	// 内置硬规则播种(P2-08e):内置清单写缺失键,幂等不覆盖用户改动。
	if err := seedHardRulesOnce(store); err != nil {
		return nil, fmt.Errorf("server: 播种内置硬规则: %w", err)
	}
	// 实时推送枢纽(P3-08):挂账本提交钩子——一切面板关心的状态变化
	// 经 Append 落库后单点通知,广播归枢纽自身循环。
	live := newLiveHub(gate)
	store.SetOnCommit(live.onCommit)
	return &Server{Store: store, Manager: mgr, Gate: gate, Helm: swarm.NewHelm(store),
		Config: DefaultConfig(), live: live}, nil
}

// swarmExec 虫群执行器快照(P3-05b ⑤:运行时装配可换入,读侧经此取,
// 与直读字段互斥)。
func (s *Server) swarmExec() *swarm.Executor {
	s.assemMu.Lock()
	defer s.assemMu.Unlock()
	return s.Swarm
}

// helmOf 舵杆实例快照(同 swarmExec)。
func (s *Server) helmOf() *swarm.Helm {
	s.assemMu.Lock()
	defer s.assemMu.Unlock()
	return s.Helm
}

// AttachRuntime 运行时换入执行器全家桶(P3-05b ⑤:装配层在链就绪后
// 调用——Swarm 与 Helm(executor 的同一舵杆实例)同锁换入;启动装配
// 也走本入口)。
func (s *Server) AttachRuntime(exec *swarm.Executor) {
	s.assemMu.Lock()
	defer s.assemMu.Unlock()
	if exec != nil {
		s.Swarm = exec
		s.Helm = exec.Helm
	}
}

// Handler 挂载全部面板 API(返回 *ServeMux 便于调用方续挂前端等路由)。
func (s *Server) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", Health)
	mux.HandleFunc("GET /api/version", Version) // P4-26 ④ 面板版本(与日志头/诊断包同源)
	mux.HandleFunc("GET /api/ws", s.handleWS)   // P3-08 实时推送(状态变化主动推面板)
	// ---- 面板认证(P3-09):密码登录换发 token;除认证入口外一切
	// 路由经 WrapAuth(生产装配在 main.go 包外层;测试直挂 Handler()
	// 时不设防)。 ----
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("POST /api/auth/password", s.handleAuthPassword)
	// ---- 目标凭据(P3-09):存储与调用分离——面板配置面在此,执行侧
	// 调用(任务包清单 + bash 环境注入)在 internal/swarm。 ----
	mux.HandleFunc("GET /api/credentials", s.handleCredentialsList)
	mux.HandleFunc("POST /api/credentials", s.handleCredentialSave)
	mux.HandleFunc("DELETE /api/credentials/{id}", s.handleCredentialDelete)
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("POST /api/tasks", s.handleTaskCreate)
	mux.HandleFunc("POST /api/tasks/extract", s.handleTaskExtract)            // P2-08f 建任务"从描述提取"(字面路由优先于 {id})
	mux.HandleFunc("POST /api/tasks/assets-organize", s.handleAssetsOrganize) // P4-16 资产一键优化整理(守恒把关)
	mux.HandleFunc("POST /api/tasks/{id}/stop", s.handleTaskStop)
	mux.HandleFunc("POST /api/tasks/{id}/closure", s.handleTaskClosure)              // P4-18 收口提醒两键裁决
	mux.HandleFunc("POST /api/tasks/{id}/closure/config", s.handleTaskClosureConfig) // 审核迭代 1 任务级配置变更
	mux.HandleFunc("POST /api/tasks/{id}/meta", s.handleTaskMeta)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleTaskDelete)
	mux.HandleFunc("POST /api/tasks/batch", s.handleTaskBatch)
	mux.HandleFunc("GET /api/task-categories", s.handleTaskCategoriesList)
	mux.HandleFunc("POST /api/task-categories", s.handleTaskCategoryCreate)
	mux.HandleFunc("POST /api/task-categories/{id}", s.handleTaskCategoryRename)
	mux.HandleFunc("DELETE /api/task-categories/{id}", s.handleTaskCategoryDelete)
	mux.HandleFunc("GET /api/search", s.handleSearch) // P3-10 命令面板:跨任务搜任务与发现
	mux.HandleFunc("GET /api/tasks/{id}/assets", s.handleAssetTree)
	mux.HandleFunc("POST /api/tasks/{id}/assets", s.handleAssetCreate) // P4-05 手动新增资产(批量,来源=手动)
	mux.HandleFunc("GET /api/tasks/{id}/assets/{nid}", s.handleAssetDetail)
	// 统一图投影(P2-09,8.7 三视图共用):树/DAG/全图共用的账本→图数据
	// 唯一端点,进度图为首个接入方(年轮图 P2-11 接入)。
	mux.HandleFunc("GET /api/tasks/{id}/graph", s.handleTaskGraph)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/config/search", s.handleSearchUpdate)   // P3-03 搜索配置(后端/key/独立出口代理)
	mux.HandleFunc("POST /api/config/ua", s.handleUAUpdate)           // P3-13c UA 策略(随机池/固定自定义/模拟工具)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnosticsExport) // P3-13b ④ 诊断包(日志+版本+平台+脱敏配置 zip;P4-32 范围过滤与内容勾选)

	// ---- Agent 配置(P2-08d):牧者/主宰/虫群三组——提示词可编辑正文
	// (A 段保存/恢复默认,固定段只读)+ 运行参数(并发/重锚定/心跳/
	// 执行预算,自系统设置迁入)。读写与执行侧同一规约与键值表,无旁路。 ----
	mux.HandleFunc("GET /api/agents", s.handleAgentsGet)
	mux.HandleFunc("POST /api/agents/prompt", s.handleAgentPromptSave)
	mux.HandleFunc("POST /api/agents/prompt/reset", s.handleAgentPromptReset)
	mux.HandleFunc("POST /api/agents/params", s.handleAgentParamsUpdate)

	// ---- 模型库与主备(P2-06e,第七章 §5:设置页存多个模型配置、激活
	// 主用;Key 永不回显明文,GET 打码)。配置写后链重载 + 自动重开
	// blocked 的 Step(恢复两层之自动层)。P2-08c 增:测试连接/从 API
	// 拉模型列表/立即恢复(LLM 配置页)。 ----
	mux.HandleFunc("GET /api/models", s.handleModelsGet)
	mux.HandleFunc("POST /api/models", s.handleModelSave)
	mux.HandleFunc("DELETE /api/models/{name}", s.handleModelDelete)
	mux.HandleFunc("POST /api/models/activate", s.handleModelActivate)
	mux.HandleFunc("POST /api/models/test", s.handleModelTest)
	mux.HandleFunc("POST /api/models/fetch-list", s.handleModelFetchList)
	mux.HandleFunc("POST /api/models/{name}/recover", s.handleModelRecover)
	mux.HandleFunc("POST /api/models/{name}/reveal-key", s.handleModelRevealKey) // P3-05c ② 已存 key 明文查看(受控+审计)

	// ---- 恢复两层(P2-06e):自动重开(模型配置写路径)+ 恢复全部
	//(人工兜底,任务列表头部)+ 单任务恢复(P3-05c ⑥,工作台)。三条
	// 路径同一实现:重开 blocked 步骤 + 执行侧拉起(暂停任务 Resume)。 ----
	mux.HandleFunc("POST /api/tasks/recover-all", s.handleRecoverAll)
	mux.HandleFunc("POST /api/tasks/{id}/recover", s.handleTaskRecover)
	mux.HandleFunc("GET /api/tasks/{id}/roe", s.handleRoEGet)
	mux.HandleFunc("POST /api/tasks/{id}/roe", s.handleRoEUpdate)
	mux.HandleFunc("POST /api/tasks/{id}/roe/assets", s.handleRoEAssetScope) // P4-05 授权资产范围(纳入/摘除)
	mux.HandleFunc("GET /api/tasks/{id}/goals", s.handleGoalsList)
	mux.HandleFunc("POST /api/tasks/{id}/goals", s.handleGoalCreate)
	mux.HandleFunc("POST /api/tasks/{id}/goals/{gid}/achieve", s.handleGoalAchieve)
	mux.HandleFunc("GET /api/tasks/{id}/claims", s.handleClaimsList)
	mux.HandleFunc("POST /api/tasks/{id}/claims/{cid}/retest", s.handleClaimRetest)
	mux.HandleFunc("POST /api/tasks/{id}/claims/{cid}/replay", s.handleClaimReplay)
	mux.HandleFunc("GET /api/tasks/{id}/claims/{cid}", s.handleClaimDetail)
	mux.HandleFunc("POST /api/tasks/{id}/claims/{cid}/reopen", s.handleClaimReopen)
	mux.HandleFunc("GET /api/tasks/{id}/hypotheses", s.handleHypothesesList)
	mux.HandleFunc("GET /api/tasks/{id}/hypotheses/{hid}", s.handleHypothesisDetail)
	mux.HandleFunc("GET /api/tasks/{id}/facts", s.handleFactsList)
	mux.HandleFunc("GET /api/tasks/{id}/report", s.handleReport)
	mux.HandleFunc("GET /api/tasks/{id}/report/export", s.handleTaskReportExport)  // P2-13 任务级格式矩阵
	mux.HandleFunc("GET /api/tasks/{id}/claims/{cid}/export", s.handleClaimExport) // P2-13 漏洞级导出
	mux.HandleFunc("GET /api/report/export", s.handleCrossReportExport)            // P2-13 跨任务导出(一级·报告页)
	mux.HandleFunc("GET /api/tasks/{id}/report/archives", s.handleReportArchives)  // P3-02 存档清单 + 当前证据版本
	mux.HandleFunc("POST /api/tasks/{id}/report/archive", s.handleReportArchive)   // P3-02 存档写入(evidence_version 乐观锁,冲突 409)
	mux.HandleFunc("GET /api/tasks/{id}/report/archives/{rev}", s.handleReportArchiveDownload)
	mux.HandleFunc("GET /api/tasks/{id}/approvals", s.handleApprovalsList)
	mux.HandleFunc("POST /api/tasks/{id}/approvals/{rid}", s.handleApprovalDecide)
	mux.HandleFunc("POST /api/tasks/{id}/approvals/{rid}/release", s.handleApprovalRelease)
	mux.HandleFunc("POST /api/tasks/{id}/wake", s.handleWake)

	// ---- 舵杆(P2-05,第八章第 8 节:操作者侧 kill_work 与指令审计流;
	// steer_work 的面板入口是插话 /interject/messages(内经舵杆同一
	// 实例),直连 POST /steer 已按 P3-14b ③ 删除——展示与操作同源,
	// 禁旁路装配) ----
	mux.HandleFunc("GET /api/tasks/{id}/steers", s.handleSteersList)
	mux.HandleFunc("POST /api/tasks/{id}/kill-work", s.handleKillWork)

	// ---- P2-04 四类记录分工的只读取证端点:token 独立表逐次用量与
	// 黑匣子系统事件(任务侧证据视图;仪表盘属 P2-12,此处只出原始行)。 ----
	mux.HandleFunc("GET /api/tasks/{id}/tokens", s.handleTaskTokens)
	mux.HandleFunc("GET /api/tasks/{id}/blackbox", s.handleTaskBlackbox)

	// P2-12 一级·观测:Token 成本仪表盘 / 工具执行流水 / 两个吞吐指标
	//(全部只读既有记账,零新增采集)。
	mux.HandleFunc("GET /api/obs/tokens", s.handleObsTokens)
	mux.HandleFunc("GET /api/obs/tools", s.handleObsTools)
	mux.HandleFunc("GET /api/obs/throughput", s.handleObsThroughput)
	mux.HandleFunc("GET /api/obs/intercepts", s.handleObsIntercepts) // P4-07 拦截命中统计

	// ---- 牧者会话(P2-07,第四章 §1:每任务一个总对话入口,看/说/答;
	// 会话域布局的牧者组数据面——消息送达状态/工具折叠卡/纳谏轨迹/
	// 信息条四项)。节点详情端点为 fact# 等引用的点击核对面。 ----
	mux.HandleFunc("GET /api/tasks/{id}/shepherd", s.handleShepherdGet)
	mux.HandleFunc("POST /api/tasks/{id}/shepherd/messages", s.handleShepherdPost)
	// 成果追问会话(P4-14,问题清单 #24):发现详情"追问"区的数据面,
	// 牧者答题、独立留存;问答不进报告(导出零影响)。
	mux.HandleFunc("GET /api/tasks/{id}/shepherd/claim/{cid}", s.handleShepherdClaimGet)
	mux.HandleFunc("POST /api/tasks/{id}/shepherd/claim/{cid}/messages", s.handleShepherdClaimPost)
	mux.HandleFunc("GET /api/tasks/{id}/nodes/{nid}", s.handleNodeDetail)

	// ---- 人机通道(P2-08,第四章 §7 v1.8:插话 + 三级控制) ----
	// 插话子会话(问/改)与单位列表;单位控制(暂停/继续,停止走既有
	// kill-work)与主宰循环控制;任务控制(冬眠/唤醒;熔断走既有 stop)。
	mux.HandleFunc("GET /api/tasks/{id}/units", s.handleUnitsList)
	mux.HandleFunc("GET /api/tasks/{id}/transcripts/{sid}", s.handleTranscriptGet)
	mux.HandleFunc("POST /api/tasks/{id}/sessions/{sid}/meta", s.handleSessionMeta) // P3-13b ② 会话重命名/备注/置顶
	mux.HandleFunc("GET /api/tasks/{id}/interject", s.handleInterjectGet)
	mux.HandleFunc("POST /api/tasks/{id}/interject/messages", s.handleInterjectPost)
	mux.HandleFunc("POST /api/tasks/{id}/units/{step}/pause", s.handleUnitPause)
	mux.HandleFunc("POST /api/tasks/{id}/units/{step}/resume", s.handleUnitResume)
	mux.HandleFunc("GET /api/tasks/{id}/overmind", s.handleOvermindStatus)
	mux.HandleFunc("POST /api/tasks/{id}/overmind/pause", s.handleOvermindPause)
	mux.HandleFunc("POST /api/tasks/{id}/overmind/resume", s.handleOvermindResume)
	mux.HandleFunc("POST /api/tasks/{id}/hibernate", s.handleTaskHibernate)
	mux.HandleFunc("POST /api/tasks/{id}/hibernate-wake", s.handleTaskHibernateWake)
	mux.HandleFunc("POST /api/tasks/{id}/checkpoint", s.handleTaskCheckpoint) // P3-07 存档断点

	// ---- 拦截规则引擎(P2-08e):全局硬规则管理(系统设置页正式端点);
	// 任务级规则逐条增删改 + 自然语言提取(审核迭代),变更落 roe.updated
	// 并定向告知受影响的在飞单位(经舵杆,范围外单位零打扰) ----
	mux.HandleFunc("GET /api/guard/rules", s.handleGuardRulesList)
	mux.HandleFunc("POST /api/guard/rules", s.handleGuardRulesSave)
	mux.HandleFunc("DELETE /api/guard/rules/{id}", s.handleGuardRulesDelete)
	mux.HandleFunc("POST /api/tasks/{id}/roe/rules", s.handleRoERuleAdd)
	mux.HandleFunc("POST /api/tasks/{id}/roe/rules/patterns", s.handleRoERulePatterns)
	mux.HandleFunc("POST /api/tasks/{id}/roe/rules/{rid}", s.handleRoERuleEdit)
	mux.HandleFunc("DELETE /api/tasks/{id}/roe/rules/{rid}", s.handleRoERuleDelete)

	// ---- 流量页与证据绑定(P3-02,8.3 一级·流量 + 第五章绑定三机制):
	// 镜留痕跨任务列表(筛选)、单条交换元数据与分段原文回放(blob 惰性
	// 读)、TestClaim ↔ 流量证据绑定/补绑(入账,R7 无旁路)。 ----
	mux.HandleFunc("GET /api/mirror/exchanges", s.handleTrafficList)
	mux.HandleFunc("GET /api/mirror/exchanges/{exID}", s.handleTrafficDetail)
	mux.HandleFunc("GET /api/mirror/exchanges/{exID}/raw", s.handleTrafficRaw)
	mux.HandleFunc("POST /api/tasks/{id}/claims/{cid}/evidence", s.handleClaimEvidenceBind)
	return mux
}

// ---- 通用小件 ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 错误响应:面板错误人话化——writeErr 是 API 错误的单一流出
// 口,内部模块名前缀在此统一剥除(P3-11a ②,8.5 第七条:内部模块名/
// 包名不上界面);日志与账本里的错误原文不受影响,剥除只发生在界面
// 边界。5xx(服务端错误)同步落运行日志 ERROR(P3-13b ③);4xx 是
// 请求侧错误,不进日志。
func writeErr(w http.ResponseWriter, code int, err error) {
	if code >= http.StatusInternalServerError {
		runlog.Errorf("面板 API 服务端错误(%d): %v", code, err)
	}
	writeJSON(w, code, map[string]string{"error": humanErr(err)})
}

// internalModPrefixRe 内部模块名前缀(错误链里 fmt.Errorf("%s: …")
// 层层包装带出的包名):匹配"前缀 + 半角冒号 + 空格",锚定行首或
// 非字母数字字符之后——嵌套包装(swarm: …: rings: …)循环剥净,
// 正常中文错误文本不误伤。
var internalModPrefixRe = regexp.MustCompile(
	`(^|[^a-zA-Z0-9])(?:swarm|rings|overmind|report|failover|arbiter|manager|secrets|vault|shepherd|mirror|server|interject|websearch|mocktarget): `)

// humanErr 剥除错误文本里的内部模块名前缀(含嵌套,至多 4 轮),其余
// 原样——错误正文本身已是"什么错了"的人话原因;剥完为空的极端形态
// 退原始文本,不返回空串。
func humanErr(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	s := raw
	for i := 0; i < 4; i++ {
		next := internalModPrefixRe.ReplaceAllString(s, "$1")
		if next == s {
			break
		}
		s = next
	}
	if strings.TrimSpace(s) == "" {
		return raw
	}
	return strings.TrimSpace(s)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return false
	}
	return true
}

// shortID 面板侧生成的短随机 ID(任务/目标)。
func shortID(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, 0)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

// taskExists 任务须已在账本(任务侧一切面板操作的可见性前提)。
func (s *Server) taskExists(taskID string) bool {
	rows, err := s.Store.Tasks(context.Background())
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.ID == taskID {
			return true
		}
	}
	return false
}

// wake 唤醒任务的主宰循环(RoE/目标变更通知主宰,第四章 §4)。
// 未在跑任务的唤醒落进容量 1 的通道,拉起后消费,不丢。
func (s *Server) wake(taskID string) {
	if t := s.Manager.Task(taskID); t != nil {
		t.Wake()
	}
}

// ---- 任务 ----

// handleTasks 任务列表(账本投影 + Manager 运行态合并视图)。
// P2-04:有序收尾定的终态(stopped/completed,detail 带 settled_by=
// task_gate 整体超时 / closure_prompt 收口提醒结束,P4-18)标 settled=
// true——面板与操作者熔断区分呈现(tasks 投影 detail 自 P2-04 起可靠
// 留存,直读即可)。
// P2-06e:受阻徽标(blocked=true)由"图上存在 blocked 步骤"
// 派生(持久真值;非运行态,任务仍在 started 等待恢复)。P4-20:受阻
// 时主状态单枚「受阻」(前端 taskStatusBadge,受阻优先于运行中),
// 阻因留账本不上界面,接口不再透出原因。
// P2-06h:完整版各列数据(描述/目标/分类/置顶/目标进度/运行中单位数,
// 见 taskDigest);墓碑任务过滤(历史仍在账本,列表不呈现);排序
// 置顶在前(置顶动作先后,次之创建序)。
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type taskView struct {
		ID            string             `json:"id"`
		Title         string             `json:"title"`
		Description   string             `json:"description,omitempty"` // 描述(created 事件锚点)
		Targets       []string           `json:"targets"`               // 目标(种子资产标识)
		Category      string             `json:"category,omitempty"`    // 分类 ID
		Pinned        bool               `json:"pinned"`                // 置顶
		Status        string             `json:"status"`                // Manager 运行态(进程内真值)
		Settled       bool               `json:"settled"`               // 终态由有序收尾所定(非操作者熔断)
		SettledBy     string             `json:"settled_by,omitempty"`  // 结束方式锚点(task_gate/closure_prompt;P4-25 审核迭代:显示层译为整体超时/收口结束)
		Blocked       bool               `json:"blocked"`               // 受阻(普遍故障:全部模型熔断冷却中,等恢复;P4-27 ② 收紧)
		BlockedSteps  int                `json:"blocked_steps"`         // blocked 步骤数(全部归因;冬眠悬停与任务行计数用,P4-27 ②④)
		GoalsAchieved int                `json:"goals_achieved"`        // 已达成目标数
		GoalsTotal    int                `json:"goals_total"`           // 目标总数
		RunningUnits  int                `json:"running_units"`         // 运行中虫群单位数(图上 running 步骤)
		PendingAppr   int                `json:"pending_approvals"`     // 待审批笔数(P4-23:任务行与 RoE 标签红点计数,审批门现读)
		Closure       *closurePromptView `json:"closure,omitempty"`     // 待决收口提醒(P4-18,仅运行中任务)
		ClosureConfig *closureConfigView `json:"closure_config"`        // 收口配置生效值(审核迭代 1,常呈)
	}
	out := make([]taskView, 0, len(rows))
	for _, row := range rows {
		if row.Deleted {
			continue // 墓碑任务:历史在账本,列表不呈现
		}
		v := taskView{
			ID: row.ID, Title: row.Title, Status: row.Status,
			Category: row.Category, Pinned: row.Pinned,
		}
		d := s.digestOf(r.Context(), row.ID)
		v.Description, v.Targets = d.Description, d.Targets
		v.GoalsAchieved, v.GoalsTotal, v.RunningUnits = d.GoalsAchieved, d.GoalsTotal, d.RunningUnits
		if by, _ := row.Detail["settled_by"].(string); by == rings.SettleByTaskGate || by == rings.SettleByClosurePrompt {
			v.Settled = true
			v.SettledBy = by
		}
		if t := s.Manager.Task(row.ID); t != nil {
			v.Status = t.Status()
		}
		if v.Status != "stopped" && v.Status != "completed" {
			v.Blocked = s.blockedOf(r.Context(), v.Status)
			v.BlockedSteps = s.blockedStepsOf(r.Context(), row.ID)
		}
		v.PendingAppr = len(s.Gate.Pending(row.ID))
		v.Closure = s.closureViewOf(r, row.ID, v.Status)
		v.ClosureConfig = s.closureConfigViewOf(r, row.ID)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTaskCreate 建任务 + 资产种子表单(P1-09"资产同步"v1 形态):
// 12 类资产随建任务一批入图,可选 parent 指向表单内另一资产的标识,
// 落成 sub_of 层级边(资产树的骨架)。可选整体超时(P2-04 任务级总闸,
// 秒,0/缺省=不限)与重锚定间隔(P2-05 纠偏防线③,步,0/缺省=Agent 页
// 全局默认,08d 前为固定 10)随 created 事件透出。P2-06h:描述随 created detail 入账(事件流锚点,
// 不受后续生命周期事件覆盖投影 detail 影响);分类在种子之后经
// task.meta 事件指派。输入校验全部前置(CreateTask 之前),坏请求
// 零残留;资产批单独一事务落账,成功后唤醒主宰。
func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID               string         `json:"id"`
		Title            string         `json:"title"`
		Description      string         `json:"description"` // 描述(可选)
		Category         string         `json:"category"`    // 分类 ID(可选,P2-06h)
		RoE              *roeBody       `json:"roe"`
		Assets           []assetIn      `json:"assets"`
		Goals            []string       `json:"goals"`              // 种子目标(可选,P2-08f 提取确认后随建任务落库)
		TimeoutSeconds   int            `json:"timeout_seconds"`    // 整体超时(秒);<=0 = 不限
		AnchorEverySteps int            `json:"anchor_every_steps"` // 重锚定间隔(步);<=0 = Agent 页全局默认
		Closure          *closureBodyIn `json:"closure"`            // 收口提醒任务级配置(审核迭代 1;缺省 = 开 + 用全局默认)
	}
	if !readJSON(w, r, &req) {
		return
	}
	// P4-16:任务名可选(空 = 列表以任务 ID 呈现);目标与资产必填
	//(无目标的永动任务与无资产的空转任务在建单口拒住,人话提示)。
	title := strings.TrimSpace(req.Title)
	if req.TimeoutSeconds < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("整体超时不能为负"))
		return
	}
	if req.AnchorEverySteps < 0 || req.AnchorEverySteps > 1000 {
		writeErr(w, http.StatusBadRequest, errors.New("重锚定间隔须在 1~1000 步之间(留空取 Agent 页全局默认)"))
		return
	}
	if err := req.Closure.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	category := strings.TrimSpace(req.Category)
	if category != "" {
		if ok, err := s.categoryExists(r.Context(), category); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		} else if !ok {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("分类 %s 不存在", category))
			return
		}
	}
	seed, err := buildAssetSeed(req.Assets)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(seed.titleToID) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("资产不能为空:至少填一个域名 / IP / 网址"))
		return
	}
	goals := make([]string, 0, len(req.Goals))
	for _, g := range req.Goals {
		if g = strings.TrimSpace(g); g != "" {
			if len([]rune(g)) > 200 {
				writeErr(w, http.StatusBadRequest, errors.New("目标标题过长(上限 200 字)"))
				return
			}
			goals = append(goals, g)
		}
	}
	if len(goals) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("目标不能为空:写明这个任务要拿到什么,如:拿下管理权限"))
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = shortID("t")
	}
	roe := rings.RoeUpdated{}
	if req.RoE != nil {
		roe = req.RoE.toRings()
		// 提取确认的约束:补齐缺省规则 ID + 范围引用(资产标识)映射为
		// 种子资产 ID(P2-08f:提取的约束以表单内资产圈范围,建任务时
		// 一并解析落库,之后可在 RoE 面板改)。
		roe.IntentRules = normalizeIntentRules(roe.IntentRules, seed.titleToID)
	}
	var detail map[string]any
	if req.TimeoutSeconds > 0 {
		detail = map[string]any{"timeout_seconds": req.TimeoutSeconds}
	}
	if req.AnchorEverySteps > 0 {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["anchor_every_steps"] = req.AnchorEverySteps
	}
	if desc := strings.TrimSpace(req.Description); desc != "" {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["description"] = desc
	}
	// 收口提醒任务级初始配置(审核迭代 1):只存显式项,缺省组不落键
	//(任务级无配置 = 开 + 用全局默认)。
	if cd := req.Closure.detailOf(); cd != nil {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["closure"] = cd
	}
	if _, err := s.Manager.CreateTask(r.Context(), id, title, roe, detail); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, g := range goals {
		if err := overmind.SeedGoal(r.Context(), s.Store, id, shortID("g"), g, map[string]any{}); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("目标落库: %w", err))
			return
		}
	}
	if len(seed.events) > 0 {
		if _, err := s.Store.Append(r.Context(), id, seed.events...); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("资产种子写入: %w", err))
			return
		}
	}
	// 种子资产或种子目标是唤醒事件方(P1-05 约定):主宰据此开始拆解。
	if len(seed.events) > 0 || len(goals) > 0 {
		s.wake(id)
	}
	if category != "" {
		if _, err := s.Store.Append(r.Context(), id, rings.Event{
			Kind: rings.EventTaskMeta, Actor: "creator",
			Payload: rings.TaskMeta{Category: &category, Reason: "建任务时指派分类"},
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("分类指引写入: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// assetIn 种子表单单行:类 + 标识 + 可选父标识(表单内另一行的标识)。
type assetIn struct {
	Class  string `json:"class"`
	Title  string `json:"title"`
	Parent string `json:"parent,omitempty"`
}

// assetClassWords 12 类资产词汇表(引用 rings 常量,不漂移);
// 空类由地基层兜底 other,但表单是受限输入,显式给出才合法。
var assetClassWords = map[rings.AssetClass]bool{
	rings.AssetDomain: true, rings.AssetSubdomain: true, rings.AssetHost: true,
	rings.AssetService: true, rings.AssetAPI: true, rings.AssetOrganization: true,
	rings.AssetMiniProgram: true, rings.AssetApp: true, rings.AssetWechatAccount: true,
	rings.AssetPerson: true, rings.AssetLeakInfo: true, rings.AssetOther: true,
}

// assetSeed 资产种子的事件批:node.added × N + edge.added(sub_of) × M,
// 同批一事务(节点先于边,层级边端点在同事务内可见)。titleToID 供
// 任务级拦截规则的范围引用解析(P2-08f 提取确认的约束以表单内资产
// 标识圈范围,建任务时映射为种子资产 ID)。
type assetSeed struct {
	events    []rings.Event
	edgeCount int
	titleToID map[string]string
}

// normalizeIntentRules 建任务随带的任务级拦截规则(P2-08f 提取确认的
// 约束):补齐缺省 ID(ir-<rand>,与 RoE 逐条端点同段);范围引用
// (资产标识)在种子表单内的映射为种子 ID,表单外的引用丢弃(范围
// 退空 = 全部资产,不误拦)。
func normalizeIntentRules(rules []rings.IntentRule, titleToID map[string]string) []rings.IntentRule {
	if len(rules) == 0 {
		return rules
	}
	seen := map[string]bool{}
	out := make([]rings.IntentRule, 0, len(rules))
	for _, r := range rules {
		if r.ID == "" {
			r.ID = shortID("ir")
		}
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if len(r.AssetIDs) > 0 && len(titleToID) > 0 {
			resolved := make([]string, 0, len(r.AssetIDs))
			for _, ref := range r.AssetIDs {
				if id, ok := titleToID[ref]; ok {
					resolved = append(resolved, id)
				}
			}
			r.AssetIDs = resolved
		}
		out = append(out, r)
	}
	return out
}

// buildAssetSeed 校验并编译种子表单:标识非空且表单内唯一;类必须是 12
// 类词汇之一;parent 须指向表单内另一资产的标识(不自引、不成环——
// 层级边成环会让资产树无法渲染)。资产 node_id 面板侧生成(a-*)。
func buildAssetSeed(assets []assetIn) (*assetSeed, error) {
	seed := &assetSeed{titleToID: make(map[string]string, len(assets))}
	titleToID := seed.titleToID
	type link struct{ id, parentTitle string }
	var links []link
	for i, a := range assets {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			return nil, fmt.Errorf("第 %d 行资产的标识不能为空", i+1)
		}
		if _, dup := titleToID[title]; dup {
			return nil, fmt.Errorf("资产标识 %q 在表单内重复(父引用按标识匹配,须唯一)", title)
		}
		class := rings.AssetClass(strings.TrimSpace(a.Class))
		if !assetClassWords[class] {
			return nil, fmt.Errorf("资产 %q 的类 %q 不在 12 类词汇表内", title, a.Class)
		}
		id := shortID("a")
		titleToID[title] = id
		seed.events = append(seed.events, rings.Event{
			Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
				Title: title, Status: "active", AssetClass: class,
				Detail: map[string]any{"source": rings.AssetSourceSeed}},
		})
		if p := strings.TrimSpace(a.Parent); p != "" {
			links = append(links, link{id: id, parentTitle: p})
		}
	}
	for _, lk := range links {
		if _, ok := titleToID[lk.parentTitle]; !ok {
			return nil, fmt.Errorf("资产的父标识 %q 不在表单内(父子层级只能在种子表单内表达)", lk.parentTitle)
		}
	}
	// 环检测:沿 parent 链走,步数超过资产数必回到访问过的节点。
	parentOf := make(map[string]string, len(links)) // id → parent id
	for _, lk := range links {
		parentOf[lk.id] = titleToID[lk.parentTitle]
	}
	for start := range parentOf {
		seen := map[string]bool{start: true}
		cur := start
		for {
			next, ok := parentOf[cur]
			if !ok {
				break
			}
			if seen[next] {
				return nil, errors.New("资产的父子层级成环(父链绕回自身)")
			}
			seen[next] = true
			cur = next
		}
	}
	for _, lk := range links {
		seed.events = append(seed.events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "creator",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeSubOf,
				FromNode: lk.id, ToNode: titleToID[lk.parentTitle]},
		})
		seed.edgeCount++
	}
	return seed, nil
}

// ---- 任务熔断(P1-09 面板成品) ----

// handleTaskStop 熔断按钮:Manager.StopBy 保证"先追加控制事件、再
// cancel 任务 ctx";operator 恒为造物主(creator),理由随请求带上——
// 账本可回答"谁在何时熔断了哪个任务"。幂等(已停任务再按仍 200)。
func (s *Server) handleTaskStop(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	detail := map[string]any{"operator": "creator"}
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		detail["reason"] = reason
	}
	if err := s.Manager.StopBy(taskID, detail); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": taskID, "status": "stopped"})
}

// ---- 资产树(P1-09,读投影) ----

// detailStr 节点 detail 的字符串字段直读(缺省空;出生血缘等短字段用)。
func detailStr(detail map[string]any, key string) string {
	s, _ := detail[key].(string)
	return s
}

// assetTreeView 资产图最小投影的读模型:节点(覆盖标志 + 事实/假设
// 计数)与 sub_of 层级边(from=子、to=父),前端据此渲染树。
type assetTreeView struct {
	Assets []assetNodeJSON `json:"assets"`
	Tree   []edgeJSON      `json:"hierarchy"`
}

type assetNodeJSON struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Class           string `json:"class"`
	Status          string `json:"status"`
	Covered         bool   `json:"covered"`
	FactCount       int    `json:"fact_count"`
	HypothesisCount int    `json:"hypothesis_count"`
	Source          string `json:"source"`   // 来源三态:seed/manual/discovered(P4-05)
	Excluded        bool   `json:"excluded"` // 已从授权范围摘除(P4-05;范围外)
	CreatedRev      int64  `json:"created_rev"`
	// 发现型资产的出生血缘(P4-12):发现单位/发现步骤/发现事实(收割入图时
	// 冻结进 detail;非发现资产零值,如实断链)。
	BirthUnit string `json:"birth_unit,omitempty"`
	BirthStep string `json:"birth_step,omitempty"`
	BirthFact string `json:"birth_fact,omitempty"`
}

type edgeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) handleAssetTree(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ag, err := s.Store.AssetGraph(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var excluded map[string]bool
	if roe, err := s.Store.CurrentRoE(r.Context(), taskID); err == nil {
		excluded = roe.ExcludedAssetSet()
	}
	v := assetTreeView{Assets: []assetNodeJSON{}, Tree: []edgeJSON{}}
	for _, a := range ag.Assets {
		v.Assets = append(v.Assets, assetNodeJSON{
			ID: a.ID, Title: a.Title, Class: string(a.AssetClass), Status: a.Status,
			Covered: a.Covered, FactCount: a.FactCount, HypothesisCount: a.HypothesisCount,
			Source: a.Source, Excluded: excluded[a.ID],
			CreatedRev: a.CreatedRev,
		})
	}
	for _, e := range ag.Hierarchy {
		v.Tree = append(v.Tree, edgeJSON{From: e.From, To: e.To})
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- 手动新增资产(P4-05,问题清单 #10) ----

// handleAssetCreate 面板"新增资产":表单同建任务种子表单(12 类选型 +
// 标识 + 可选父标识,可批量)。入账 node.added(Actor=造物主,来源
// detail.source=manual)+ sub_of 边,同批一事务;事件即审计(谁、何时、
// 加了什么、挂在哪)。资产默认纳入授权范围(图上资产即授权域,无需
// 另写纳入事件;不想测的在 RoE 摘除)。入账即唤醒主宰(P4-04 语义:
// 新资产入图,主宰下轮读到自行决定派探测意图)。
func (s *Server) handleAssetCreate(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Assets []assetIn `json:"assets"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	events, err := s.buildManualAssets(r.Context(), taskID, req.Assets)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.Store.Append(r.Context(), taskID, events...); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("手动资产写入: %w", err))
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]any{"added": len(req.Assets), "edges": len(events) - len(req.Assets)})
}

// buildManualAssets 校验并编译手动资产批:标识非空、批内唯一、不与图上
// 既有资产同标识;类在 12 类词表;父标识按 本批标识 → 图上唯一同标识
// 资产 → 图上资产 ID 三序解析;父子层级不得成环(在既有 sub_of 之上
// 叠加本批边做链检测)。返回事件批(节点先于边,同批一事务)。
func (s *Server) buildManualAssets(ctx context.Context, taskID string, assets []assetIn) ([]rings.Event, error) {
	if len(assets) == 0 {
		return nil, errors.New("资产清单为空(至少一行)")
	}
	ag, err := s.Store.AssetGraph(ctx, taskID)
	if err != nil {
		return nil, err
	}
	titleIDs := map[string][]string{} // 图上标识 → 资产 ID(可重名)
	byID := map[string]bool{}
	for _, a := range ag.Assets {
		titleIDs[a.Title] = append(titleIDs[a.Title], a.ID)
		byID[a.ID] = true
	}
	batchIDs := map[string]string{} // 本批标识 → 新 ID
	type link struct{ id, parentRef string }
	var links []link
	events := make([]rings.Event, 0, len(assets))
	for i, a := range assets {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			return nil, fmt.Errorf("第 %d 行资产的标识不能为空", i+1)
		}
		if _, dup := batchIDs[title]; dup {
			return nil, fmt.Errorf("资产标识 %q 在本次提交内重复", title)
		}
		if len(titleIDs[title]) > 0 {
			return nil, fmt.Errorf("图上已有同标识资产 %q(重复入图请先核对既有资产)", title)
		}
		class := rings.AssetClass(strings.TrimSpace(a.Class))
		if !assetClassWords[class] {
			return nil, fmt.Errorf("资产 %q 的类 %q 不在 12 类词汇表内", title, a.Class)
		}
		id := shortID("a")
		batchIDs[title] = id
		events = append(events, rings.Event{
			Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
				Title: title, Status: "active", AssetClass: class,
				Detail: map[string]any{"source": rings.AssetSourceManual}},
		})
		if p := strings.TrimSpace(a.Parent); p != "" {
			links = append(links, link{id: id, parentRef: p})
		}
	}
	// 父标识解析:本批 → 图上唯一同标识 → 图上资产 ID。
	resolveParent := func(ref string) (string, error) {
		if id, ok := batchIDs[ref]; ok {
			return id, nil
		}
		if ids := titleIDs[ref]; len(ids) == 1 {
			return ids[0], nil
		} else if len(ids) > 1 {
			return "", fmt.Errorf("父标识 %q 在图上对应多个资产,请改用资产 ID 指定", ref)
		}
		if byID[ref] {
			return ref, nil
		}
		return "", fmt.Errorf("父标识 %q 不在图上且不在本次提交内", ref)
	}
	// 既有 sub_of 父链 + 本批新边,叠加后做环检测。
	parentOf := map[string]string{}
	for _, e := range ag.Hierarchy {
		parentOf[e.From] = e.To
	}
	for _, lk := range links {
		pid, err := resolveParent(lk.parentRef)
		if err != nil {
			return nil, err
		}
		if pid == lk.id {
			return nil, errors.New("资产的父标识不能指向自身")
		}
		parentOf[lk.id] = pid
	}
	total := len(parentOf)
	for start := range parentOf {
		seen := map[string]bool{start: true}
		cur := start
		for {
			next, ok := parentOf[cur]
			if !ok {
				break
			}
			if seen[next] {
				return nil, errors.New("资产的父子层级成环(父链绕回自身)")
			}
			seen[next] = true
			if len(seen) > total {
				break
			}
			cur = next
		}
	}
	for _, lk := range links {
		pid, err := resolveParent(lk.parentRef)
		if err != nil {
			return nil, err
		}
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "creator",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeSubOf, FromNode: lk.id, ToNode: pid},
		})
	}
	return events, nil
}

// handleAssetDetail 资产详情(8.7 Sheet 抽屉数据,P1-09b):节点投影 +
// about 边反查挂在该资产上的事实/假设/发现。读事务快照由各查询自理。
func (s *Server) handleAssetDetail(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	nodeID := r.PathValue("nid")
	n, err := s.Store.Node(r.Context(), taskID, nodeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("节点 %s 不存在", nodeID))
		return
	}
	if n.Type != rings.NodeAsset {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("节点 %s 不是资产(%s)", nodeID, n.Type))
		return
	}
	abouts, err := s.Store.Edges(r.Context(), taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	anchored := make(map[string]bool, len(abouts))
	for _, e := range abouts {
		if e.To == nodeID {
			anchored[e.From] = true
		}
	}

	type itemJSON struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Status     string `json:"status"`
		CreatedRev int64  `json:"created_rev"`
	}
	pick := func(nt rings.NodeType) []itemJSON {
		nodes, err := s.Store.NodesByType(r.Context(), taskID, nt, "")
		if err != nil {
			return []itemJSON{}
		}
		out := []itemJSON{}
		for _, x := range nodes {
			if anchored[x.ID] {
				out = append(out, itemJSON{ID: x.ID, Title: x.Title, Status: x.Status, CreatedRev: x.CreatedRev})
			}
		}
		return out
	}
	v := struct {
		Asset      assetNodeJSON `json:"asset"`
		Facts      []itemJSON    `json:"facts"`
		Hypotheses []itemJSON    `json:"hypotheses"`
		Claims     []itemJSON    `json:"claims"`
	}{
		Asset: assetNodeJSON{
			ID: n.ID, Title: n.Title, Class: string(n.AssetClass), Status: n.Status,
			Source:     rings.AssetSourceOf(n.Detail),
			CreatedRev: n.CreatedRev,
			BirthUnit:  detailStr(n.Detail, "unit_id"),
			BirthStep:  detailStr(n.Detail, "step_id"),
			BirthFact:  detailStr(n.Detail, "discovered_fact"),
		},
		Facts:      pick(rings.NodeFact),
		Hypotheses: pick(rings.NodeHypothesis),
		Claims:     pick(rings.NodeTestClaim),
	}
	// 覆盖标志与计数以 AssetGraph 投影为准(节点 detail 不承载派生值);
	// 范围外(P4-05)与 RoE 摘除清单同源。
	if ag, err := s.Store.AssetGraph(r.Context(), taskID); err == nil {
		for _, a := range ag.Assets {
			if a.ID == nodeID {
				v.Asset.Covered = a.Covered
				v.Asset.FactCount = a.FactCount
				v.Asset.HypothesisCount = a.HypothesisCount
				v.Asset.Source = a.Source
				break
			}
		}
	}
	if roe, err := s.Store.CurrentRoE(r.Context(), taskID); err == nil {
		v.Asset.Excluded = roe.ExcludedAssetSet()[nodeID]
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- 执行预算生效视图(Agent 配置栏 params.budget,P4-01 起 0=不限) ----

// BudgetView 执行预算的生效视图:三项生效值 + 被全局覆盖项的轻量审计
// (改动者/时刻;未覆盖为空)。max_turns / run_seconds 为 0 = 不限
// (默认;P4-01 雇佣关系——领了干完为止,想限随时配回)。
type BudgetView struct {
	MaxTurns    int `json:"max_turns"`    // 生效轮次上限(0 = 不限;装配默认+全局覆盖)
	RunSeconds  int `json:"run_seconds"`  // 生效时长上限(秒;0 = 不限)
	SettleTurns int `json:"settle_turns"` // 收尾轮数(只读默认,R16 Settlement)
	// 覆盖审计(轻量):键被全局覆盖时带改动者与时刻,未覆盖为空。
	MaxTurnsBy          string `json:"max_turns_by,omitempty"`
	MaxTurnsUpdatedAt   string `json:"max_turns_updated_at,omitempty"`
	RunSecondsBy        string `json:"run_seconds_by,omitempty"`
	RunSecondsUpdatedAt string `json:"run_seconds_updated_at,omitempty"`
}

// effectiveBudget 计算生效执行预算:装配默认 + 全局配置覆盖(键值表,
// 与虫群 Claim 同一读法/fail-safe 语义),并把覆盖审计(by/updated_at)
// 一并透出。读失败退装配默认(设置页如实呈现默认值,不因配置读故障
// 拒绝整个面板)。
func (s *Server) effectiveBudget(ctx context.Context) BudgetView {
	var b BudgetView
	def := swarm.DefaultBudget()
	b.MaxTurns, b.RunSeconds, b.SettleTurns = def.MaxTurns, def.RunSeconds, def.WrapupMaxTurns
	audit := func(key string) (val int, by, at string, ok bool) {
		e, found, err := s.Store.Setting(ctx, key)
		if err != nil || !found {
			return 0, "", "", false
		}
		n, perr := strconv.Atoi(strings.TrimSpace(e.Value))
		if perr != nil || n <= 0 {
			return 0, "", "", false
		}
		return n, e.UpdatedBy, e.UpdatedAt.Local().Format("2006-01-02 15:04:05"), true
	}
	if n, by, at, ok := audit(swarm.SettingKeyMaxTurns); ok {
		b.MaxTurns, b.MaxTurnsBy, b.MaxTurnsUpdatedAt = n, by, at
	}
	if n, by, at, ok := audit(swarm.SettingKeyRunSeconds); ok {
		b.RunSeconds, b.RunSecondsBy, b.RunSecondsUpdatedAt = n, by, at
	}
	return b
}

// handleConfig 设置页读 API:LLM 注入状态、虫群并发上限(装配常量)。
// 执行预算读写在 Agent 配置栏(/api/agents)。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	v := s.Config
	v.Search = s.effectiveSearch(r.Context())
	v.UA = s.effectiveUA(r.Context())
	writeJSON(w, http.StatusOK, v)
}

// 执行预算取值范围(受控校验,/api/agents/params 消费):轮次 0–1000,
// 时长 0–86400 秒(一天);0 = 不限(删键回默认,P4-01)。
const (
	budgetMaxTurnsLimit   = 1000
	budgetRunSecondsLimit = 86400
)

// ---- 搜索配置(P3-03,系统设置「搜索配置」卡) ----

// searchBody 搜索配置写体:key 三态(未提交=不改、空串=清除、非空=
// 设置);backend 缺省按默认 ddgs;proxy 空串 = 清除(直连)。
type searchBody struct {
	Backend   string  `json:"backend"`
	BraveKey  *string `json:"brave_key"`
	TavilyKey *string `json:"tavily_key"`
	Proxy     *string `json:"proxy"`
}

// handleSearchUpdate 搜索配置写 API:写设置表(search.* 键值),对
// 下一次 web_search 调用即生效(工具每次现读配置)。key 明文只进
// 服务端存储,响应恒为打码视图(第七章 §5:web 端永不回显明文)。
func (s *Server) handleSearchUpdate(w http.ResponseWriter, r *http.Request) {
	var body searchBody
	if !readJSON(w, r, &body) {
		return
	}
	// 后端显式校验(不静默归一):拼错(如 "brav")如实 400,防误存默认。
	switch strings.ToLower(strings.TrimSpace(body.Backend)) {
	case websearch.BackendDDGS, websearch.BackendBrave, websearch.BackendTavily:
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("后端须为 ddgs/brave/tavily 之一"))
		return
	}
	// 缺 key 的后端照存(切后端与配 key 分两步是合法操作);工具侧
	// 用时如实报"需要 key"。代理校验先行,坏地址不入库。
	cfg := websearch.Load(r.Context(), s.Store)
	cfg.Backend = websearch.NormalizeBackend(body.Backend)
	if body.BraveKey != nil {
		cfg.BraveKey = strings.TrimSpace(*body.BraveKey)
	}
	if body.TavilyKey != nil {
		cfg.TavilyKey = strings.TrimSpace(*body.TavilyKey)
	}
	if body.Proxy != nil {
		cfg.Proxy = strings.TrimSpace(*body.Proxy)
		if err := websearch.ValidateProxy(cfg.Proxy); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := websearch.Save(r.Context(), s.Store, cfg, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.effectiveSearch(r.Context()))
}

// ---- RoE 面板(操作约束区) ----

// roeBody RoE 提交体(面板表单/JSON 双用)。IntentRules 是任务级拦截
// 规则(P2-08e):随快照整体提交,增删 = 提交新快照。ExcludedAssets 是
// 授权范围摘除清单(P4-05):随快照整体提交;面板专用端点 /roe/assets
// 改它,其余快照提交须透传当前值(否则清空)。
type roeBody struct {
	AllowedTools      []string       `json:"allowed_tools"`
	DeniedTools       []string       `json:"denied_tools"`
	ApprovalThreshold string         `json:"approval_threshold"` // 空 = 默认宽(仅增删改弹门)
	IntentRules       []intentRuleIn `json:"intent_rules"`
	ExcludedAssets    []string       `json:"excluded_assets"` // 授权范围摘除清单(P4-05;省略 = 保持现值)
	Note              string         `json:"note"`
}

func (b *roeBody) toRings() rings.RoeUpdated {
	rules := make([]rings.IntentRule, 0, len(b.IntentRules))
	for _, r := range b.IntentRules {
		rules = append(rules, r.toRings())
	}
	return rings.RoeUpdated{
		AllowedTools:      b.AllowedTools,
		DeniedTools:       b.DeniedTools,
		ApprovalThreshold: rings.ActionLevel(b.ApprovalThreshold),
		IntentRules:       rules,
		ExcludedAssets:    b.ExcludedAssets,
		Note:              b.Note,
	}
}

// roeView RoE 当前值(显式给出生效的审批门槛,面板不必懂默认值)。
type roeView struct {
	AllowedTools      []string        `json:"allowed_tools"`
	DeniedTools       []string        `json:"denied_tools"`
	ApprovalThreshold string          `json:"approval_threshold"` // 已归一的生效审批门槛
	IntentRules       []intentRuleOut `json:"intent_rules"`       // 任务级拦截规则(带资产标题与已用次数)
	ExcludedAssets    []string        `json:"excluded_assets"`    // 授权范围摘除清单(P4-05)
	Note              string          `json:"note,omitempty"`
	UpdatedRev        int64           `json:"updated_rev"`
}

func (s *Server) currentRoE(taskID string) (roeView, error) {
	st, err := s.Store.CurrentRoE(context.Background(), taskID)
	if err != nil {
		return roeView{}, err
	}
	return roeView{
		AllowedTools:      st.AllowedTools,
		DeniedTools:       st.DeniedTools,
		ApprovalThreshold: string(swarm.NormalizeApprovalThreshold(st.ApprovalThreshold)),
		IntentRules:       s.intentRuleViews(taskID, st.IntentRules),
		ExcludedAssets:    st.ExcludedAssets,
		Note:              st.Note,
		UpdatedRev:        st.UpdatedRev,
	}, nil
}

func (s *Server) handleRoEGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRoEUpdate RoE 变更(R7:面板增删改全部记账,改动下一轮生效——
// 在飞单位不做硬中断,后续领取读到新边界;R14 复检兜住领而未飞的单位)。
// 提交的是整体快照(增删改 = 提交新快照),审计走 roe.updated 事件流。
// 任务级拦截规则随快照提交(P2-08e):词表与结构校验在 rings 投影入口,
// 资产存在性在此查图。摘除清单(P4-05)省略时保持现值(专用端点管理,
// 其余快照提交不误清);显式提交时同样查图校验。
func (s *Server) handleRoEUpdate(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var body roeBody
	if !readJSON(w, r, &body) {
		return
	}
	roe := body.toRings()
	roe.Note = strings.TrimSpace(roe.Note)
	if body.ExcludedAssets == nil {
		if st, err := s.Store.CurrentRoE(r.Context(), taskID); err == nil {
			roe.ExcludedAssets = st.ExcludedAssets
		}
	}
	if err := s.validateAssetIDs(taskID, roe.ExcludedAssets); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.validateIntentRuleAssets(taskID, roe.IntentRules); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator", Payload: roe,
	}); err != nil {
		// 审批门槛/规则词表非法等受控入口校验失败原样透出。
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.wake(taskID)
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- RoE 面板(目标管理区) ----

func (s *Server) handleGoalsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	goals, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeGoal, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type goalView struct {
		ID         string          `json:"id"`
		Title      string          `json:"title"`
		Status     string          `json:"status"`
		Predicate  json.RawMessage `json:"predicate,omitempty"` // 创建时冻结的完成谓词
		CreatedRev int64           `json:"created_rev"`
	}
	out := make([]goalView, 0, len(goals))
	for _, g := range goals {
		v := goalView{ID: g.ID, Title: g.Title, Status: g.Status, CreatedRev: g.CreatedRev}
		if p, ok := g.Detail["predicate"]; ok && p != nil {
			if raw, err := json.Marshal(p); err == nil {
				v.Predicate = raw
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGoalCreate 新增目标(第四章 §4:目标 = Goal,新增通知主宰并可
// 复活任务——记账后唤醒)。谓词可选,交 overmind.SeedGoal 的入口校验
// (非法受限结构创建即拒,P1-06 契约①)。
func (s *Server) handleGoalCreate(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		ID        string          `json:"id"`
		Title     string          `json:"title"`
		Predicate json.RawMessage `json:"predicate"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("目标标题不能为空"))
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = shortID("g")
	}
	detail := map[string]any{}
	if len(req.Predicate) > 0 && string(req.Predicate) != "null" {
		var p map[string]any
		if err := json.Unmarshal(req.Predicate, &p); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("完成谓词必须是 JSON 对象: %w", err))
			return
		}
		detail["predicate"] = p
	}
	if err := overmind.SeedGoal(r.Context(), s.Store, taskID, id, req.Title, detail); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handleGoalAchieve 人工标记目标达成(P3-14b ④):无谓词的目标机器
// 不可判完成(ErrNoPredicate 是合法状态),造物主看到证据后手动收场。
// 依据必填,状态迁移与依据同笔入账(actor=creator,可审计);随后唤醒
// 主宰——全部目标达成时按既有完成路径收场(裁决与人工标记在账本事件
// 的 actor 上天然可辨)。
func (s *Server) handleGoalAchieve(w http.ResponseWriter, r *http.Request) {
	taskID, goalID := r.PathValue("id"), r.PathValue("gid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeErr(w, http.StatusBadRequest, errors.New("标记达成必须写明依据(看到证据后的收场理由)"))
		return
	}
	goal, err := s.Store.Node(r.Context(), taskID, goalID)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("目标 %s 不存在", goalID))
		return
	}
	if goal.Type != rings.NodeGoal {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("节点 %s 不是目标", goalID))
		return
	}
	if goal.Status != overmind.GoalStatusActive {
		writeErr(w, http.StatusConflict, errors.New("目标已达成,无需再标记"))
		return
	}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind:  rings.EventNodeStateChanged,
		Actor: "creator",
		Payload: rings.NodeStateChanged{
			NodeID: goalID, Status: overmind.GoalStatusAchieved, Note: reason,
		},
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("标记达成写入: %w", err))
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]string{"id": goalID, "status": overmind.GoalStatusAchieved})
}

// ---- 发现列表(P1-08,TestClaim 读投影) ----

// claimView 发现列表条目:节点投影 + 类型化边(资产锚点/血缘)+ 状态机
// 审计流补足(搁置理由、验证者)。待确认条目的 PendingReason 是 R4 的
// 面板可见面;发现者/验证者并排呈现,R12(发现者不得自证)审计可查。
type claimView struct {
	ID            string   `json:"id"`
	Status        string   `json:"status"`           // candidate|testing|awaiting_round|confirmed|pending|fixed
	Statement     string   `json:"statement"`        // 存在性声明
	Impact        string   `json:"impact,omitempty"` // 影响面声明(与存在性分开)
	Vulnclass     string   `json:"vulnclass,omitempty"`
	Severity      string   `json:"severity,omitempty"`        // 收割定稿严重级别(=末轮反证影响面定级,P2-02)
	ReproSteps    int      `json:"repro_steps"`               // 定稿复现步骤数(P2-13 列表折叠入口;0=无定稿)
	AssetIDs      []string `json:"asset_ids,omitempty"`       // 资产锚点(about 边)
	Discoverer    string   `json:"discoverer_unit,omitempty"` // 发现单位
	DiscoverStep  string   `json:"discover_step,omitempty"`   // 发现步骤(derived_from 边)
	FalsifyStep   string   `json:"falsify_step,omitempty"`    // 反证步骤
	Verifier      string   `json:"verifier_unit,omitempty"`   // 反证单位(≠发现者,R12)
	PendingReason string   `json:"pending_reason,omitempty"`  // 待确认搁置理由(R4)
	VerdictRev    int64    `json:"verdict_rev,omitempty"`     // 裁决事件 revision
	CreatedRev    int64    `json:"created_rev"`
	// 复测面(P3-06):最新复测结论与轮次、当前是否有活复测会话、状态可否
	// 复测(已确认/待确认/已修复)。
	RetestVerdict string `json:"retest_verdict,omitempty"` // fixed|unfixed|inconclusive
	RetestRound   int    `json:"retest_round,omitempty"`
	RetestAt      string `json:"retest_at,omitempty"`
	RetestRunning bool   `json:"retest_running"`
	Retestable    bool   `json:"retestable"`
	// 重放面(P3-07):最新重放校验结论与轮次、当前是否有活重放、可否
	// 重放(声明有绑定流量证据即可,与锤炼状态无关——重放不迁移状态)。
	ReplayVerdict string `json:"replay_verdict,omitempty"` // reproduced|diverged|unreachable
	ReplayRound   int    `json:"replay_round,omitempty"`
	ReplayAt      string `json:"replay_at,omitempty"`
	ReplayRunning bool   `json:"replay_running"`
	Replayable    bool   `json:"replayable"`
	// Combined 组合声明标记与攻击链环节(P2-06:多条 Fact 合并的攻击链级
	// 发现;血缘经 derived_from 边挂环节 Fact)。
	Combined bool            `json:"combined,omitempty"`
	Chain    []ChainLinkView `json:"chain,omitempty"`
	// 复核标记(P4-30 ③):信息性判断(无直接危害)不纳入报告;最新
	// report_review 记录为准,理由随标记透出。
	ReportExcluded       bool   `json:"report_excluded,omitempty"`
	ReportExcludedReason string `json:"report_excluded_reason,omitempty"`
}

// ChainLinkView 攻击链环节(P2-06):环节引用的已确认 Fact + 角色。
type ChainLinkView struct {
	FactID string `json:"fact_id"`
	Role   string `json:"role,omitempty"`
	Title  string `json:"title,omitempty"` // 环节事实陈述(读侧下钻)
}

func (s *Server) handleClaimsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	claims, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeTestClaim, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	abouts, err := s.Store.Edges(r.Context(), taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	derived, err := s.Store.Edges(r.Context(), taskID, rings.EdgeDerivedFrom)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	evs, err := s.Store.Events(r.Context(), taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 状态机审计流:每声明取最新 falsify_result、verdict 与 repro(定稿
	// 严重级别跟最新收割定稿走——重开再确认后新定稿取代旧定稿);复测
	// 结论取最新(P3-06);重放结论取最新 + 绑定证据在场判定(P3-07,
	// 事件流一次扫描内完成)。
	falsifyLatest := map[string]rings.TestClaimRecord{}
	verdictLatest := map[string]rings.RawEvent{}
	reproLatest := map[string]rings.TestClaimRecord{}
	type retestLatestRow struct {
		rec rings.TestClaimRecord
		at  string
	}
	retestLatest := map[string]retestLatestRow{}
	type replayLatestRow struct {
		rec rings.TestClaimRecord
		at  string
	}
	replayLatest := map[string]replayLatestRow{}
	reviewLatest := map[string]rings.TestClaimRecord{} // 复核标记(P4-30 ③,最新为准)
	hasEvidence := map[string]bool{}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		switch rec.Type {
		case rings.TestClaimRecFalsify:
			falsifyLatest[rec.ClaimID] = rec
		case rings.TestClaimRecVerdict:
			verdictLatest[rec.ClaimID] = ev
		case rings.TestClaimRecRepro:
			reproLatest[rec.ClaimID] = rec
		case rings.TestClaimRecRetest:
			retestLatest[rec.ClaimID] = retestLatestRow{rec: rec,
				at: ev.OccurredAt.Local().Format("2006-01-02 15:04:05")}
		case rings.TestClaimRecReplay:
			replayLatest[rec.ClaimID] = replayLatestRow{rec: rec,
				at: ev.OccurredAt.Local().Format("2006-01-02 15:04:05")}
		case rings.TestClaimRecEvidence:
			if len(rec.Exchanges) > 0 {
				hasEvidence[rec.ClaimID] = true
			}
		case rings.TestClaimRecReportReview:
			reviewLatest[rec.ClaimID] = rec
		}
	}

	out := make([]claimView, 0, len(claims))
	for _, c := range claims {
		v := claimView{
			ID: c.ID, Status: c.Status, Statement: c.Title, CreatedRev: c.CreatedRev,
		}
		v.Impact, _ = c.Detail["impact"].(string)
		v.Vulnclass, _ = c.Detail["vulnclass"].(string)
		v.Discoverer, _ = c.Detail["unit_id"].(string)
		v.DiscoverStep, _ = c.Detail["step_id"].(string)
		v.Combined, _ = c.Detail["combined"].(bool)
		if v.Combined {
			// 组合声明的链序与角色随列表下发(P2-11 年轮图·攻击链透镜
			// 读列表装配,与详情同源 chainLinksOf)。
			v.Chain = s.chainLinksOf(r.Context(), taskID, &c)
		}
		for _, e := range abouts {
			if e.From == c.ID {
				v.AssetIDs = append(v.AssetIDs, e.To)
			}
		}
		if v.DiscoverStep == "" {
			// 组合声明(P2-06):血缘指向环节 Fact,发现步骤如实留空
			//(详情侧以攻击链卡呈现血缘)。
			for _, e := range derived {
				if e.From == c.ID {
					if n, err := s.Store.Node(r.Context(), taskID, e.To); err == nil && n.Type == rings.NodeStep {
						v.DiscoverStep = e.To
						break
					}
				}
			}
		}
		if fr, ok := falsifyLatest[c.ID]; ok {
			v.FalsifyStep, v.Verifier = fr.StepID, fr.UnitID
		}
		if rp, ok := reproLatest[c.ID]; ok {
			v.Severity = rp.Severity
			v.ReproSteps = len(rp.Steps)
		}
		if ev, ok := verdictLatest[c.ID]; ok {
			var rec rings.TestClaimRecord
			if err := json.Unmarshal(ev.Payload, &rec); err == nil {
				v.PendingReason = rec.PendingReason
				if rec.StepID != "" {
					v.FalsifyStep = rec.StepID
				}
				if rec.UnitID != "" {
					v.Verifier = rec.UnitID
				}
			}
			v.VerdictRev = ev.Revision
		}
		v.Retestable = swarm.Retestable(c.Status)
		if rt, ok := retestLatest[c.ID]; ok {
			v.RetestVerdict, v.RetestRound, v.RetestAt = rt.rec.RetestVerdict, rt.rec.Round, rt.at
		}
		v.Replayable = hasEvidence[c.ID]
		if rp, ok := replayLatest[c.ID]; ok {
			v.ReplayVerdict, v.ReplayRound, v.ReplayAt = rp.rec.ReplayVerdict, rp.rec.Round, rp.at
		}
		if rv, ok := reviewLatest[c.ID]; ok {
			v.ReportExcluded, v.ReportExcludedReason = true, rv.Reason
		}
		if exec := s.swarmExec(); exec != nil {
			v.RetestRunning = exec.RetestRunning(taskID, c.ID)
			v.ReplayRunning = exec.ReplayRunning(taskID, c.ID)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 报告(P1-10 预览 + P2-13 格式矩阵/漏洞级/跨任务导出)----
// handleReport 与导出端点见 server_report.go。

// ---- 审批弹门(P2-06b 三级处置 v2) ----

// approvalRecordView 审批记录(approval.record 事件流读侧,倒序最新在先)。
type approvalRecordView struct {
	RequestID   string `json:"request_id"`
	StepID      string `json:"step_id"`
	UnitID      string `json:"unit_id"`
	Type        string `json:"type"` // requested|approved|denied|forced
	ActionLevel string `json:"action_level"`
	HarmClass   string `json:"harm_class,omitempty"`
	Intent      string `json:"intent,omitempty"`
	ToolName    string `json:"tool_name"`
	ToolArgs    string `json:"tool_args,omitempty"`
	Note        string `json:"note,omitempty"` // 弹门上下文(拦截规则命中转人审的原因)
	DenyLine    string `json:"deny_line,omitempty"`
	DecidedBy   string `json:"decided_by,omitempty"`
	Reason      string `json:"reason,omitempty"`
	WaitMillis  int64  `json:"wait_millis,omitempty"`
	OccurredAt  string `json:"occurred_at"`
	// Releasable 该记录可否研判后放行(拒绝线的自动拒绝且尚未放行)。
	Releasable bool `json:"releasable"`
}

// approvalView 待决审批请求(审批弹门的面板核对物)。端点视图与实时
// 推送载荷(P3-08)同源共用——展示与操作同源,推送不另立装配。
type approvalView struct {
	ID          string `json:"id"`
	StepID      string `json:"step_id"`
	UnitID      string `json:"unit_id"`
	Intent      string `json:"intent"`         // 意图声明
	ActionLevel string `json:"action_level"`   // 分级
	ToolName    string `json:"tool_name"`      // 实际工具
	ToolArgs    string `json:"tool_args"`      // 实际参数(人即兜底的核对物)
	Note        string `json:"note,omitempty"` // 弹门上下文(拦截规则命中转人审的原因)
	// HarmNote 危害说明三段式(P4-27 ⑨):按分级+命令特征确定性拼装,
	// 现组装现服务(不落库);说清做什么/目标会怎样/最坏后果。
	HarmNote  string `json:"harm_note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// releasableDenyLines 可研判后放行的拒绝线类别(第四章 §3 v2:拒绝线
// 动作的审批卡片上有放行按钮;面板拒绝是造物主已决策过的,closeout 是
// 任务级中断,均不放行)。
var releasableDenyLines = map[string]bool{
	rings.DenyLineHarm: true, rings.DenyLineRoeScope: true,
	rings.DenyLineLevelInvalid: true, rings.DenyLineNoGate: true,
}

// handleApprovalsList 审批弹门待决请求 + 审批记录(面板轮询)。
func (s *Server) handleApprovalsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	pending := s.Gate.Pending(taskID)
	out := make([]approvalView, 0, len(pending))
	for _, p := range pending {
		out = append(out, approvalView{
			ID: p.ID, StepID: p.StepID, UnitID: p.UnitID, Intent: p.Intent,
			ActionLevel: string(p.ActionLevel), ToolName: p.ToolName,
			ToolArgs:  p.ToolArgs,
			Note:      p.Note,
			HarmNote:  swarm.HarmNote(p.ActionLevel, p.ToolName, json.RawMessage(p.ToolArgs)),
			CreatedAt: p.CreatedAt.Local().Format("2006-01-02 15:04:05"),
		})
	}
	records, err := s.approvalRecords(context.Background(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// P2-08:冬眠中审批单冻结为只读(面板禁用裁决按钮并提示;唤醒恢复)。
	writeJSON(w, http.StatusOK, map[string]any{
		"pending": out, "records": records, "frozen": s.taskHibernated(taskID),
	})
}

// approvalRecords 事件流读侧:审批记录倒序(最新在先,面板近期记录),
// 拒绝线记录标注可放行与否(已放行的 forced 记录在流里可见)。
func (s *Server) approvalRecords(ctx context.Context, taskID string) ([]approvalRecordView, error) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("查审批事件流: %w", err)
	}
	// 先扫一遍放行集合(forced),再组装视图——denied 在 forced 之前落账,
	// 单遍会把已放行的拒绝线记录误标可放行。
	forced := map[string]bool{} // requestID -> 已研判后放行
	type parsedRec struct {
		rec        rings.ApprovalRecord
		occurredAt time.Time
	}
	var parsed []parsedRec
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var rec rings.ApprovalRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			return nil, fmt.Errorf("解码 approval.record: %w", err)
		}
		if rec.Type == rings.ApprovalRecForced {
			forced[rec.RequestID] = true
		}
		parsed = append(parsed, parsedRec{rec: rec, occurredAt: ev.OccurredAt})
	}
	var recs []approvalRecordView
	for _, pr := range parsed {
		rec := pr.rec
		recs = append(recs, approvalRecordView{
			RequestID: rec.RequestID, StepID: rec.StepID, UnitID: rec.UnitID,
			Type: rec.Type, ActionLevel: string(rec.ActionLevel), HarmClass: string(rec.HarmClass),
			Intent: rec.Intent, ToolName: rec.ToolName, ToolArgs: string(rec.ToolArgs),
			Note: rec.Note, DenyLine: rec.DenyLine, DecidedBy: rec.DecidedBy, Reason: rec.Reason,
			WaitMillis: rec.WaitMillis, OccurredAt: pr.occurredAt.Local().Format("2006-01-02 15:04:05"),
			Releasable: rec.Type == rings.ApprovalRecDenied && releasableDenyLines[rec.DenyLine] && !forced[rec.RequestID],
		})
	}
	// 倒序:最新在先。
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	return recs, nil
}

// handleApprovalDecide 面板裁决(批准/拒绝):先记账后送达(审批门
// LedgerGate 保证),此处只做参数透传。裁决者恒为造物主(creator)。
// 拒绝即熔断该单位会话(单位级终止,Step 终态"审批拒绝终止")。
func (s *Server) handleApprovalDecide(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	rid := r.PathValue("rid")
	var req struct {
		Approved *bool  `json:"approved"` // 显式布尔,缺省视为坏请求
		Reason   string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Approved == nil {
		writeErr(w, http.StatusBadRequest, errors.New("审批结果必须是布尔值"))
		return
	}
	err := s.Gate.Decide(r.Context(), taskID, rid, swarm.GateDecision{
		Approved: *req.Approved, By: "creator", Reason: strings.TrimSpace(req.Reason),
	})
	if err != nil {
		if errors.Is(err, swarm.ErrGateFrozen) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rid, "approved": *req.Approved})
}

// handleApprovalRelease 研判后放行(第四章 §3 v2 拒绝线):造物主对拒绝线
// 自动拒绝的记录逐次放行——先落 forced 裁决记录(全记账),再按记录
// 在案的参数执行那一次调用(强制放行全记账;限时与 bash 工具一致且
// 不转后台,镜归因照常)。执行不复活被熔断的单位;同请求不可重复放行。
func (s *Server) handleApprovalRelease(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	rid := r.PathValue("rid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.swarmExec() == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("无虫群执行器可用,规划后放行不可执行"))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()

	// 从事件流找该请求的最新记录:须是拒绝线的 denied 且从未 forced
	//(拒绝线记录不经 Enter,无 requested 前导;弹门流有 requested 前导,
	// 其 denied 的 DenyLine=panel 不可放行)。
	var target *rings.ApprovalRecord
	evs, err := s.Store.Events(r.Context(), taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for i := range evs {
		if evs[i].Kind != rings.EventApprovalRecord {
			continue
		}
		var rec rings.ApprovalRecord
		if err := json.Unmarshal(evs[i].Payload, &rec); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if rec.RequestID != rid {
			continue
		}
		if rec.Type == rings.ApprovalRecForced {
			writeErr(w, http.StatusConflict, errors.New("该请求已规划后放行过(逐次放行,不可重复)"))
			return
		}
		c := rec
		target = &c // 事件升序,留在最后的即最新
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, swarm.ErrGateRequestNotFound)
		return
	}
	if target.Type != rings.ApprovalRecDenied || !releasableDenyLines[target.DenyLine] {
		writeErr(w, http.StatusBadRequest, errors.New("仅拒绝线的自动拒绝可规划后放行(面板拒绝/收尾结案不放行)"))
		return
	}

	// 账本先于副作用(R7):forced 裁决记录先落,再执行。
	forced := *target
	forced.Type = rings.ApprovalRecForced
	forced.DenyLine = target.DenyLine // 保留拒绝线类别,审计可溯来源
	forced.DecidedBy = "creator"
	forced.Reason = strings.TrimSpace(req.Reason)
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "creator", Payload: forced,
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	result, err := s.swarmExec().ExecuteForcedRelease(r.Context(), taskID, *target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rid, "released": true, "tool": target.ToolName, "result": clipJSONText(result, 512),
	})
}

// clipJSONText 面板回显的执行结果截断(全文在账本 tool_call 记录)。
func clipJSONText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// handleWake 手动唤醒(面板调试入口;RoE/目标变更已自动唤醒)。
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "woken"})
}

// ---- 舵杆(P2-05,第八章第 8 节:steer_work / kill_work) ----

// handleSteersList 舵杆指令审计流(steer.record 事件流读侧):排队与
// 送达两级都可见,回执状态机的采纳/被拒/过时随 P2-08 并入。
func (s *Server) handleSteersList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	evs, err := s.Store.Events(r.Context(), taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type steerView struct {
		ID      string `json:"id"`
		StepID  string `json:"step_id"`
		UnitID  string `json:"unit_id,omitempty"`
		Command string `json:"command"` // steer|kill
		State   string `json:"state"`   // queued|delivered
		Message string `json:"message,omitempty"`
		By      string `json:"by,omitempty"`
		Reason  string `json:"reason,omitempty"`
		Note    string `json:"note,omitempty"`
		At      string `json:"at"`
	}
	out := []steerView{}
	for _, ev := range evs {
		if ev.Kind != rings.EventSteerRecord {
			continue
		}
		var rec rings.SteerRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		by := rec.By
		if by == "" {
			by = "creator"
		}
		out = append(out, steerView{
			ID: rec.SteerID, StepID: rec.StepID, UnitID: rec.UnitID,
			Command: rec.Command, State: rec.State, Message: rec.Message,
			By: by, Reason: rec.Reason, Note: rec.Note,
			At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleKillWork 下达 kill_work(止损终止):方向整个错误时停掉在跑
// 单位——走独立停止机制(cancel 单位 ctx,引擎真中断不进收尾),步骤
// 落 aborted(中性可重派)。与任务级熔断不同:只停这一条工作线。
func (s *Server) handleKillWork(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		StepID string `json:"step_id"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.StepID) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("step_id 不能为空"))
		return
	}
	id, err := s.helmOf().KillWork(r.Context(), taskID, strings.TrimSpace(req.StepID), req.Reason, "creator")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": "delivered"})
}

// ---- 四类记录取证(P2-04) ----

// handleTaskTokens token 独立表逐次用量(一行=一次模型调用)。
func (s *Server) handleTaskTokens(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	rows, err := s.Store.TokenUsageRows(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type rowView struct {
		Agent            string `json:"agent"`
		Model            string `json:"model"`
		TranscriptID     string `json:"transcript_id"`
		Turn             int    `json:"turn"`
		InputTokens      int    `json:"input_tokens"`
		OutputTokens     int    `json:"output_tokens"`
		CacheReadTokens  int    `json:"cache_read_tokens"`
		CacheWriteTokens int    `json:"cache_write_tokens"`
		OccurredAt       string `json:"occurred_at"`
	}
	out := make([]rowView, 0, len(rows))
	for _, x := range rows {
		out = append(out, rowView{
			Agent: x.Agent, Model: x.Model, TranscriptID: x.TranscriptID, Turn: x.Turn,
			InputTokens: x.InputTokens, OutputTokens: x.OutputTokens,
			CacheReadTokens: x.CacheReadTokens, CacheWriteTokens: x.CacheWriteTokens,
			OccurredAt: x.OccurredAt.Local().Format("2006-01-02 15:04:05"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTaskBlackbox 黑匣子系统事件(预算状态变化与聚合里程碑)。
func (s *Server) handleTaskBlackbox(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	evs, err := s.Store.Events(r.Context(), taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type recView struct {
		Type   string         `json:"type"`
		StepID string         `json:"step_id,omitempty"`
		UnitID string         `json:"unit_id,omitempty"`
		Detail map[string]any `json:"detail,omitempty"`
		At     string         `json:"at"`
	}
	out := []recView{}
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, recView{
			Type: rec.Type, StepID: rec.StepID, UnitID: rec.UnitID,
			Detail: rec.Detail, At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

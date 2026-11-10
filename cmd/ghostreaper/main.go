// Command ghostreaper 是 Agent GhostReaper 的单二进制入口:
// 内嵌 Next.js 静态面板并提供后端 API。P1-07 起装配安全闸门全套:
// 年轮账本 + 审批门 + Manager(每任务主宰循环)+ 面板 API。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/interject"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/secrets"
	"ghostreaper/internal/server"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/webui"
)

// exitStartupFailed 启动期致命错误退出码(sysexits.h EX_CONFIG 惯例)。
// 二进制自保契约(P3-12):守护脚本 ghostreaperd.sh 按退出码决定拉起——
// 0 / 130 / 143 主动退出与信号停止不拉起;78 起不来(坏配置 / 坏版本)
// 连续 3 次自动回滚 <二进制>.old 旧版;其余运行期崩溃直接拉起。
const exitStartupFailed = 78

// startupFatalf 启动期致命错误:ERROR 级落运行日志(未开日志时保底
// 标准错误),以 78 退出供守护脚本识别。
func startupFatalf(format string, args ...any) {
	runlog.Errorf(format, args...)
	if runlog.Dir() == "" {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	os.Exit(exitStartupFailed)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:28282", "HTTP 监听地址")
	dbPath := flag.String("db", "ghostreaper.db", "年轮账本 SQLite 路径")
	tasksDir := flag.String("tasks", "tasks", "任务工作目录根(转录/blob)")
	flag.Parse()

	// 运行日志(P3-13b ③):logs/ 目录滚动文件,日志头带版本 / 平台 /
	// 启动配置;标准 log 库重定向进来(既有散点 log.Printf 行按 INFO 落)。
	// 开档失败不致命(降级为仅标准错误),其余启动照常。
	// P4-47 C:开档成功即接管标准错误——panic 堆栈与 runtime 致命错误
	// 直写运行日志(崩溃现场不丢,随诊断包导出);接管失败降级为不接管,
	// 如实提示,退出码语义(P3-12)不动。
	logDir := filepath.Join(filepath.Dir(*dbPath), "logs")
	if rl, err := runlog.Open(logDir, runlog.Options{StartupConfig: []string{
		"监听: " + *addr,
		"账本: " + *dbPath,
		"任务目录: " + *tasksDir,
	}}); err != nil {
		log.Printf("警告: 运行日志不可用(%v)——仅标准错误输出", err)
	} else {
		defer rl.Close()
		log.SetOutput(rl)
		if terr := runlog.TakeoverStderr(rl); terr != nil {
			runlog.Warnf("标准错误接管失败(%v)——panic 堆栈不进运行日志,仅标准错误", terr)
		}
	}

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			startupFatalf("建数据目录: %v", err)
		}
	}
	if err := os.MkdirAll(*tasksDir, 0o755); err != nil {
		startupFatalf("建任务目录: %v", err)
	}
	store, err := rings.Open(*dbPath)
	if err != nil {
		startupFatalf("打开年轮账本: %v", err)
	}
	defer store.Close()

	// 静态加密(P3-09):主密钥独立文件(0600,<db>.key,与库同目录),
	// 敏感设置值(模型 key / 搜索 key / 目标凭据 / 镜 CA 私钥)存储层
	// 封装——拿到数据库文件拿不到密钥文件时为不可读密文;存量明文启动
	// 时就地迁移。密钥文件读不出且非首次属致命错误(密文不可恢复,如实
	// 失败不静默重建)。
	keyPath := *dbPath + ".key"
	masterKey, err := secrets.LoadOrCreateKeyFile(keyPath)
	if err != nil {
		startupFatalf("静态加密主密钥: %v", err)
	}
	atRest, err := secrets.NewCipher(masterKey)
	if err != nil {
		startupFatalf("静态加密封装器: %v", err)
	}
	if n, err := store.EnableAtRest(context.Background(), atRest); err != nil {
		startupFatalf("静态加密迁移: %v", err)
	} else if n > 0 {
		log.Printf("静态加密:已就地加密 %d 条存量敏感值", n)
	}

	// 审批门(弹门请求与裁决全程记账,面板经 /api/tasks/{id}/approvals 裁决)。
	gate := swarm.NewLedgerGate(store)
	// 任务冬眠门(P2-08,第七章「任务冬眠」):Manager/虫群执行器/主宰
	// 循环/牧者/审批门/插话服务共持同一实例——冬眠时全员在同一扇门上
	// 冻结,唤醒原地继续。进程级单例,重启后凭账本收养(冬眠保持冬眠)。
	hiberGate := hiber.New()
	gate.Hiber = hiberGate
	// 断点冻结信号(P3-07,第七章「故障与恢复」断点续接):Manager/
	// 虫群执行器/主宰循环共持同一实例——存档断点时单位与循环在各自
	// 边界收场,进程可关;拉起时统一解除。
	ckptGate := ckpt.New()

	// LLM Provider 装配(P2-06e 起经主备熔断链):优先模型库(设置页
	// 存多模型配置并激活主备,键值表持久),环境变量(GR_BASE_URL /
	// GR_API_KEY / GR_MODEL / GR_FORMAT)为进程级兜底——模型库是配置
	// 权威入口,链在 Provider 层做主备切换/冷却回切/零交付重放,对会话
	// 循环透明。链与 Provider 恒装配;镜/主宰循环/虫群执行器在链就绪时
	// 装配(启动时就绪即刻装配;启动时未就绪的,面板激活主用模型后经
	// 06e 配置写触发链重载的既有路径运行时拉起,P3-05b ⑤ 免重启)。
	// 两者皆缺时任务仍可创建与管理 RoE/目标/审批,但无主宰循环运行。
	chain := failover.NewChain(store, envConfigFromOS())
	provider := llm.Provider(chain)
	// 在跑主宰循环注册表(P2-08 单位控制的主宰循环粒度:面板经它找到
	// Loop 实例下达暂停/继续)。
	loops := overmind.NewRegistry()
	var mgr *manager.Manager
	// 牧者会话服务(P2-07,第四章 §1):每任务一个总对话入口(看/说/答),
	// 对话审计流入账本;未配模型时照常装配(消息入账后落失败态,面板如实
	// 呈现)。先于 Manager 构建:其终态回调(P2-08f 收尾总结)随 Config
	// 注入,避免启动后改写配置;Wake 闭包引用的 mgr 在任务拉起前必已赋值。
	// ModelOf 恒注入(链未就绪时返回空串,如实走"模型未配置"路径;链
	// 就绪后自动生效,免重启);记账模型按会话逐请求解析(P4-41)。
	// 执行器装配态(P3-05b ⑤ 两段装配的共享指针;先于牧者声明——
	// 牧者 HelmOf 闭包读当前装配的舵杆,P4-45)。
	var (
		assembleMu sync.Mutex
		exec       *swarm.Executor
	)
	shep := &shepherd.Service{
		Store: store, Provider: provider, RootDir: *tasksDir,
		Model: chain.ActiveModel(), Hiber: hiberGate,
		WindowOf: func() int { return failover.ContextWindowFor(context.Background(), store, chain.ActiveModel()) },
		Wake: func(taskID string) {
			if t := mgr.Task(taskID); t != nil {
				t.Wake()
			}
		},
		// P4-45 资产范围工具的摘除止损经舵杆终止在途单位:闭包读当前
		// 装配的执行器舵杆(与面板 helmOf 同一实例;未装配时 nil——
		// 无在跑单位可停,止损只剩排队步骤取消,如实执行)。
		HelmOf: func() *swarm.Helm {
			assembleMu.Lock()
			defer assembleMu.Unlock()
			if exec == nil {
				return nil
			}
			return exec.Helm
		},
		// P4-54 牧者操作面的任务级控制(与面板按钮同源同账本):闭包
		// 引用的 mgr 在任务拉起前必已赋值(与 Wake 同款次序)。唤醒按
		// 任务实际状态路由(冬眠走 Wake、暂停/断点走 Resume——与面板
		// 唤醒端点同一判定)。
		TaskHibernate: func(taskID string, detail map[string]any) error {
			return mgr.Hibernate(taskID, detail)
		},
		TaskWake: func(taskID string) error {
			switch t := mgr.Task(taskID); {
			case t == nil:
				return fmt.Errorf("任务 %s 不在册", taskID)
			case t.Status() == manager.StatusHibernated:
				return mgr.Wake(taskID, nil)
			case t.Status() == manager.StatusPaused || t.Status() == manager.StatusCheckpointed:
				return mgr.Resume(taskID)
			default:
				return fmt.Errorf("任务 %s 状态 %s 不可唤醒", taskID, t.Status())
			}
		},
		TaskStop: func(taskID string) error {
			return mgr.Stop(taskID)
		},
		TaskStatus: func(taskID string) string {
			if t := mgr.Task(taskID); t != nil {
				return t.Status()
			}
			return ""
		},
	}
	// 文风 skill 包(P3-11,第四章 §10 按需层):内嵌 SKILL.md 物化到
	// 任务根 skills/ 后经 Norma 原生加载挂进牧者会话,deferred 按需注入。
	// 失败降级为不挂(对话照常,「改像人话」功能缺失,启动日志如实提示)。
	if reg, err := shepherd.LoadSkills(filepath.Join(*tasksDir, "skills")); err != nil {
		runlog.Warnf("文风 skill 包加载失败(%v)——对话照常,改写 skill 不可用", err)
	} else {
		shep.Skills = reg
	}
	if err := shep.Start(context.Background()); err != nil {
		startupFatalf("牧者会话接管: %v", err)
	}

	// 执行器全家桶装配(P3-05b ⑤ 重排为两段):buildExec 构建虫群执行器
	// 与镜(P2-01/P3-01:工具流量强制经镜,HTTPS 解密留痕;镜核与虫群
	// blob 存储共用,CA 经设置表持久化、信任包落任务根目录);attachLoop
	// 在 Manager/面板就绪后挂循环体与运行时指针。启动时就绪的装配必须
	// 发生在 manager.New 之前——重启收养的拉起判定(OnTaskRun 非空)
	// 据此走重拉路径;启动时未就绪的,面板激活主用模型后经 06e 配置写
	// 触发链重载的既有路径运行时补装配(免重启)。
	buildExec := func() {
		assembleMu.Lock()
		defer assembleMu.Unlock()
		if exec != nil || !chain.Ready() {
			return
		}
		exec = swarm.NewExecutor(store, provider, *tasksDir, swarm.Budget{})
		exec.Gate = gate
		exec.Hiber = hiberGate
		exec.Ckpt = ckptGate
		exec.Model = chain.ActiveModel()
		exec.Mirror = mirror.New(store, exec.Blobs, *tasksDir)
		log.Printf("模型就绪:已装配镜 / 虫群执行器(免重启)")
	}
	// taskLoopOf 每任务一个主宰循环(P1-05 约定:经 Manager 挂接点构造;
	// P2-04 挂任务级总闸操控面;P2-08 挂冬眠门与循环注册表)。闭包引用的
	// exec 在装配完成后不变(装配仅一次)。
	taskLoopOf := func(e *swarm.Executor) manager.TaskRunFunc {
		return func(ctx context.Context, taskID string, wake <-chan struct{}) {
			l := &overmind.Loop{
				TaskID: taskID, Store: store, Swarm: e, Provider: provider,
				RootDir: *tasksDir, Model: chain.ActiveModel(),
				SettleCtl: mgr, Hiber: hiberGate, Ckpt: ckptGate,
			}
			unregister := loops.Register(taskID, l)
			defer unregister()
			l.Run(ctx, wake)
		}
	}
	buildExec()
	var startupLoop manager.TaskRunFunc
	if exec != nil {
		startupLoop = taskLoopOf(exec)
	}
	// P4-33 冬眠让位的执行层配套:同执行器/审批门实例(闭包读当前
	// 装配——启动未就绪、面板激活后运行时装配同样生效);未装配时为
	// nil(无执行层挂起者,冬眠收场无票可放行)。
	hibSink := &hibernateSink{execOf: func() *swarm.Executor {
		assembleMu.Lock()
		defer assembleMu.Unlock()
		return exec
	}}
	mgr, err = manager.New(manager.Config{
		Store: store, RootDir: *tasksDir, Hiber: hiberGate,
		Checkpoint:    ckptGate,
		OnTerminal:    shep.OnTerminal, // P2-08f:任务终态 → 牧者一句进展总结
		OnTaskRun:     startupLoop,     // 启动时就绪:重启收养按重拉路径收养
		HibernateSink: hibSink,         // P4-33:冬眠收场放行挂起者/票据善后
	})
	if err != nil {
		startupFatalf("构建 Manager: %v", err)
	}

	srv, err := server.New(store, mgr, gate)
	if err != nil {
		startupFatalf("构建面板后端: %v", err)
	}
	srv.Shepherd = shep
	// 插话子会话服务(P2-08,第四章 §7:问/改的旁路子会话,独立留存
	// 不污染主轨迹)。改→对虫群经舵杆(与执行器同一实例)、对主宰落
	// 建议并唤醒裁决;问经快照直问(P2-08f:目标最近一次请求的不可变
	// 上下文快照单次 Complete,不建会话)。未配模型时舵杆与执行器随
	// 运行时装配换入(steer 排队照常、kill 如实报无单位)。
	ij := &interject.Service{
		Store: store, Provider: provider, RootDir: *tasksDir,
		Model: chain.ActiveModel(),
		Hiber: hiberGate,
		Wake: func(taskID string) {
			if t := mgr.Task(taskID); t != nil {
				t.Wake()
			}
		},
	}
	srv.Interject = ij
	srv.HiberGate = hiberGate
	srv.Loops = loops
	srv.RootDir = *tasksDir
	// 主备链交给面板(P2-06e):模型库写路径经同一链实例重载(配置
	// 切换即时生效),熔断状态/激活配置/故障原因读同一持久化状态。
	srv.Chain = chain

	// attachLoop 运行时挂接(幂等):挂 Manager 全局循环体 + 面板换入
	// 执行器与同一舵杆实例 + 插话服务接舵杆。启动时就绪与运行时激活
	// 走同一入口。
	attachLoop := func() {
		assembleMu.Lock()
		e := exec
		assembleMu.Unlock()
		if e == nil {
			return
		}
		// P4-64(问题清单 #82)显示层包装钩子:swarm 侧入账点(反证轮
		// 结论/复测结论)按任务路由到在跑主宰循环生成包装文本——钩子
		// 读注册表实时取循环,循环退出后自然退 nil(读侧退原文,不阻塞
		// 入账);执行器为全局一个、循环每任务一个,故经注册表分发。
		e.RoundHumanGen = func(ctx context.Context, taskID, claimID string, rec *rings.TestClaimRecord) *rings.ClaimRoundHuman {
			if l := loops.Get(taskID); l != nil {
				return l.GenRoundHumanFor(ctx, claimID, rec)
			}
			return nil
		}
		e.RetestNotePolisher = func(ctx context.Context, taskID, claimID string, round int, verdict, note string) *rings.RetestNotePolish {
			if l := loops.Get(taskID); l != nil {
				return l.PolishRetestNote(ctx, claimID, round, verdict, note)
			}
			return nil
		}
		mgr.SetOnTaskRun(taskLoopOf(e))
		srv.AttachRuntime(e)
		ij.SetHelm(e.Helm)
	}
	// 运行时装配触发点(P3-05b ⑤):面板激活主用/保存使链就绪 → 经
	// 06e 配置写触发链重载的既有路径回调,免重启生效。
	srv.RuntimeAssemble = func() {
		buildExec()
		attachLoop()
	}
	attachLoop()
	if exec == nil {
		runlog.Warnf("未配置可用模型(模型库未激活主用,GR_* 环境变量未注入)——任务可管理 RoE/目标/审批,但主宰循环不运行;面板 LLM配置 页激活主用后即自动装配(免重启)")
	}
	// P4-57 ④ 存量受阻步骤随升级一次性自动重开(程序缺陷类死锁解法:
	// 重开不再只挂"模型配置恢复";幂等标记键,仅本升级跑一次)。
	srv.ReopenBlockedStockOnce()

	// 设置页只读装配信息(P1-09b):LLM 注入状态 + 并发/预算默认值。
	// P2-08c:系统级参数(监听/账本/任务目录)随设置收窄一并呈现。
	srv.Config.LLM.Configured = os.Getenv("GR_BASE_URL") != "" && os.Getenv("GR_API_KEY") != "" && os.Getenv("GR_MODEL") != ""
	srv.Config.System.Addr = *addr
	srv.Config.System.DBPath = *dbPath
	srv.Config.System.TasksDir = *tasksDir
	if srv.Config.LLM.Configured {
		srv.Config.LLM.Model = os.Getenv("GR_MODEL")
		srv.Config.LLM.Format = string(llm.FormatOpenAI)
		if strings.TrimSpace(strings.ToLower(os.Getenv("GR_FORMAT"))) == "anthropic" {
			srv.Config.LLM.Format = string(llm.FormatAnthropic)
		}
	}
	mux := srv.Handler()
	mux.Handle("/", webui.Handler())

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.WrapAuth(mux), // P3-09 面板认证:未登录不可访问面板与 API
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("ghostreaper 面板: http://%s (db=%s, 密钥=%s)", *addr, *dbPath, keyPath)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		startupFatalf("http server: %v", err)
	}
}

// envConfigFromOS 环境变量兜底配置(P1-02 惯例:运行时注入,零落盘)。
func envConfigFromOS() failover.EnvConfig {
	baseURL, apiKey, model := os.Getenv("GR_BASE_URL"), os.Getenv("GR_API_KEY"), os.Getenv("GR_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		return failover.EnvConfig{}
	}
	format := string(llm.FormatOpenAI)
	if strings.TrimSpace(strings.ToLower(os.Getenv("GR_FORMAT"))) == "anthropic" {
		format = string(llm.FormatAnthropic)
	}
	return failover.EnvConfig{Configured: true, Format: format, BaseURL: baseURL, APIKey: apiKey, Model: model}
}

// hibernateSink P4-33 冬眠让位的执行层配套(manager.HibernateSink 的
// 装配实现):收场放行与票据善后全走虫群执行器/审批门既有实例;执行器
// 未装配时静默无动作(无挂起者可放行、无票可善后)。execOf 读当前装配
// ——启动未就绪、面板激活后运行时装配同样生效。
type hibernateSink struct {
	execOf func() *swarm.Executor
}

func (h *hibernateSink) FlushSuspended(taskID string) {
	if e := h.execOf(); e != nil {
		e.FlushSuspended(taskID)
	}
}

func (h *hibernateSink) DropPending(taskID string) {
	if e := h.execOf(); e != nil {
		e.DropPendingApprovals(taskID)
	}
}

func (h *hibernateSink) ExpirePending(taskID, reason string) {
	if e := h.execOf(); e != nil {
		e.ExpirePendingApprovals(taskID, reason)
	}
}

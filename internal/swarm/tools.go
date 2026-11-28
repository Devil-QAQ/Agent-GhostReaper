package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/uapolicy"
	"ghostreaper/internal/vault"
)

// unitEnv 是一只虫群单位执行期间的共享环境:受控入口(提交候选)与
// 工具(大输出落 blob、HTTP 通道经镜)都从这里拿账本句柄与单位身份。
type unitEnv struct {
	Executor        *Executor
	TaskID          string
	StepID          string
	UnitID          string
	TranscriptID    string
	graphVersion    int64
	auditMu         sync.Mutex
	auditedResults  map[string]int
	owner           *rings.ExecutionOwner
	callsMu         sync.Mutex
	calls           sync.WaitGroup
	callbackCancel  context.CancelFunc
	callsClosed     bool
	executionClosed bool
	reviewMu        sync.Mutex // one policy decision/approval ticket at a time per unit
	reviewSources   map[string]string
	reviewErr       error           // drained with callbacks; uses the existing model-fault recovery
	mirror          *mirror.Session // 本单位的镜会话(nil=无镜直连,P1 行为);接管对工具透明

	// Segment state is shared by parallel callbacks. The drive reads it only
	// after cancellation and callback drain. Policy decisions use reviewMu.
	segCancel    context.CancelFunc // 当前执行段的取消句柄(弹门挂起/拒绝熔断/暂停挂起收段用)
	park         *GateTicket        // 挂起中的弹门票(非 nil = 当前段因弹门收拢,drive 停在票上等裁决)
	allowance    *gateAllowance     // 批准续跑的单次放行配额(重试同一调用时消费)
	deniedReason string             // 拒绝即熔断:非空 = 单位以 approval_denied 终态收场(仅拒绝线 denyGuard)

	// P2-08 单位暂停/任务冬眠的段状态(暂停请求由 guard 在调用边界置位,
	// drive 消费;审批挂起锚点在进票时记录)。
	parkArmed      bool          // 工具边界已请求挂起(单位暂停/任务冬眠):段收拢后回段循环顶挂起
	ticketRev      int64         // 弹门挂起时的图版本(等待期间冬眠过的,R14 复检锚点)
	ticketGen      uint64        // 弹门挂起时的冬眠代数(代数前进 = 等过一场冬眠)
	deadlineCredit time.Duration // 冬眠时长抵扣(唤醒续跑段的任务收尾线顺延量)
	// P5-05 暂停挂等的宽限状态:parkYielded=宽限耗尽已让位(闩已静默
	// 清除,drive 落 pause_wait 收场);latchSince=本段暂停插曲的起算
	//(继续即清零重计)。
	parkYielded bool
	latchSince  time.Time

	// resumed 原会话续(P2-06f 重启收养 + P3-05c ① 受阻重开 + P3-07 断点
	// 续接,第七章「恢复粒度」):本单位领取的步骤存在可续会话——首段经
	// 转录 Resume 原会话原地续跑(与冬眠唤醒同款),续跑指令为短注入,
	// 任务包原文已在会话历史里。用量增量基线同步锚到恢复出的累计用量
	//(token 表不重复计历史)。resumeKind 区分可续会话的来源,续跑指令
	// 措辞随之不同;转录缺失/为空时在首段续跑处降级回全新会话接力。
	resumed    bool
	resumeKind string // ""(全新)| adoption(崩溃残留)| blocked(受阻保留)| checkpoint(断点存档)
	// resumeYield 让位收场的续跑上下文(P5-05,仅 resumeKind=yield):
	// 让位时冻结的挂等来源与被拦命令——续跑首条指令按此组装,审批类
	// 批准同时武装放行配额。
	resumeYield *rings.YieldNote

	// retest 复测会话绑定(P3-06):非 nil = 复测单位——工具集收窄为
	// 复测受限集(bash/只读探测/read + submit_retest_result),结论
	// 写回目标从绑定取(服务端定死防串号)。见 retest.go。
	retest *retestBinding

	// bash 后台输出文件序号(P3-05:并行调用下原子分配 bg-N.output)。
	bgMu  sync.Mutex
	bgSeq int
}

// reserveBGPath 分配本单位的下一个后台输出文件路径(步骤工作目录内,
// 调用前目录已建好)。
func (env *unitEnv) reserveBGPath() string {
	env.bgMu.Lock()
	env.bgSeq++
	n := env.bgSeq
	env.bgMu.Unlock()
	return filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), fmt.Sprintf("bg-%d.output", n))
}

// httpClient 单位 HTTP 客户端:装配了镜就走镜(传输层显式 Proxy),
// 没装配就直连——工具代码两种情形完全同一份(接管透明的结构保证)。
func (env *unitEnv) httpClient() *http.Client {
	if env.mirror != nil {
		return env.mirror.HTTPClient(15 * time.Second)
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// userAgent 出站请求的 User-Agent(P3-13c 流量去特征):按系统设置
// UA 策略现取——随机浏览器池(默认)/ 固定自定义 / 模拟常见工具,配置
// 对下一次请求即生效(与搜索配置同语义)。只作用于自家工具(http_probe
// / web_fetch);bash 里模型自拼命令的 UA 不干预(完全开放原则)。
func (env *unitEnv) userAgent(ctx context.Context) string {
	return uapolicy.Load(ctx, env.Executor.Store).Pick()
}

func alwaysRO(json.RawMessage) bool { return true }

func allowReadOnly(context.Context, json.RawMessage, permission.Context) permission.Decision {
	return permission.Allowed()
}

// shortID 生成短随机后缀(候选节点 ID 用)。
func shortID(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

// ---- Bash 通道(P3-05 限时放开:模型自调 timeout,封顶 600 秒,超时
// 自动转后台) ----
//
// 能力口径与引擎内置 bash 对齐:timeout_ms 自调(默认 120 秒、上限
// 600 秒)、显式后台(run_in_background)、超时未完成自动转后台。转后台
// 的进程脱离本次调用继续运行,输出流式写进本步骤工作目录的 bg-N.output
// (退出后文件末尾追加 [exit: N] 尾行,模型据尾行判定结束,经 read 或
// bash(tail) 读取;完成事实在下一个工具调用边界以提醒消息注入,免盲
// 轮询)。sleep 类纯等待命令不转后台(转后台毫无产出,超时即杀是诚实
// 语义,与引擎同款)。审批与留痕照旧:限时放开不豁免任何门与账——
// bash 本就逐次过审批门,工具结果(含"已转后台"事实)照常入账,
// 后台子进程与前台同一构造(镜代理注入照旧,流量经镜留痕)。
// 后台进程登记在执行器(单位粒度),单位落地/硬停/止损时整组回收,
// 输出文件保留供审计。
// P4-37 失控防护两道闸(定值,不做配置项):输出文件写满 100MB 停写
// (尾部追加省略标记,截断记黑匣子);后台进程挂钟超 30 分钟强制结束
// (kill 记黑匣子,尾行与 [exit] 同款)。

const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxTimeout     = 600 * time.Second
)

// bashTimeoutOf 解析 timeout_ms 入参(缺省或 <=0 = 默认;超上限压到上限)。
func bashTimeoutOf(ms int) time.Duration {
	if ms <= 0 {
		return bashDefaultTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d > bashMaxTimeout {
		return bashMaxTimeout
	}
	return d
}

// bashAutoBGDenied 超时后不得转后台的命令(首词判定,与引擎同款):
// sleep 是等待原语,转后台不会产出任何东西,超时即杀才诚实。
var bashAutoBGDenied = map[string]bool{"sleep": true}

func bashAutoBackgroundAllowed(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	base := fields[0]
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	return !bashAutoBGDenied[base]
}

// bashProc 一条已启动的 bash 进程:输出流式落文件。前台完成即删文件、
// 输出走返回文本;转后台则文件保留,退出后追加 [exit: N] 尾行。
// out 是带上限的输出写入器(P4-37 ①:写满 100MB 停写+省略标记);
// started 锚定挂钟起点(P4-37 ② 寿命闸自进程启动起算)。
type bashProc struct {
	cmd     *exec.Cmd
	f       *os.File
	out     *capWriter
	outPath string
	started time.Time
	waitErr error // 仅在 exited 关闭后读(通道关闭先行发生保证)
	exited  chan struct{}
}

// exitLine 进程退出码的尾行形态("[exit: N]";非退出码错误如实带原文)。
func exitLineOf(err error) string {
	if err == nil {
		return "[exit: 0]"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("[exit: %d]", ee.ExitCode())
	}
	return "[exit: " + err.Error() + "]"
}

// bgRun 一条在册的后台 bash 命令(执行器按单位登记,落地/硬停/止损
// 整组回收;输出文件保留供审计与读取)。notified 保证完成提醒至多一次。
type bgRun struct {
	proc     *bashProc
	outFile  string
	notified bool
}

// Finished 进程是否已退出。
func (r *bgRun) Finished() bool {
	select {
	case <-r.proc.exited:
		return true
	default:
		return false
	}
}

// startBash 启动一条 bash 进程(输出经带上限的写入器落文件,waiter 协程
// 守退出;P4-37 ① 输出闸自进程启动即生效——前台段与转后台共用同一
// 输出文件,闸门必须横跨两个阶段)。
func (env *unitEnv) startBash(command, dir string) (*bashProc, error) {
	outPath := env.reserveBGPath()
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	cw := newCapWriter(f, bgOutputCapBytes)
	cmd := env.newBashCmd(command, dir)
	cmd.Stdout, cmd.Stderr = cw, cw
	if err := cmd.Start(); err != nil {
		f.Close()
		os.Remove(outPath)
		return nil, err
	}
	p := &bashProc{cmd: cmd, f: f, out: cw, outPath: outPath, started: time.Now(), exited: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// newBashCmd 构造 bash 子进程:工作目录、镜代理环境注入(先剔除继承
// 环境的代理变量防旁路;前台后台同一构造,后台命令流量照常经镜留痕)、
// 目标凭据环境变量注入(P3-09:GR_CRED_*=秘密,任务包只告知变量名)、
// 独立进程组(回收时整组杀)。
func (env *unitEnv) newBashCmd(command, dir string) *exec.Cmd {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	if env.mirror != nil {
		cmd.Env = append(stripProxyEnv(os.Environ()), env.mirror.ProxyEnv()...)
	}
	// 目标凭据(P3-09):读失败如实跳过(凭据不可读不挡命令执行,与
	// 领取侧 fail-safe 同纪律);无凭据时零注入。
	if env.Executor != nil && env.Executor.Store != nil {
		if credEnv, err := vault.EnvVars(context.Background(), env.Executor.Store); err == nil && len(credEnv) > 0 {
			cmd.Env = append(cmd.Env, credEnv...)
		}
	}
	setBashProcGroup(cmd)
	return cmd
}

// handoverBG 转后台交接:进程不杀、输出续写;退出后追加尾行并入册
// (单位落地/硬停/止损整组回收)。P4-37:输出截断在此刻武装黑匣子回记
// (纯前台路径不记闸账),寿命闸看门狗同步上岗(覆盖显式后台与超时转
// 后台两条路径——全部后台进程都经此交接)。
func (env *unitEnv) handoverBG(p *bashProc) *bgRun {
	p.out.armCapped(func() { env.recordBGGuard(rings.BlackboxBGOutputTrunc, p) })
	go env.bgLifespanGuard(p)
	go func() {
		<-p.exited
		p.f.Close()
		if af, err := os.OpenFile(p.outPath, os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(af, "\n%s\n", exitLineOf(p.waitErr))
			af.Close()
		}
	}()
	run := &bgRun{proc: p, outFile: filepath.Base(p.outPath)}
	env.Executor.registerBG(env.UnitID, run)
	return run
}

// bgTruncNote 输出已截断时的话术追加(P4-37 ①:如实回传截断状态,
// 模型据此自行换法);未截断返回空串。
func bgTruncNote(p *bashProc) string {
	if p.out.Truncated() {
		return "输出已达上限被截断(文件尾有省略标记),需要完整输出请改用输出更少的方式(如写文件后分段读取)。"
	}
	return ""
}

// bashTool 执行 shell 命令的通道:真实进程、真实超时、合并 stdout/stderr。
// 大输出整段入内容寻址文件,模型只见截头+引用(R18;与账本同款 spill,
// 审计可从虫群记录的 ResultRef 一路下钻到原始输出)。
// 描述即规则(§11):限时默认/上限/超时转后台写明,模型排命令时自行掂量。
func (env *unitEnv) bashTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "bash",
		Description: toolDescBash,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string", "description": "要执行的 shell 命令(单条或管道)"},
				"timeout_ms":        map[string]any{"type": "integer", "description": "限时毫秒(默认 120000,上限 600000):长任务(扫描/爆破/批量验证)按预估时长自行调大;超时未完成自动转后台继续跑,不会掐断"},
				"run_in_background": map[string]any{"type": "boolean", "description": "true = 不等结果直接后台运行,立即返回;输出写本步骤工作目录 bg-N.output,用 read 或 bash(tail) 读取,结束后有后台完成提醒;后台命令输出上限 100MB(超出省略)、最长运行 30 分钟(到点强制结束)"},
			},
			"required": []string{"command"},
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Command         string `json:"command"`
				TimeoutMS       int    `json:"timeout_ms"`
				RunInBackground bool   `json:"run_in_background"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.Command) == "" {
				return tool.Errorf("command 不能为空:填入要执行的 shell 命令(单条或管道)后重发"), nil
			}
			dir := env.Executor.stepDir(env.TaskID, env.StepID)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return tool.Errorf("建工作目录失败: " + err.Error()), nil
			}
			if args.RunInBackground {
				return env.runBashBG(args.Command, dir)
			}
			return env.runBashFG(ctx, args.Command, dir, bashTimeoutOf(args.TimeoutMS))
		},
	})
}

// runBashFG 前台执行:限时内完成即合并输出返回;超时且可转后台(非
// sleep 类、非单发路径)则进程脱手继续跑、输出续写文件;ctx 取消
// (段收拢/单位硬停)杀整组进程。
// P5-01②:镜接管时结果尾部标注本次新增的交换编号(exchange= 行,
// 交换单 evidence_refs 的引用来源——bash 通道此前取不到编号,照抄
// http_probe 的快照差分语义;一次命令可能产生多条交换,按受理次序
// 全列,模型引用直接证明结论的那条)。
func (env *unitEnv) runBashFG(ctx context.Context, command, dir string, timeout time.Duration) (tool.Result, error) {
	var mirrorSeq int
	if env.mirror != nil {
		mirrorSeq = env.mirror.ExchangeCount()
	}
	p, err := env.startBash(command, dir)
	if err != nil {
		return tool.Errorf("启动命令失败: " + err.Error()), nil
	}
	// finish 前台完成/取消的收尾:删文件(输出走返回文本)、拼退出注记、
	// 大输出入 blob(R18)、附本次交换编号。
	finish := func() tool.Result {
		p.f.Close()
		out, _ := os.ReadFile(p.outPath)
		os.Remove(p.outPath)
		text := string(out)
		if p.waitErr != nil {
			text += fmt.Sprintf("\n[exit: %v]", p.waitErr)
		}
		if env.mirror != nil {
			for _, ex := range env.mirror.ExchangesSince(mirrorSeq) {
				text += fmt.Sprintf("\nexchange=%s %s", ex.ID, ex.URL)
			}
		}
		if ref, head := env.Executor.Blobs.spill(text); ref != "" {
			return tool.Text(fmt.Sprintf("%s\n[输出超长,全文已存档: %s]", head, ref))
		}
		return tool.Text(text)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.exited:
		return finish(), nil
	case <-timer.C:
		select {
		case <-p.exited: // 与到期同刻完成:按完成计
			return finish(), nil
		default:
		}
		if !bashAutoBackgroundAllowed(command) {
			killBashGroup(p.cmd)
			<-p.exited
			p.f.Close()
			out, _ := os.ReadFile(p.outPath)
			os.Remove(p.outPath)
			return tool.Errorf(fmt.Sprintf("命令超时(>%s),已终止:更长的任务用 timeout_ms 调大后重发(默认 %s,上限 %s),或改用 run_in_background;残余输出: %s",
				timeout, bashDefaultTimeout, bashMaxTimeout, clipTail(string(out), 512))), nil
		}
		run := env.handoverBG(p)
		return tool.Text(fmt.Sprintf(
			"命令超时(>%s),已转后台继续运行:输出写入本步骤工作目录 %s,结束后文件末尾出现 [exit: N] 行。期间可先做其他事,用 read 或 bash(tail %s) 查看进度;命令结束后会有后台完成提醒。%s",
			timeout, run.outFile, run.outFile, bgTruncNote(p))), nil
	case <-ctx.Done():
		killBashGroup(p.cmd)
		<-p.exited
		return finish(), nil
	}
}

// runBashBG 显式后台:立即返回,进程与输出文件的生命周期同转后台路径。
func (env *unitEnv) runBashBG(command, dir string) (tool.Result, error) {
	p, err := env.startBash(command, dir)
	if err != nil {
		return tool.Errorf("启动命令失败: " + err.Error()), nil
	}
	run := env.handoverBG(p)
	return tool.Text(fmt.Sprintf(
		"已在后台启动:输出写入本步骤工作目录 %s,结束后文件末尾出现 [exit: N] 行;用 read 或 bash(tail %s) 查看进度;命令结束后会有后台完成提醒。%s",
		run.outFile, run.outFile, bgTruncNote(p))), nil
}

// ---- 探测工具 ----

// portProbeTool 只读探测:对目标发起 TCP 连接判定端口开放状态并抓取
// banner(读到即断,不发攻击载荷)。只读工具经引擎权限管线自动放行。
// 描述即规则(§11):低价值面(banner 截短/静默)在描述内直接压制,
// 模型决定调不调的瞬间读到的就是边界。
func (env *unitEnv) portProbeTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "port_probe",
		Description: toolDescPortProbe,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host": map[string]any{"type": "string", "description": "目标主机(须在 RoE 授权范围内)"},
				"port": map[string]any{"type": "integer", "description": "目标端口(1–65535)"},
			},
			"required": []string{"host", "port"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if args.Host == "" || args.Port <= 0 || args.Port > 65535 {
				return tool.Errorf("host/port 非法:host 须非空、port 须为 1–65535 的整数;修正参数后重发"), nil
			}
			addr := net.JoinHostPort(args.Host, fmt.Sprintf("%d", args.Port))
			d := net.Dialer{Timeout: 5 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return tool.Text(fmt.Sprintf("host=%s port=%d state=closed (%v)", args.Host, args.Port, err)), nil
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 256)
			n, _ := conn.Read(buf)
			banner := ""
			if n > 0 {
				banner = strings.TrimSpace(string(buf[:n]))
			}
			state := "state=open"
			if n == 0 {
				state = "state=open banner=<silent>"
			} else {
				state = fmt.Sprintf("state=open banner=%q", clipTail(banner, 200))
			}
			return tool.Text(fmt.Sprintf("host=%s port=%d %s", args.Host, args.Port, state)), nil
		},
	})
}

// ---- HTTP 探测工具(P2-01 流量载体) ----

// httpProbeBodyClip 工具返回给模型的响应体截取长度(完整原文在镜的
// blob 留痕里,审计走 mirror.record 引用下钻)。
const httpProbeBodyClip = 1536

// httpProbeTool 只读 HTTP 探测:对 URL 发 GET/HEAD,返回状态码、关键
// 响应头与响应体开头。流量载体(P2-01 镜验证):客户端从单位环境拿
// (装配了镜就走镜,工具代码零感知)。只读工具经引擎权限管线自动放行。
// 描述即规则(§11):响应体截取边界写明——勿据此认为已读全。
func (env *unitEnv) httpProbeTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "http_probe",
		Description: "只读探测:对指定 URL 发起 HTTP 请求(GET 或 HEAD),返回状态码、关键响应头(Server/Content-Type/Content-Length)与响应体开头内容。响应体只返回前 1536 字节——勿据此认为已读全,需要完整响应时改用 bash(curl)并注意该意图的审批分级。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":    map[string]any{"type": "string", "description": "目标 URL(http/https,须在 RoE 授权范围内)"},
				"method": map[string]any{"type": "string", "description": "请求方法,GET 或 HEAD,默认 GET"},
			},
			"required": []string{"url"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			u, err := url.Parse(strings.TrimSpace(args.URL))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return tool.Errorf("url 非法:须为 http/https 且含主机(如 http://host:port/path);读本地文件不归本工具,改用 bash"), nil
			}
			method := strings.ToUpper(strings.TrimSpace(args.Method))
			if method == "" {
				method = http.MethodGet
			}
			if method != http.MethodGet && method != http.MethodHead {
				return tool.Errorf("本工具只读,method 仅支持 GET/HEAD;要发带载荷的测试请走 bash 通道,并注意该意图的审批分级"), nil
			}
			req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
			if err != nil {
				return tool.Errorf("构造请求失败: " + err.Error()), nil
			}
			req.Header.Set("User-Agent", env.userAgent(ctx))
			// P4-73 ①:镜会话的受理序号快照(请求后按最终 URL 在新增交换里
			// 找到本次交换编号,结果里标注 exchange= 行——反证结论据此引用
			// 证明交换;重定向链取末跳)。
			var mirrorSeq int
			if env.mirror != nil {
				mirrorSeq = env.mirror.ExchangeCount()
			}
			resp, err := env.httpClient().Do(req)
			if err != nil {
				return tool.Errorf("请求失败: " + err.Error()), nil
			}
			defer resp.Body.Close()
			exchangeID := ""
			if env.mirror != nil {
				finalURL := u.String()
				if resp.Request != nil && resp.Request.URL != nil {
					finalURL = resp.Request.URL.String()
				}
				for _, ex := range env.mirror.ExchangesSince(mirrorSeq) {
					if ex.URL == finalURL {
						exchangeID = ex.ID // 末条命中 = 本次交换(受理次序)
					}
				}
			}
			var body string
			if method == http.MethodGet {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, httpProbeBodyClip))
				body = string(b)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "url=%s method=%s status=%d\n", u.String(), method, resp.StatusCode)
			fmt.Fprintf(&b, "server=%s content-type=%s content-length=%s\n",
				resp.Header.Get("Server"), resp.Header.Get("Content-Type"), resp.Header.Get("Content-Length"))
			if exchangeID != "" {
				fmt.Fprintf(&b, "exchange=%s\n", exchangeID)
			}
			if body != "" {
				fmt.Fprintf(&b, "--- body(前 %d 字节) ---\n%s", httpProbeBodyClip, body)
			}
			return tool.Text(b.String()), nil
		},
	})
}

// proxyEnvNames 子进程环境里须剔除的代理类变量(镜像接管时防止
// 外层环境造成旁路;大小写各一份,curl/Go 等读取口径不一)。CA 信任
// 变量一并剔除(P3-01 信任安装):重复键下子进程取先出现者,外层
// 残留会把镜信任包顶掉。
var proxyEnvNames = map[string]bool{
	"HTTP_PROXY": true, "http_proxy": true,
	"HTTPS_PROXY": true, "https_proxy": true,
	"ALL_PROXY": true, "all_proxy": true,
	"NO_PROXY": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
}

// stripProxyEnv 剔除继承环境中的代理变量。
func stripProxyEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && proxyEnvNames[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ---- 工具目录(名称 + 描述单一来源) ----
//
// 描述即规则(§11):模型决定调不调工具的瞬间读的就是规则,因此描述
// 文本只此一份——工具 Spec 与快照重建的只读目录(ToolCatalog,插话
// 快照直问用)共用同一批常量,两处不得漂移。

const (
	toolDescBash                = "在授权范围内执行 shell 命令(单条或管道),返回合并输出。单条命令默认限时 120 秒,可用 timeout_ms 自行调整(上限 600 秒);超时未完成的命令自动转入后台继续运行(输出落文件可稍后读取,sleep 类等待命令除外),也可用 run_in_background 直接后台跑。后台命令输出上限 100MB(超出省略)、最长运行 30 分钟。大输出会被存档并截断,需要全文时按存档引用查阅。"
	toolDescPortProbe           = "只读探测:检测 host:port 的 TCP 开放状态并尝试读取服务 banner。banner 只取前 256 字节且可能静默——勿据此认为已测完;需要更多服务特征时改用 http_probe 或 bash 通道。"
	toolDescHTTPProbe           = "只读 HTTP 探测:对指定 URL 发起 HTTP 请求(GET 或 HEAD),返回状态码、关键响应头(Server/Content-Type/Content-Length)与响应体开头内容。响应体只返回前 1536 字节——勿据此认为已读全,需要完整响应时改用 bash(curl)并注意该意图的审批分级。"
	toolDescSubmitCandidateFact = "观察到资产解析、部署等关系时，随证据提交asset_relations；使用实际资产编号，已有资产不重复申报，申报的新对象可用$new引用。关系不等于父子层级、产生来源或授权。提交一条新进事实(你本次真实观察到的结论)。须经主宰检查归纳确认后才成为正式事实。陈述须有本次执行取得的证据。观察到清单以外的域名、IP或服务入口时，可随本条事实申报new_asset，已纳入网站内的资源直接归档；新端口、新域名/IP只有已有事实确认同系统才自动归入，其他独立系统或无法归属由操作者选择加入；same_system只凭本次实际依据填写，不能用共享IP或相似框架证明。子域名仍选domain，IP选host，网站入口选service；站内接口/API/URL直接随事实归到所属网站，不逐条申报独立资产，HTTP方法和参数只留在证据。组织、人员、登记资料、文件、泄露内容写在事实和证据中，不申报资产；不得把文件名中的点号当成发现域名。框架、认证模块等技术组件写事实并锚定所属入口，不申报独立应用；只有已有证据确认独立系统的应用或小程序才申报并标明independent。已知对象用 asset_id 锚定；发现新对象时可同时保留来源资产编号。只提交本次实际观察及证据，不提交未验证的猜测；意图之外的实际观察也可写回，由主宰决定是否跟进。本机回环(127.0.0.1/localhost/::1)与镜等测试环境自身设施不是发现对象——对它们的观察会被拒收并记为环境观察;任务资产清单内的目标不受此限。"
	toolDescSubmitClaim         = "交卷:把你在本会话真实坐实的漏洞交出去(探索→发现→证明→反证自查完成后调用,做完即停)。结合已有全图信息确定归属：同一根因的新证据用submit_candidate_fact的claim_id补原漏洞，不重复报；独立问题正常交卷，需最新清单时用list_claims。六字段全部必写:summary=漏洞一句话(本质+影响对象);severity=自报级别(critical|high|medium|low;" + SeverityAnchor + ");evidence_refs=证明证据引用列表(工具结果里 exchange= 行给出的交换编号,通常恰 1 条主证明——直接证明结论的那条;多步组合链按复现顺序逐环排列);proving_marker=从证据原文里抄的关键证明物字符串(如拿到的手机号、返回的敏感字段值——系统会机器核验它真的出现在证据响应体里,抄不准交不进);countercheck_note=反证自查结果自由文本(做了什么检查、为什么没翻车);impact_note=影响列点(可空,可列 1~3 条;只写有依据的,有几条写几条,猜测要标明是猜测,照实进报告)。交卷后系统即时机器核验:未过会告诉你缺什么,补做后带 claim_id 重交(补考只有一次)。立判门槛:" + ClaimHarmGate + "。本机回环(127.0.0.1/localhost/::1)与镜等测试环境自身设施不是目标攻击面——锚点或证据流量指向它们的交卷会被拒收并记为环境观察;任务资产清单内的目标不受此限。"
	toolDescListClaims          = "只读查询本任务已有漏洞的编号、状态、资产和摘要。结合任务包里的全图信息确定归属；需要最新信息时查询。同一漏洞的新证据或影响补充，用submit_candidate_fact的claim_id归到原漏洞；只有独立问题才另行交卷，不以换标题、入口或新证据重复申报。"
)

// ToolEntry 工具目录条目(只读呈现用)。
type ToolEntry struct {
	Name string
	Desc string
}

// ToolCatalog 工具全集目录(名称 + 描述;P2-08f 插话快照直问用——目标
// 的工具定义进快照,模型看得懂目标在引用什么,但应答自身无工具)。
// P5-04 起含知情查询五件(search_facts/list_assets/search_trails/
// traffic 三件;查漏洞=既有 list_claims)。
func ToolCatalog() []ToolEntry {
	return []ToolEntry{
		{"bash", toolDescBash},
		{"port_probe", toolDescPortProbe},
		{"http_probe", toolDescHTTPProbe},
		{"read", toolDescRead},
		{"resource_read", toolDescResourceRead},
		{"web_search", toolDescWebSearch},
		{"web_fetch", toolDescWebFetch},
		{"list_claims", toolDescListClaims},
		{"search_facts", toolDescSearchFacts},
		{"list_assets", toolDescListAssets},
		{"search_trails", toolDescSearchTrails},
		{"traffic_search", toolDescTrafficSearch},
		{"traffic_get", toolDescTrafficGet},
		{"traffic_blob", toolDescTrafficBlob},
		{"submit_candidate_fact", toolDescSubmitCandidateFact},
		{"submit_claim", toolDescSubmitClaim},
	}
}

// ---- 受控入口工具:候选证据回流(R8) ----

// submitCandidateFactTool 虫群无写权,唯一的事实回流通道:候选 Fact
// (status=candidate)经本工具入账,正式 Fact 事件由主宰收割产生。
// detail 携带证据与 transcript_id(第二章:图上执行记录引用转录 ID)。
// ReadOnly:只写自家账本、不碰目标环境——不设审批门(防审批疲劳),
// RoE 工具过滤对它照常生效。
func candidateFactSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"statement":       map[string]any{"type": "string", "description": "新进事实陈述:一句话可复核命题,写明资产与完整定位要素(域名或 IP 起头、host:port 形态,不用裸端口/裸路径指代目标),使他人可独立重验"},
			"evidence":        map[string]any{"type": "string", "description": "支撑证据:须含可独立复核的关键原文(请求/响应片段、命令与其输出);只写结论不附原文的证据过不了检查归纳"},
			"asset_id":        map[string]any{"type": "string", "description": "关联资产节点 ID(可选;已知对象锚定用,可同时记录来源资产与新发现对象)"},
			"claim_id":        map[string]any{"type": "string", "description": "本条事实补充的已有漏洞编号（可选）。依据已有全图信息在产生时确定：同一根因的新证据、验证结果或影响补充归原漏洞，不换标题重复申报；独立问题正常交卷，普通观察省略。只关联本条事实实际支持的漏洞。"},
			"asset_relations": map[string]any{"type": "array", "maxItems": 64, "description": "本次证据直接观察到的资产关系；不是父子层级、产生来源或执行前置。from/to使用本任务资产编号；本条new_asset对象可用$new引用。已有资产之间的关系直接填编号，无需重复申报新资产。推断不得填写。", "items": map[string]any{"type": "object", "properties": map[string]any{"from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"resolves_to", "hosted_on", "related_to", "redirects_to", "same_system"}, "description": "resolves_to=前者解析到后者；hosted_on=前者部署在后者；redirects_to=实测跳转；same_system=证据确认的同系统不同入口；related_to=明确观察到的其他关联，陈述中写清含义"}}, "required": []string{"from", "to", "kind"}}},
			"result_types":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"credential", "data", "permission", "foothold", "plain", "unclassified"}}, "uniqueItems": true, "description": "本条事实实际取得的结果，可多选：credential=取得可用凭据；data=取得敏感数据；permission=实际取得访问能力；foothold=实际建立可用立足点。普通观察或未发现成果只选 plain；拿不准只选 unclassified。按本次证据判断，不按陈述出现的词判断；推断线索不能标成已取得成果。"},
			"new_asset": map[string]any{
				"type":        "object",
				"description": "新对象申报(可选;本次观察到新的任务对象时随事实申报；操作者加入后才入资产清单，asset_id可记录发现来源)",
				"properties": map[string]any{
					"class":       map[string]any{"type": "string", "enum": []string{"domain", "host", "service", "miniprogram", "app"}, "description": "域名domain、IP host、服务入口service，独立应用app或小程序miniprogram；不为背景资料建资产"},
					"title":       map[string]any{"type": "string", "description": "域名、IP、完整服务地址或独立应用标识；URL/API归到网站入口，不带HTTP方法"},
					"independent": map[string]any{"type": "boolean", "description": "仅已有观察确认是独立网站／系统时填true。组件、框架、认证模块等指纹不填；它们随事实归已有入口。"},
					"parent":      map[string]any{"type": "string", "description": "父资产节点 ID(可选；只填所属域名或服务地址本身。DNS解析、共享IP、部署关联不作为父子层级，另写asset_relations)"},
				},
				"required": []string{"class", "title"},
			},
		},
		"required": []string{"statement", "evidence"},
	}
}

func (env *unitEnv) submitCandidateFactTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_candidate_fact",
		Description: toolDescSubmitCandidateFact,
		Schema:      candidateFactSchema(),
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Statement   string                 `json:"statement"`
				Evidence    string                 `json:"evidence"`
				AssetID     string                 `json:"asset_id"`
				ClaimID     string                 `json:"claim_id"`
				ResultTypes []rings.FactResultType `json:"result_types"`
				NewAsset    *NewAssetDecl          `json:"new_asset"`
				Relations   []rings.AssetRelation  `json:"asset_relations"`
			}
			if err := decodeCandidateFactInput(in, &args); err != nil {
				return tool.Errorf(err.Error()), nil
			}
			id, err := env.submitCandidateFact(ctx, args.Statement, args.Evidence, args.AssetID, args.NewAsset, args.Relations, args.ClaimID, args.ResultTypes...)
			if err != nil {
				return tool.Errorf("提交失败: " + err.Error()), nil
			}
			status := "candidate"
			if n, err := env.Executor.Store.Node(ctx, env.TaskID, id); err == nil {
				status = n.Status
			}
			return tool.Text(fmt.Sprintf("已提交新进事实 %s(status=%s)", id, status)), nil
		},
	})
}

// NewAssetDecl 新对象声明(P4-12,问题清单 #21):虫群观察到图上没有的
// 对象时随候选事实一并声明,主宰收割时入图(source=discovered,出生
// 血缘挂发现步骤与单位)。归 swarm 定义、overmind 收割侧消费,形状
// 单一不漂移。
type NewAssetDecl struct {
	Class       string `json:"class"`                 // 通过共享资产身份校验并归一
	Title       string `json:"title"`                 // 对象标识(与图上既有资产查重)
	Parent      string `json:"parent,omitempty"`      // 父资产节点 ID(可选)
	Independent bool   `json:"independent,omitempty"` // 已有事实确认其为独立系统；组件不填
}

func decodeCandidateFactInput(in json.RawMessage, dst any) error {
	d := json.NewDecoder(strings.NewReader(string(in)))
	d.DisallowUnknownFields()
	err := d.Decode(dst)
	if err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = errors.New("一次只提交一个JSON对象")
		}
	}
	if err == nil {
		return nil
	}
	props := candidateFactSchema()["properties"].(map[string]any)
	keys := func(properties map[string]any) string {
		var names []string
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		return strings.Join(names, ", ")
	}
	asset := props["new_asset"].(map[string]any)["properties"].(map[string]any)
	return fmt.Errorf("提交参数不符: %v。可用字段: %s；new_asset字段: %s。按字段说明修正，不更换事实或证据", err, keys(props), keys(asset))
}

func (env *unitEnv) SubmitCandidateFact(ctx context.Context, statement, evidence, assetID string, newAsset *NewAssetDecl, resultTypes ...rings.FactResultType) (string, error) {
	return env.SubmitCandidateFactWithRelations(ctx, statement, evidence, assetID, newAsset, nil, resultTypes...)
}

func (env *unitEnv) SubmitCandidateFactWithRelations(ctx context.Context, statement, evidence, assetID string, newAsset *NewAssetDecl, relations []rings.AssetRelation, resultTypes ...rings.FactResultType) (string, error) {
	return env.submitCandidateFact(ctx, statement, evidence, assetID, newAsset, relations, "", resultTypes...)
}

func (env *unitEnv) submitCandidateFact(ctx context.Context, statement, evidence, assetID string, newAsset *NewAssetDecl, relations []rings.AssetRelation, claimID string, resultTypes ...rings.FactResultType) (string, error) {
	if strings.TrimSpace(statement) == "" || strings.TrimSpace(evidence) == "" {
		return "", fmt.Errorf("swarm: 新进事实须同时具备陈述与证据")
	}
	claimID = strings.TrimSpace(claimID)
	if claimID != "" {
		claim, err := env.Executor.Store.Node(ctx, env.TaskID, claimID)
		if err != nil || claim.Type != rings.NodeTestClaim || claim.Status == rings.TestClaimDiscarded || claim.Status == rings.TestClaimRefuted {
			return "", fmt.Errorf("关联漏洞不存在或已废弃、被推翻；核对claim_id，独立观察可省略该字段")
		}
	}
	if newAsset != nil {
		newAsset.Title = strings.TrimSpace(newAsset.Title)
		if newAsset.Title == "" {
			return "", fmt.Errorf("swarm: 新对象申报缺标识(title)")
		}
		class, title, _, err := rings.AssetIdentity(rings.AssetClass(strings.TrimSpace(newAsset.Class)), newAsset.Title)
		if err != nil {
			return "", fmt.Errorf("new_asset标识不符: %v；域名用domain、IP用host、服务用service。背景资料保留在statement和evidence，不填new_asset", err)
		}
		// Share identity rules with inventory/adoption, rather than inviting a
		// new classification and asking the model to retry it downstream.
		parentRef := strings.TrimSpace(newAsset.Parent)
		if rings.AssetClass(newAsset.Class) == rings.AssetAPI {
			parentRef = ""
		}
		newAsset = &NewAssetDecl{Class: string(class), Title: title, Parent: parentRef}
		if p := strings.TrimSpace(newAsset.Parent); p != "" {
			parent, err := env.Executor.Store.Node(ctx, env.TaskID, p)
			if err != nil {
				return "", fmt.Errorf("swarm: 新对象 %q 的父资产 %s: %w", newAsset.Title, p, err)
			}
			if parent.Type != rings.NodeAsset || parent.Status == rings.AssetStatusRetired {
				return "", fmt.Errorf("父资产不可用")
			}
			if err := rings.ValidateAssetParent(rings.AssetClass(newAsset.Class), newAsset.Title, parent.AssetClass, parent.Title); err != nil {
				return "", err
			}
		}
	}
	// P4-25 ① 环回自指排除:本机环回与系统自身设施(镜/面板等)不作
	// 为发现对象——锚点(新对象申报/发现型资产)或证据流量指向本机环
	// 回即不立,拒收并记为环境观察(信息不丢,供排查)。
	if newAsset != nil {
		if a, hit := env.selfRefAnchor(ctx, newAsset.Title); hit {
			env.recordEnvObservation("新进事实", a, statement, evidence)
			return "", fmt.Errorf("swarm: 新对象 %s 属本机环回(测试环境自身,镜等系统设施),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", a)
		}
	} else if assetID != "" {
		if a, hit := env.selfRefAssetAnchor(ctx, assetID); hit {
			env.recordEnvObservation("新进事实", a, statement, evidence)
			return "", fmt.Errorf("swarm: 锚点资产 %s 属本机环回(发现型,测试环境自身),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", a)
		}
	}
	if e, hit := env.selfRefEvidence(ctx, evidence); hit {
		env.recordEnvObservation("新进事实", e, statement, evidence)
		return "", fmt.Errorf("swarm: 证据流量指向本机环回 %s(测试环境自身,镜等系统设施),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", e)
	}
	id := shortID("f")
	detail := map[string]any{
		"transcript_id": env.TranscriptID,
		"unit_id":       env.UnitID,
	}
	if claimID != "" {
		detail["claim_id"] = claimID
	}
	for _, kind := range resultTypes {
		if !rings.ValidFactResult(kind) {
			return "", fmt.Errorf("事实结果类型无效")
		}
	}
	detail["result_types"] = rings.NormalizeFactResults(resultTypes)
	if err := rings.ValidateFactResult(detail); err != nil {
		return "", err
	}
	if ref, head := env.Executor.Blobs.spill(evidence); ref != "" {
		detail["evidence_ref"], detail["evidence_head"] = ref, head
	} else {
		detail["evidence"] = evidence
	}
	if newAsset != nil {
		detail["new_asset"] = map[string]any{
			"class":       strings.TrimSpace(newAsset.Class),
			"title":       newAsset.Title,
			"parent":      strings.TrimSpace(newAsset.Parent),
			"independent": newAsset.Independent,
		}
	}
	if len(relations) > 0 {
		detail["asset_relations"] = relations
		if err := env.Executor.Store.ValidateAssetRelations(ctx, env.TaskID, detail); err != nil {
			return "", err
		}
		clean, err := rings.AssetRelationsFromDetail(detail)
		if err != nil {
			return "", err
		}
		detail["asset_relations"] = clean
	}
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeFact, Title: statement, Status: "candidate", Detail: detail,
		}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: env.StepID, ToNode: id,
		}},
	}
	if assetID != "" {
		if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
			return "", fmt.Errorf("swarm: 事实关联资产 %s: %w", assetID, err)
		}
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: id, ToNode: assetID},
		})
	}
	anchors := map[string]bool{assetID: true}
	for _, r := range relations {
		for _, ref := range []string{strings.TrimSpace(r.From), strings.TrimSpace(r.To)} {
			if ref == "$new" || anchors[ref] {
				continue
			}
			anchors[ref] = true
			events = append(events, rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: id, ToNode: ref}})
		}
	}
	if _, err := env.appendBusiness(ctx, events...); err != nil {
		return "", fmt.Errorf("swarm: 新进事实写入: %w", err)
	}
	env.Executor.factSubmitted(env.TaskID)
	return id, nil
}

// listClaimsTool 虫群查重只读能力(P2-08f):报 TestClaim 前可查已报声明
// 清单,防重复报同一漏洞浪费整轮反证(只读不违 R8)。描述即规则(§11):
// 查重要求埋在工具描述与 submit_test_claim 描述里,模型决定提交的瞬间
// 读到的就是规则。
func (env *unitEnv) listClaimsTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "list_claims",
		Description: toolDescListClaims,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"asset_id": map[string]any{"type": "string", "description": "只看某资产名下的判断(可选;空 = 全部)"},
			},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				AssetID string `json:"asset_id"`
			}
			_ = json.Unmarshal(in, &args)
			lines, err := env.ListClaims(ctx, strings.TrimSpace(args.AssetID))
			if err != nil {
				return tool.Errorf("查询已报清单失败: " + err.Error()), nil
			}
			if len(lines) == 0 {
				return tool.Text("已报判断清单为空(本任务尚无任何漏洞判断)。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "已报判断 %d 条:\n", len(lines))
			for _, l := range lines {
				b.WriteString(l)
				b.WriteByte('\n')
			}
			return tool.Text(b.String()), nil
		},
	})
}

// ListClaims 已报声明清单(只读):TestClaim 节点 × about 边资产锚点,
// 按锚定资产过滤;每条一行"ID[状态] 资产 · 声明摘要"。状态词走
// rings.ClaimStatusWord(显示层合并口径,P4-28,与面板同词)。
func (env *unitEnv) ListClaims(ctx context.Context, assetID string) ([]string, error) {
	claims, err := env.Executor.Store.NodesByType(ctx, env.TaskID, rings.NodeTestClaim, "")
	if err != nil {
		return nil, err
	}
	assets := map[string]bool{}
	if assetID != "" {
		if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
			return nil, fmt.Errorf("资产 %s: %w", assetID, err)
		}
		assets[assetID] = true
	}
	claimAsset := map[string]string{}
	if edges, err := env.Executor.Store.Edges(ctx, env.TaskID, rings.EdgeAbout); err == nil {
		for _, e := range edges {
			claimAsset[e.From] = e.To
		}
	}
	assetTitle := map[string]string{}
	var out []string
	for _, c := range claims {
		aID := claimAsset[c.ID]
		if len(assets) > 0 && !assets[aID] {
			continue
		}
		if aID != "" {
			if _, ok := assetTitle[aID]; !ok {
				if a, err := env.Executor.Store.Node(ctx, env.TaskID, aID); err == nil {
					assetTitle[aID] = a.Title
				} else {
					assetTitle[aID] = aID
				}
			}
		}
		label := aID
		if t := assetTitle[aID]; t != "" {
			label = t
		}
		out = append(out, fmt.Sprintf("%s[%s] %s · %s", c.ID, rings.ClaimStatusWord(c.Status), label, clipLine(c.Title, 160)))
	}
	return out, nil
}

// duplicateClaim 查重:同资产上存在归一后存在的性声明相同的已报声明
// (任何状态都算已报——被反证推翻的也一样,重复报同样浪费)即命中。
func (env *unitEnv) duplicateClaim(ctx context.Context, assetID, statement string) (string, string, bool) {
	claims, err := env.Executor.Store.NodesByType(ctx, env.TaskID, rings.NodeTestClaim, "")
	if err != nil {
		return "", "", false // 查重读失败不挡提交(与限量核查同款 fail-open)
	}
	claimAsset := map[string]string{}
	if edges, err := env.Executor.Store.Edges(ctx, env.TaskID, rings.EdgeAbout); err == nil {
		for _, e := range edges {
			claimAsset[e.From] = e.To
		}
	}
	want := rings.NormalizeClaimText(statement)
	for _, c := range claims {
		if claimAsset[c.ID] != assetID {
			continue
		}
		if rings.NormalizeClaimText(c.Title) == want && want != "" {
			return c.ID, rings.ClaimStatusWord(c.Status), true
		}
	}
	return "", "", false
}

// submitClaimTool 交卷受控入口(P5-01②,单会话连打交卷):虫群在同意图
// 的同一会话内完成探索→发现→证明→反证自查后,经本工具交出六字段交卷单;
// 反证自查是提示词自觉(无结构化强制字段),交卷门槛=本次真实触发过、
// 拿到可复现证据。声明节点(candidate)与交卷单事件(testclaim.record
// submit)同批落账;落账后同步触发机器三查(交卷即查,毫秒-秒级)——
// 结果随工具反馈回给本会话:全过=已确认;未过=失败原因(缺哪样)注入,
// 补做后带 claim_id 重交(上限 1 次,再不过落证据不足)。
// ReadOnly:同 submit_candidate_fact——只写自家账本,不设审批门,
// RoE 过滤照常生效。查重:交卷前先 list_claims,同资产同声明的重复
// 交卷被入口直接拒收(带 claim_id 的补考重交除外——同单更新)。
// 环回自指排除:锚点或证据流量指向本机环回(镜/面板等系统设施)
// 不立,拒收并记环境观察。
func (env *unitEnv) submitClaimTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_claim",
		Description: toolDescSubmitClaim,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string", "description": "简短说明实际问题与影响对象，必要的访问前提与结论范围保留；对象由 asset_id 定位，精确请求、载荷与完整响应由 evidence_refs 提供。" + StructuredFieldStyle},
				"severity": map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}, "description": "只为已坐实且有实际影响的漏洞定级;" + SeverityAnchor},
				"evidence_refs": map[string]any{
					"type":        "array",
					"description": "证明证据引用列表:工具结果里 exchange= 行给出的交换编号;通常恰 1 条主证明——直接证明结论的那条;多步组合链按复现顺序逐环排列每环引用",
					"items":       map[string]any{"type": "string", "description": "交换编号(如 ms-ab12cd.3)"},
				},
				"proving_marker":    map[string]any{"type": "string", "description": "关键证明物字符串:从主证据响应体原文里抄的关键值(拿到的手机号、返回的敏感字段值等)——机器按子串匹配核验它出现在证据响应体中"},
				"countercheck_note": map[string]any{"type": "string", "description": "反证自查结果自由文本:交前自己再想想(缓存假象?权限本来就有?环境因素?)——做了什么检查、为什么没翻车,如实写"},
				"impact_note":       map[string]any{"type": "string", "description": "影响列点(可选;可列 1~3 条,每条一行)。三规矩:只写有依据的;有几条写几条;猜测要标明是猜测。照实进报告"},
				"asset_id":          map[string]any{"type": "string", "description": "资产锚点:指向的资产节点 ID"},
				"vulnclass":         map[string]any{"type": "string", "description": "漏洞类型(可选,如 信息暴露/弱口令/注入)"},
				"claim_id":          map[string]any{"type": "string", "description": "补考重交时必带:机器核验未过反馈里给出的交卷单号——同单补做后重交(上限 1 次);新交卷不填"},
				"source_refs":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "实际依据的资产、意图、已确认事实或已确认漏洞编号。只记录来源，不替代证明证据；没有明确引用时省略"},
			},
			"required": []string{"summary", "severity", "evidence_refs", "proving_marker", "countercheck_note", "asset_id"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Summary          string   `json:"summary"`
				Severity         string   `json:"severity"`
				EvidenceRefs     []string `json:"evidence_refs"`
				ProvingMarker    string   `json:"proving_marker"`
				CountercheckNote string   `json:"countercheck_note"`
				ImpactNote       string   `json:"impact_note"`
				AssetID          string   `json:"asset_id"`
				Vulnclass        string   `json:"vulnclass"`
				ClaimID          string   `json:"claim_id"`
				SourceRefs       []string `json:"source_refs"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			id, check, err := env.SubmitClaim(ctx, args.Summary, args.Severity, args.EvidenceRefs, args.ProvingMarker,
				args.CountercheckNote, args.ImpactNote, args.AssetID, args.Vulnclass, args.ClaimID, args.SourceRefs...)
			if err != nil {
				return tool.Errorf("交卷失败: " + err.Error()), nil
			}
			return tool.Text(submitClaimFeedback(id, check)), nil
		},
	})
}

// submitClaimFeedback 交卷工具反馈(机器核验结果注入交卷会话;交卷即查
// 未接(无在跑主宰循环)时按"待机器核验"回话——收割兜底轮重查)。
func submitClaimFeedback(id string, check *ClaimCheckResult) string {
	if check == nil {
		return fmt.Sprintf("交卷单 %s 已记录(六字段齐全,待机器核验)", id)
	}
	switch check.Status {
	case "confirmed":
		return fmt.Sprintf("交卷单 %s 机器核验通过,已确认", id)
	case "insufficient":
		return fmt.Sprintf("交卷单 %s 补考仍未通过:%s。已记证据不足,本条到此为止", id, check.Reason)
	default:
		return fmt.Sprintf("交卷单 %s 机器核验未过:%s。缺什么补什么,补做后重交:submit_claim 带 claim_id=%s,六字段重新写全(补考只有一次)", id, check.Reason, id)
	}
}

// SubmitClaim 交卷受控入口(P5-01②;P5-02 补考重交与交卷即查):
// 声明 TestClaim 节点(status=candidate)+ 血缘边 derived_from
// (声明→执行意图 Step)+ 资产锚点边 about(声明→资产)+ 交卷单事件
// testclaim.record(submit)一批落账;落账后同步触发机器三查(Executor
// 的 ClaimCheck 钩子,毫秒-秒级,结果随工具反馈回注入本会话——三查
// 不过即补考反馈)。claimID 非空为补考重交:同单第二次交换单(不查重
// ——同单更新;上限 1 次,战役内已有两单即拒)。detail 冻结:判定
// 标准(R2 契约②,创建时冻结变更须走新事件——查三依据)、自报级别、
// 交卷单位与步骤(血缘可查)。入口校验:六字段齐全(impact_note 可空)、
// 级别词表受限、evidence_refs 非空、资产锚点存在;引用命中与
// proving_marker 核验归机器三查,提交侧只收不拦(直连形态可引用 URL
// 字符串,查一回查)。
func (env *unitEnv) SubmitClaim(ctx context.Context, summary, severity string, evidenceRefs []string,
	provingMarker, countercheckNote, impactNote, assetID, vulnclass, claimID string, sourceRefs ...string) (string, *ClaimCheckResult, error) {
	if strings.TrimSpace(summary) == "" {
		return "", nil, fmt.Errorf("swarm: 交卷单缺 summary(漏洞一句话:本质+影响对象)")
	}
	severity = strings.TrimSpace(strings.ToLower(severity))
	if severity == "none" {
		return "", nil, fmt.Errorf("信息性观察不立漏洞。请用submit_candidate_fact提交实际观察及证据；不要改级别重交。普通核对完成后正常结束意图，无需交漏洞单")
	}
	if !rings.IsValidSubmitSeverity(severity) {
		return "", nil, fmt.Errorf("swarm: 交卷自报级别必须是 critical|high|medium|low 之一,得到 %q", severity)
	}
	refs := make([]string, 0, len(evidenceRefs))
	seen := map[string]bool{}
	for _, r := range evidenceRefs {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		refs = append(refs, r)
	}
	if len(refs) == 0 {
		return "", nil, fmt.Errorf("swarm: 交卷单缺 evidence_refs(证明证据引用:工具结果里 exchange= 行给出的交换编号)")
	}
	if strings.TrimSpace(provingMarker) == "" {
		return "", nil, fmt.Errorf("swarm: 交卷单缺 proving_marker(从证据原文里抄的关键证明物)")
	}
	if strings.TrimSpace(countercheckNote) == "" {
		return "", nil, fmt.Errorf("swarm: 交卷单缺 countercheck_note(反证自查结果:做了什么检查、为什么没翻车)")
	}
	if strings.TrimSpace(assetID) == "" {
		return "", nil, fmt.Errorf("swarm: 交卷必须锚定具体资产(asset_id)")
	}
	if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
		return "", nil, fmt.Errorf("swarm: 交卷锚定资产 %s: %w", assetID, err)
	}
	retake := strings.TrimSpace(claimID)
	var addedSources []rings.Event
	if retake != "" {
		// 补考重交:同单更新——声明在候选态、战役内恰一单(上限 1 次)。
		n, err := env.Executor.Store.Node(ctx, env.TaskID, retake)
		if err != nil {
			return "", nil, fmt.Errorf("swarm: 补考重交查交卷单 %s: %w", retake, err)
		}
		if n.Type != rings.NodeTestClaim {
			return "", nil, fmt.Errorf("swarm: 节点 %s 是 %s 不是交卷单", retake, n.Type)
		}
		if n.Status != rings.TestClaimCandidate {
			return "", nil, fmt.Errorf("swarm: 交卷单 %s 当前状态 %s,只有机器核验未过待补考的交卷单可重交", retake, n.Status)
		}
		submits, reopened, err := env.campaignSubmission(ctx, retake)
		if err != nil {
			return "", nil, err
		}
		if submits >= 2 {
			return "", nil, fmt.Errorf("swarm: 交卷单 %s 补考已用(上限 1 次),不得再交", retake)
		}
		if submits == 0 && !reopened {
			return "", nil, fmt.Errorf("swarm: 交卷单 %s 战役内无交换单,不是补考重交形态", retake)
		}
		if len(sourceRefs) > 0 {
			existing, err := env.Executor.Store.Edges(ctx, env.TaskID, rings.EdgeSourcedFrom)
			if err != nil {
				return "", nil, err
			}
			known := map[string]bool{}
			for _, e := range existing {
				if e.From == retake {
					known[e.To] = true
				}
			}
			var fresh []string
			for _, ref := range sourceRefs {
				if !known[strings.TrimSpace(ref)] {
					if !reopened || submits != 0 {
						return "", nil, fmt.Errorf("本轮补做沿用首次交卷的来源引用，新增依据须重开后提交")
					}
					fresh = append(fresh, ref)
				}
			}
			addedSources, err = env.Executor.Store.SourceEvents(ctx, env.TaskID, retake, "swarm:"+env.UnitID, fresh)
			if err != nil {
				return "", nil, err
			}
		}
	} else {
		// 新交卷查重(P2-08f 既有):同资产 + 归一后相同的声明 = 重复交
		// 同一漏洞,直接拒收;模型读到拒收原因后自行核对已报清单。
		if dupID, dupStatus, dup := env.duplicateClaim(ctx, assetID, summary); dup {
			return "", nil, fmt.Errorf("swarm: 与已报漏洞 %s[%s] 重复:同一资产上同一漏洞不得重复交；先用 list_claims 核对已有结果，补做按反馈使用 claim_id 重交", dupID, dupStatus)
		}
	}
	// P4-25 ① 环回自指排除(既有):锚点是发现型环回资产、或证据引用
	// 命中的交换指向本机环回(镜/面板等系统设施),不立声明——拒收并
	// 记环境观察。
	if a, hit := env.selfRefAssetAnchor(ctx, assetID); hit {
		env.recordEnvObservation("交卷", a, summary, provingMarker)
		return "", nil, fmt.Errorf("swarm: 锚点资产 %s 属本机环回(发现型,测试环境自身),不立漏洞;观察已记为环境观察供排查,任务目标不受影响", a)
	}
	if e, hit := env.selfRefEvidenceRef(ctx, refs); hit {
		env.recordEnvObservation("交卷", e, summary, strings.Join(refs, ","))
		return "", nil, fmt.Errorf("swarm: 证据引用指向本机环回 %s(测试环境自身,镜等系统设施),不立漏洞;观察已记为环境观察供排查,任务目标不受影响", e)
	}
	var events []rings.Event
	if retake == "" {
		id := shortID("tc")
		detail := map[string]any{
			"unit_id":       env.UnitID,
			"step_id":       env.StepID,
			"transcript_id": env.TranscriptID,
			"severity":      severity,
			"standard":      arbiter.DefaultClaimStandard(),
		}
		if vulnclass != "" {
			detail["vulnclass"] = vulnclass
		}
		events = []rings.Event{
			{Kind: rings.EventNodeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.NodeAdded{
				NodeID: id, NodeType: rings.NodeTestClaim, Title: summary, Status: rings.TestClaimCandidate, Detail: detail,
			}},
			{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeDerivedFrom, FromNode: id, ToNode: env.StepID,
			}},
			{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeAbout, FromNode: id, ToNode: assetID,
			}},
		}
		claimID = id
		sources, err := env.Executor.Store.SourceEvents(ctx, env.TaskID, id, "swarm:"+env.UnitID, sourceRefs)
		if err != nil {
			return "", nil, err
		}
		events = append(events, sources...)
	}
	if retake != "" {
		derived, err := env.Executor.Store.Edges(ctx, env.TaskID, rings.EdgeDerivedFrom)
		if err != nil {
			return "", nil, err
		}
		linked := false
		for _, edge := range derived {
			if edge.From == claimID && edge.To == env.StepID {
				linked = true
				break
			}
		}
		if !linked {
			events = append(events, rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: env.StepID}})
		}
	}
	events = append(events, addedSources...)
	// 新交与重交共用交换单；后者只补真实执行血缘，不重建节点。
	events = append(events, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:" + env.UnitID, Payload: rings.TestClaimRecord{
		ClaimID: claimID, StepID: env.StepID, UnitID: env.UnitID, Type: rings.TestClaimRecSubmit,
		Summary: strings.TrimSpace(summary), Severity: severity, EvidenceRefs: refs,
		ProvingMarker: strings.TrimSpace(provingMarker), CountercheckNote: strings.TrimSpace(countercheckNote),
		ImpactNote: strings.TrimSpace(impactNote),
	}})
	if _, err := env.appendBusiness(ctx, events...); err != nil {
		return "", nil, fmt.Errorf("swarm: 交卷单写入: %w", err)
	}
	// 交卷即查(P5-02②):交卷事件落账后同步触发机器三查(毫秒-秒级,
	// 不经收割轮);判定与落账在钩子侧(主宰循环),结果回传本会话。
	// 钩子未接(无在跑循环)按待核验回话,收割兜底轮重查;钩子失败
	// 如实回错——交换单已入账,单位按待核验处理(勿重复交卷)。
	check, cerr := env.Executor.claimCheck(ctx, env.TaskID, claimID)
	if cerr != nil {
		return claimID, nil, fmt.Errorf("swarm: 交卷单 %s 已记录,机器核验暂未完成(%s):按待核验处理,勿重复交卷", claimID, cerr.Error())
	}
	return claimID, check, nil
}

// campaignSubmitCount 声明当前战役(reopen 清边界)内的交换单数
// (补考上限判定)。
func (env *unitEnv) campaignSubmission(ctx context.Context, claimID string) (int, bool, error) {
	evs, err := env.Executor.Store.Events(ctx, env.TaskID, 0, 0)
	if err != nil {
		return 0, false, err
	}
	campaignRev := int64(0)
	n := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID {
			continue
		}
		switch r.Type {
		case rings.TestClaimRecReopen:
			campaignRev = ev.Revision
			n = 0
		case rings.TestClaimRecSubmit:
			if ev.Revision > campaignRev {
				n++
			}
		}
	}
	return n, campaignRev > 0, nil
}

func (env *unitEnv) toolset() []tool.CoreTool {
	if env.retest != nil {
		return env.retestToolset()
	}
	return []tool.CoreTool{
		env.bashTool(),
		env.portProbeTool(),
		env.httpProbeTool(),
		env.readTool(),
		env.resourceReadTool(),
		env.webSearchTool(),
		env.webFetchTool(),
		env.listClaimsTool(),
		env.searchFactsTool(),
		env.listAssetsTool(),
		env.searchTrailsTool(),
		TrafficSearchTool(env.Executor.Store, env.TaskID),
		TrafficGetTool(env.Executor.Store, env.Executor.Blobs, env.TaskID),
		TrafficBlobTool(env.Executor.Store, env.Executor.Blobs, env.TaskID),
		env.submitCandidateFactTool(),
		env.submitClaimTool(),
	}
}

// filterTools 经 RoE 过滤工具全集(Q7:经 RoE 过滤的工具发现边界)。
func filterTools(tools []tool.CoreTool, roe RoE) []tool.CoreTool {
	var out []tool.CoreTool
	for _, t := range tools {
		if roe.Allows(t.Name()) {
			out = append(out, t)
		}
	}
	return out
}

func clipTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

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
	Executor     *Executor
	TaskID       string
	StepID       string
	UnitID       string
	TranscriptID string
	mirror       *mirror.Session // 本单位的镜会话(nil=无镜直连,P1 行为);接管对工具透明

	// P2-06b 三级处置 v2 的执行段状态(单 goroutine 顺序使用:审批回调
	// 在引擎循环内、与 drive 同 goroutine,无并发)。
	segCancel    context.CancelFunc // 当前执行段的取消句柄(弹门挂起/拒绝熔断/暂停挂起收段用)
	park         *GateTicket        // 挂起中的弹门票(非 nil = 当前段因弹门收拢,drive 停在票上等裁决)
	allowance    *gateAllowance     // 批准续跑的单次放行配额(重试同一调用时消费)
	bypassPass   *gateGroupPass     // P4-17 代理旁路组单位级放行(该组批准一次后武装,销毁即失效)
	stepPass     *gateStepPass      // P4-27 ⑧ 同步骤放行(审批门槛批准一次后武装,本步骤后续命令不再逐条弹门)
	deniedReason string             // 拒绝即熔断:非空 = 单位以 approval_denied 终态收场

	// P4-44 范围判定的 DNS 缓存(种子给 IP 的闭包下,域名目标按解析到
	// 闭包 IP 判同机):单位生命周期内 host → 是否在闭包内,解析一次
	// 不再重复(并行工具调用下加锁)。
	scopeMu    sync.Mutex
	scopeHits  map[string]bool
	scopeTried map[string]bool

	// P2-08 单位暂停/任务冬眠的段状态(暂停请求由 guard 在调用边界置位,
	// drive 消费;审批挂起锚点在进票时记录)。
	parkArmed      bool          // 工具边界已请求挂起(单位暂停/任务冬眠):段收拢后回段循环顶挂起
	ticketRev      int64         // 弹门挂起时的图版本(等待期间冬眠过的,R14 复检锚点)
	ticketGen      uint64        // 弹门挂起时的冬眠代数(代数前进 = 等过一场冬眠)
	deadlineCredit time.Duration // 冬眠时长抵扣(唤醒续跑段的任务收尾线顺延量)

	// resumed 原会话续(P2-06f 重启收养 + P3-05c ① 受阻重开 + P3-07 断点
	// 续接,第七章「恢复粒度」):本单位领取的步骤存在可续会话——首段经
	// 转录 Resume 原会话原地续跑(与冬眠唤醒同款),续跑指令为短注入,
	// 任务包原文已在会话历史里。用量增量基线同步锚到恢复出的累计用量
	//(token 表不重复计历史)。resumeKind 区分可续会话的来源,续跑指令
	// 措辞随之不同;转录缺失/为空时在首段续跑处降级回全新会话接力。
	resumed    bool
	resumeKind string // ""(全新)| adoption(崩溃残留)| blocked(受阻保留)| checkpoint(断点存档)

	// oneShot 单发调用路径(P3-05:研判后放行的合成单位):没有后续会话
	// 去读后台输出,bash 超时即杀、不转后台。
	oneShot bool

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
			// 单发路径(研判后放行)不转后台:没有后续会话去读输出,
			// 超时即杀是唯一诚实话义。
			if args.RunInBackground && !env.oneShot {
				return env.runBashBG(args.Command, dir)
			}
			return env.runBashFG(ctx, args.Command, dir, bashTimeoutOf(args.TimeoutMS))
		},
	})
}

// runBashFG 前台执行:限时内完成即合并输出返回;超时且可转后台(非
// sleep 类、非单发路径)则进程脱手继续跑、输出续写文件;ctx 取消
// (段收拢/单位硬停)杀整组进程。
func (env *unitEnv) runBashFG(ctx context.Context, command, dir string, timeout time.Duration) (tool.Result, error) {
	p, err := env.startBash(command, dir)
	if err != nil {
		return tool.Errorf("启动命令失败: " + err.Error()), nil
	}
	// finish 前台完成/取消的收尾:删文件(输出走返回文本)、拼退出注记、
	// 大输出入 blob(R18)。
	finish := func() tool.Result {
		p.f.Close()
		out, _ := os.ReadFile(p.outPath)
		os.Remove(p.outPath)
		text := string(out)
		if p.waitErr != nil {
			text += fmt.Sprintf("\n[exit: %v]", p.waitErr)
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
		if env.oneShot || !bashAutoBackgroundAllowed(command) {
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
			resp, err := env.httpClient().Do(req)
			if err != nil {
				return tool.Errorf("请求失败: " + err.Error()), nil
			}
			defer resp.Body.Close()
			var body string
			if method == http.MethodGet {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, httpProbeBodyClip))
				body = string(b)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "url=%s method=%s status=%d\n", u.String(), method, resp.StatusCode)
			fmt.Fprintf(&b, "server=%s content-type=%s content-length=%s\n",
				resp.Header.Get("Server"), resp.Header.Get("Content-Type"), resp.Header.Get("Content-Length"))
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
	toolDescBash                   = "在授权范围内执行 shell 命令(单条或管道),返回合并输出。单条命令默认限时 120 秒,可用 timeout_ms 自行调整(上限 600 秒);超时未完成的命令自动转入后台继续运行(输出落文件可稍后读取,sleep 类等待命令除外),也可用 run_in_background 直接后台跑。后台命令输出上限 100MB(超出省略)、最长运行 30 分钟。大输出会被存档并截断,需要全文时按存档引用查阅。"
	toolDescPortProbe              = "只读探测:检测 host:port 的 TCP 开放状态并尝试读取服务 banner。banner 只取前 256 字节且可能静默——勿据此认为已测完;需要更多服务特征时改用 http_probe 或 bash 通道。"
	toolDescHTTPProbe              = "只读 HTTP 探测:对指定 URL 发起 HTTP 请求(GET 或 HEAD),返回状态码、关键响应头(Server/Content-Type/Content-Length)与响应体开头内容。响应体只返回前 1536 字节——勿据此认为已读全,需要完整响应时改用 bash(curl)并注意该意图的审批分级。"
	toolDescSubmitCandidateFact    = "提交一条待确认事实(你本次真实观察到的结论)。须经主宰检查归纳后才成为正式事实。陈述须有本次执行取得的证据。观察到图上没有的对象(新域名/主机/服务/接口/泄漏信息等)时,随本条事实一并申报 new_asset(12 类选型+标识)——检查归纳时对象入图,后续工作可锚定它;已知对象用 asset_id 锚定,两者不得同时给。本机回环(127.0.0.1/localhost/::1)与镜等测试环境自身设施不是发现对象——对它们的观察会被拒收并记为环境观察;任务资产清单内的目标不受此限。"
	toolDescSubmitHypothesis       = "提交一条待验证的猜测(直觉/线索/疑似问题,尚未取得直接证据)。必须指向具体资产并写明依据;每个任务包最多附带 3 条。不指向本机回环上的发现型资产(测试环境自身,入口拒收)。"
	toolDescSubmitHypothesisResult = "假设验证任务专用:写回你独立验证的结构化结论。verdict=supported(成立)|refuted(不成立)|inconclusive(未能定论);须附本轮真实取得的证据;note 文本不构成结论。验证中取得的可保留观察仍走 submit_candidate_fact / submit_test_claim 正常回传。"
	toolDescSubmitTestClaim        = "提交一条待反证的漏洞判断(你本次真实触发并取得可复现证据的发现)。提交前先用 list_claims 查已报清单:同一资产上已报过的漏洞不得重复报(入口会拒收)。存在性与影响面必须分开申报;须尽力列出其他可能原因(否则系统兜底四条基线:缓存污染/不可复现/影响高估/归属错误)。判断将交由独立单位多轮反证、仲裁者机器判定,不得自评已确认、不得自报严重级别。立判门槛:" + ClaimHarmGate + "。本机回环(127.0.0.1/localhost/::1)与镜等测试环境自身设施不是目标攻击面——锚点或证据流量指向它们的判断会被拒收并记为环境观察;任务资产清单内的目标不受此限。"
	toolDescSubmitFalsifyResult    = "反证任务专用:写回你本轮独立反证的结构化结论。existence_refuted=你能否推翻存在性判断;impact_supported=影响面证据是否足以支撑所申报的影响(成立时必须给 impact_level:critical|high|medium|low);" + SeverityAnchor + "。alternatives=任务包列出的每条其他可能原因按 ID 写回 refuted(本轮是否排除)。须附本轮真实取得的证据;note 文本不参与机器判定。"
	toolDescListClaims             = "只读查询:列出本任务已报的漏洞判断清单(ID、状态、锚定资产、判断摘要)。提交 submit_test_claim 之前先查一遍——同一资产上已报过的漏洞不要重复报(重复提交会被入口直接拒收);确有新证据或不同影响面的,改写判断另立。"
)

// ToolEntry 工具目录条目(只读呈现用)。
type ToolEntry struct {
	Name string
	Desc string
}

// ToolCatalog 工具全集目录(名称 + 描述;P2-08f 插话快照直问用——目标
// 的工具定义进快照,模型看得懂目标在引用什么,但应答自身无工具)。
func ToolCatalog() []ToolEntry {
	return []ToolEntry{
		{"bash", toolDescBash},
		{"port_probe", toolDescPortProbe},
		{"http_probe", toolDescHTTPProbe},
		{"read", toolDescRead},
		{"web_search", toolDescWebSearch},
		{"web_fetch", toolDescWebFetch},
		{"list_claims", toolDescListClaims},
		{"submit_candidate_fact", toolDescSubmitCandidateFact},
		{"submit_hypothesis", toolDescSubmitHypothesis},
		{"submit_hypothesis_result", toolDescSubmitHypothesisResult},
		{"submit_test_claim", toolDescSubmitTestClaim},
		{"submit_falsify_result", toolDescSubmitFalsifyResult},
	}
}

// ---- 受控入口工具:候选证据回流(R8) ----

// submitCandidateFactTool 虫群无写权,唯一的事实回流通道:候选 Fact
// (status=candidate)经本工具入账,正式 Fact 事件由主宰收割产生。
// detail 携带证据与 transcript_id(第二章:图上执行记录引用转录 ID)。
// ReadOnly:只写自家账本、不碰目标环境——不设审批门(防审批疲劳),
// RoE 工具过滤对它照常生效。
func (env *unitEnv) submitCandidateFactTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_candidate_fact",
		Description: toolDescSubmitCandidateFact,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"statement": map[string]any{"type": "string", "description": "待确认事实陈述:一句话可复核命题,写明资产与完整定位要素(域名或 IP 起头、host:port 形态,不用裸端口/裸路径指代目标),使他人可独立重验"},
				"evidence":  map[string]any{"type": "string", "description": "支撑证据:须含可独立复核的关键原文(请求/响应片段、命令与其输出);只写结论不附原文的证据过不了检查归纳"},
				"asset_id":  map[string]any{"type": "string", "description": "关联资产节点 ID(可选;已知对象锚定用,与 new_asset 互斥)"},
				"new_asset": map[string]any{
					"type":        "object",
					"description": "新对象申报(可选;本次观察到图上没有的对象时随事实申报,检查归纳时入图;与 asset_id 互斥)",
					"properties": map[string]any{
						"class":  map[string]any{"type": "string", "enum": []string{"domain", "subdomain", "host", "service", "api", "organization", "miniprogram", "app", "wechat_account", "person", "leak_info", "other"}, "description": "对象类别(12 类选型)"},
						"title":  map[string]any{"type": "string", "description": "对象标识(域名/主机:端口/URL/接口路径等,与图上既有资产不同名)"},
						"parent": map[string]any{"type": "string", "description": "父资产节点 ID(可选;层级挂靠,如发现的子域挂所属域名)"},
					},
					"required": []string{"class", "title"},
				},
			},
			"required": []string{"statement", "evidence"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Statement string          `json:"statement"`
				Evidence  string          `json:"evidence"`
				AssetID   string          `json:"asset_id"`
				NewAsset  json.RawMessage `json:"new_asset"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			var decl *NewAssetDecl
			if len(args.NewAsset) > 0 && string(args.NewAsset) != "null" {
				decl = &NewAssetDecl{}
				if err := json.Unmarshal(args.NewAsset, decl); err != nil {
					return tool.Errorf("new_asset 申报格式不对: " + err.Error()), nil
				}
			}
			id, err := env.SubmitCandidateFact(ctx, args.Statement, args.Evidence, args.AssetID, decl)
			if err != nil {
				return tool.Errorf("提交失败: " + err.Error()), nil
			}
			return tool.Text(fmt.Sprintf("已提交待确认事实 %s(status=candidate),待主宰检查归纳", id)), nil
		},
	})
}

// NewAssetDecl 新对象声明(P4-12,问题清单 #21):虫群观察到图上没有的
// 对象时随候选事实一并声明,主宰收割时入图(source=discovered,出生
// 血缘挂发现步骤与单位)。归 swarm 定义、overmind 收割侧消费,形状
// 单一不漂移。
type NewAssetDecl struct {
	Class  string `json:"class"`            // 12 类之一(入口校验)
	Title  string `json:"title"`            // 对象标识(与图上既有资产查重)
	Parent string `json:"parent,omitempty"` // 父资产节点 ID(可选)
}

// SubmitCandidateFact 受控入口:候选 Fact 节点 + produces 血缘边
// (Step→Fact)+ 可选 about 边(Fact→Asset)。程序化调用与工具调用
// 走同一条路(R8:子代理再多也不会绕过账本)。
// P4-12:new_asset 声明(图上没有的新对象)随事实入 detail,与 asset_id
// 互斥——锚定已知对象或声明新对象,二者择一;对象本身由主宰收割时
// 入图(虫群无写权,收割通道点亮 source=discovered)。
func (env *unitEnv) SubmitCandidateFact(ctx context.Context, statement, evidence, assetID string, newAsset *NewAssetDecl) (string, error) {
	if strings.TrimSpace(statement) == "" || strings.TrimSpace(evidence) == "" {
		return "", fmt.Errorf("swarm: 待确认事实须同时具备陈述与证据")
	}
	if newAsset != nil {
		if strings.TrimSpace(assetID) != "" {
			return "", fmt.Errorf("swarm: asset_id 与 new_asset 不得同时给(锚定已知对象或申报新对象,二者择一)")
		}
		newAsset.Title = strings.TrimSpace(newAsset.Title)
		if newAsset.Title == "" {
			return "", fmt.Errorf("swarm: 新对象申报缺标识(title)")
		}
		if !rings.IsValidAssetClass(rings.AssetClass(strings.TrimSpace(newAsset.Class))) {
			return "", fmt.Errorf("swarm: 新对象 %q 的类 %q 不在 12 类词汇表内", newAsset.Title, newAsset.Class)
		}
		if p := strings.TrimSpace(newAsset.Parent); p != "" {
			if _, err := env.Executor.Store.Node(ctx, env.TaskID, p); err != nil {
				return "", fmt.Errorf("swarm: 新对象 %q 的父资产 %s: %w", newAsset.Title, p, err)
			}
		}
	}
	// P4-25 ① 环回自指排除:本机环回与系统自身设施(镜/面板等)不作
	// 为发现对象——锚点(新对象申报/发现型资产)或证据流量指向本机环
	// 回即不立,拒收并记为环境观察(信息不丢,供排查)。
	if newAsset != nil {
		if a, hit := env.selfRefAnchor(ctx, newAsset.Title); hit {
			env.recordEnvObservation("待确认事实", a, statement, evidence)
			return "", fmt.Errorf("swarm: 新对象 %s 属本机环回(测试环境自身,镜等系统设施),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", a)
		}
	} else if assetID != "" {
		if a, hit := env.selfRefAssetAnchor(ctx, assetID); hit {
			env.recordEnvObservation("待确认事实", a, statement, evidence)
			return "", fmt.Errorf("swarm: 锚点资产 %s 属本机环回(发现型,测试环境自身),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", a)
		}
	}
	if e, hit := env.selfRefEvidence(ctx, evidence); hit {
		env.recordEnvObservation("待确认事实", e, statement, evidence)
		return "", fmt.Errorf("swarm: 证据流量指向本机环回 %s(测试环境自身,镜等系统设施),不作为发现对象;观察已记为环境观察供排查,任务目标不受影响", e)
	}
	id := shortID("f")
	detail := map[string]any{
		"transcript_id": env.TranscriptID,
		"unit_id":       env.UnitID,
	}
	if ref, head := env.Executor.Blobs.spill(evidence); ref != "" {
		detail["evidence_ref"], detail["evidence_head"] = ref, head
	} else {
		detail["evidence"] = evidence
	}
	if newAsset != nil {
		detail["new_asset"] = map[string]any{
			"class":  strings.TrimSpace(newAsset.Class),
			"title":  newAsset.Title,
			"parent": strings.TrimSpace(newAsset.Parent),
		}
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
	if _, err := env.Executor.Store.Append(ctx, env.TaskID, events...); err != nil {
		return "", fmt.Errorf("swarm: 待确认事实写入: %w", err)
	}
	return id, nil
}

// MaxHypothesesPerStep 每个 Step 限量附带的假设条数(第四章 §5:防噪音)。
const MaxHypothesesPerStep = 3

// submitHypothesisTool 虫群直觉回流(第四章 §5):假设必须指向具体资产
// 并写明依据(允许弱依据);以 candidate 状态入图,主宰收割时裁决。
// 每 Step 限量附带(MaxHypothesesPerStep,防噪音)。ReadOnly:同
// submit_candidate_fact——只写自家账本,不设审批门。
func (env *unitEnv) submitHypothesisTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_hypothesis",
		Description: toolDescSubmitHypothesis,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"statement": map[string]any{"type": "string", "description": "假设陈述"},
				"asset_id":  map[string]any{"type": "string", "description": "指向的资产节点 ID(必填)"},
				"basis":     map[string]any{"type": "string", "description": "依据(允许弱依据,如配置风格相似)"},
			},
			"required": []string{"statement", "asset_id", "basis"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Statement string `json:"statement"`
				AssetID   string `json:"asset_id"`
				Basis     string `json:"basis"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			id, err := env.SubmitHypothesis(ctx, args.Statement, args.Basis, args.AssetID)
			if err != nil {
				return tool.Errorf("提交假设失败: " + err.Error()), nil
			}
			return tool.Text(fmt.Sprintf("已提交待验证的猜测 %s(status=candidate),待主宰检查归纳判定", id)), nil
		},
	})
}

// SubmitHypothesis 受控入口:候选 Hypothesis 节点(status=candidate)+
// 血缘边 produces(发现 Step→Hypothesis,P2-03 起直觉同为 Step 产出物,
// 回流轨迹"哪一步提出"全程可查)+ 资产锚点边 about(Hypothesis→Asset)。
// 每 Step 限量附带(MaxHypothesesPerStep):超出拒收,防直觉刷屏。
// 程序化调用与工具调用走同一条路(R8:子代理再多也不会绕过账本)。
func (env *unitEnv) SubmitHypothesis(ctx context.Context, statement, basis, assetID string) (string, error) {
	if strings.TrimSpace(statement) == "" || strings.TrimSpace(basis) == "" {
		return "", fmt.Errorf("swarm: 假设须同时具备陈述与依据")
	}
	if strings.TrimSpace(assetID) == "" {
		return "", fmt.Errorf("swarm: 假设必须指向具体资产(asset_id)")
	}
	if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
		return "", fmt.Errorf("swarm: 假设指向资产 %s: %w", assetID, err)
	}
	// P4-25 ① 环回自指排除:发现型环回资产(测试环境自身)不作猜测
	// 对象,拒收并记环境观察。
	if a, hit := env.selfRefAssetAnchor(ctx, assetID); hit {
		env.recordEnvObservation("猜测", a, statement, basis)
		return "", fmt.Errorf("swarm: 假设指向的资产 %s 属本机环回(发现型,测试环境自身),不作为发现对象;观察已记为环境观察供排查", a)
	}
	// 每 Step 限量:同一发现步骤名下已有 MaxHypothesesPerStep 条即拒收。
	hyps, err := env.Executor.Store.NodesByType(ctx, env.TaskID, rings.NodeHypothesis, "")
	if err != nil {
		return "", fmt.Errorf("swarm: 假设限量核查: %w", err)
	}
	own := 0
	for _, h := range hyps {
		if sid, _ := h.Detail["step_id"].(string); sid == env.StepID {
			own++
		}
	}
	if own >= MaxHypothesesPerStep {
		return "", fmt.Errorf("swarm: 本步骤已附 %d 条假设(每步上限 %d),不再收直觉——把最值得跟的留到假设里", own, MaxHypothesesPerStep)
	}
	id := shortID("h")
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeHypothesis, Title: statement, Status: rings.HypothesisCandidate,
			Detail: map[string]any{
				"basis": basis, "transcript_id": env.TranscriptID,
				"unit_id": env.UnitID, "step_id": env.StepID,
			},
		}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: env.StepID, ToNode: id,
		}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: id, ToNode: assetID,
		}},
	}
	if _, err := env.Executor.Store.Append(ctx, env.TaskID, events...); err != nil {
		return "", fmt.Errorf("swarm: 假设写入: %w", err)
	}
	return id, nil
}

// submitHypothesisResultTool 验证单位写回假设结构化结论的受控入口
// (P2-03):验证类 Step(主宰收割评估"值得跟"派发)专用。结论三值:
// supported(成立)/ refuted(不成立)/ inconclusive(未能定论)。
// 文本结论不构成裁决依据,必须经本入口落结构化记录。ReadOnly:同上,
// 只写自家账本。
func (env *unitEnv) submitHypothesisResultTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_hypothesis_result",
		Description: toolDescSubmitHypothesisResult,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hypothesis_id": map[string]any{"type": "string", "description": "你正在验证的假设 ID(任务包假设验证节给出)"},
				"verdict":       map[string]any{"type": "string", "enum": []string{"supported", "refuted", "inconclusive"}, "description": "验证结论"},
				"evidence":      map[string]any{"type": "string", "description": "本轮验证取得的证据:须含可独立复核的关键原文(请求/响应片段、命令与其输出),只写结论不算证据"},
				"note":          map[string]any{"type": "string", "description": "备注(可选;不构成结论)"},
			},
			"required": []string{"hypothesis_id", "verdict", "evidence"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				HypothesisID string `json:"hypothesis_id"`
				Verdict      string `json:"verdict"`
				Evidence     string `json:"evidence"`
				Note         string `json:"note"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if err := env.SubmitHypothesisResult(ctx, args.HypothesisID, args.Verdict, args.Evidence, args.Note); err != nil {
				return tool.Errorf("写回验证结论失败: " + err.Error()), nil
			}
			return tool.Text("验证结论已记录(hypothesis.record),待主宰检查归纳迁移结论状态"), nil
		},
	})
}

// SubmitHypothesisResult 受控入口:验证结论落 hypothesis.record(result)。
// 入口校验:假设在验证中、本 Step 确为该假设的验证 Step(防跨步骤伪造)、
// R12 验证单位 ≠ 提出单位(发现者不得自证)、结论词汇受限、本 Step 尚未
// 写回(每验证步骤一次)。证据经内容寻址落 blob(R18),账本只存引用与
// 截头;Note 文本不进结论。
func (env *unitEnv) SubmitHypothesisResult(ctx context.Context, hypothesisID, verdict, evidence, note string) error {
	if strings.TrimSpace(hypothesisID) == "" {
		return fmt.Errorf("swarm: 验证结论必须指明 hypothesis_id")
	}
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("swarm: 验证结论必须附本轮证据")
	}
	if !rings.IsValidHypothesisVerdict(strings.TrimSpace(verdict)) {
		return fmt.Errorf("swarm: 验证结论必须是 supported|refuted|inconclusive 之一,得到 %q", verdict)
	}
	hyp, err := env.Executor.Store.Node(ctx, env.TaskID, hypothesisID)
	if err != nil {
		return fmt.Errorf("swarm: 验证假设 %s: %w", hypothesisID, err)
	}
	if hyp.Type != rings.NodeHypothesis {
		return fmt.Errorf("swarm: 节点 %s 是 %s 不是 hypothesis", hypothesisID, hyp.Type)
	}
	if hyp.Status != rings.HypothesisTesting {
		return fmt.Errorf("swarm: 假设 %s 当前状态 %s,不在验证中(状态机拒绝写回)", hypothesisID, hyp.Status)
	}
	step, err := env.Executor.Store.Node(ctx, env.TaskID, env.StepID)
	if err != nil {
		return fmt.Errorf("swarm: 验证步骤 %s: %w", env.StepID, err)
	}
	if kind, _ := step.Detail["kind"].(string); kind != "verify" {
		return fmt.Errorf("swarm: 步骤 %s 不是验证类步骤(detail.kind=%q)", env.StepID, kind)
	}
	if bound, _ := step.Detail["hypothesis_id"].(string); bound != hypothesisID {
		return fmt.Errorf("swarm: 步骤 %s 不是假设 %s 的验证步骤(绑定 %q)", env.StepID, hypothesisID, bound)
	}
	if proposer, _ := hyp.Detail["unit_id"].(string); proposer == env.UnitID {
		return fmt.Errorf("swarm: R12 发现者不得自证:假设 %s 由本单位(%s)提出,验证必须由不同单位执行", hypothesisID, env.UnitID)
	}
	rec := rings.HypothesisRecord{
		HypothesisID: hypothesisID, StepID: env.StepID, UnitID: env.UnitID,
		Type: rings.HypothesisRecResult, Verdict: strings.TrimSpace(verdict),
		Note: strings.TrimSpace(note),
	}
	// 每验证步骤一次写回:同 Step 已有 result 记录即拒收。
	evs, err := env.Executor.Store.Events(ctx, env.TaskID, 0, 0)
	if err != nil {
		return fmt.Errorf("swarm: 查验证写回历史: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventHypothesisRecord {
			continue
		}
		var prev rings.HypothesisRecord
		if err := json.Unmarshal(ev.Payload, &prev); err != nil {
			return fmt.Errorf("swarm: 解码历史假设记录: %w", err)
		}
		if prev.Type == rings.HypothesisRecResult && prev.HypothesisID == hypothesisID && prev.StepID == env.StepID {
			return fmt.Errorf("swarm: 假设 %s 的验证步骤 %s 已写回过结论,每步只许一次(旧结论不得改写,R1)",
				hypothesisID, env.StepID)
		}
	}
	if ref, head := env.Executor.Blobs.spill(evidence); ref != "" {
		rec.EvidenceRef, rec.EvidenceHead = ref, head
	} else {
		rec.EvidenceHead = clipLine(evidence, 2048)
	}
	if _, err := env.Executor.Store.Append(ctx, env.TaskID, rings.Event{
		Kind: rings.EventHypothesisRecord, Actor: "swarm:" + env.UnitID, Payload: rec,
	}); err != nil {
		return fmt.Errorf("swarm: 验证结论写入: %w", err)
	}
	return nil
}

// ---- 受控入口工具:TestClaim 候选与反证写回(P1-08 + P2-02,第三章 §5) ----

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

// normalizeClaimText 声明文本归一(大小写与空白形态不影响查重比对)。
func normalizeClaimText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
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
	want := normalizeClaimText(statement)
	for _, c := range claims {
		if claimAsset[c.ID] != assetID {
			continue
		}
		if normalizeClaimText(c.Title) == want && want != "" {
			return c.ID, rings.ClaimStatusWord(c.Status), true
		}
	}
	return "", "", false
}

// DefaultAlternativeLabels 竞争性替代解释基线(第三章 §5 示例定稿):
// 发现者未自带解释清单时兜底冻结,保证每条声明至少挂着这四条待检验
// 反面假设——负向认证不因发现者"想不出反例"而退化。
var DefaultAlternativeLabels = []string{
	"缓存污染:观察内容可能来自中间缓存而非目标本体",
	"不可复现:观察依赖特定时窗或一次性状态",
	"影响高估:所声称的实际影响缺乏证据支撑",
	"归属错误:观察可能来自其他资产或共用组件",
}

// BuildAlternatives 编译冻结解释清单:模型列出的照收(空白条目丢弃),
// 空则兜底基线;ID 顺序编号(alt1…),反证写回按 ID 引用。P2-06 起
// 虫群 submit_test_claim 与主宰组合声明(overmind)共用——冻结解释
// 清单的编译语义必须同一份代码,不得两处漂移。
func BuildAlternatives(labels []string) []rings.Alternative {
	var out []rings.Alternative
	add := func(l string) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, rings.Alternative{ID: fmt.Sprintf("alt%d", len(out)+1), Label: l})
		}
	}
	if len(labels) == 0 {
		for _, l := range DefaultAlternativeLabels {
			add(l)
		}
		return out
	}
	for _, l := range labels {
		add(l)
	}
	if len(out) == 0 {
		return BuildAlternatives(nil) // 全空白输入退回基线
	}
	return out
}

// submitTestClaimTool 虫群提交可证伪漏洞声明的受控入口:存在性与影响面
// 分开声明(报告级别跟影响面证据走——发现者无级别通道,级别只来自
// 反证轮的影响面证据);竞争性替代解释随创建冻结进 detail(P2-02),
// 仲裁者确认裁决只认这份清单的结构化排除结论。判定标准(full_falsify)
// 同批冻结。ReadOnly:同 submit_candidate_fact——只写自家账本,不设
// 审批门,RoE 过滤照常生效。P2-08f:提交前先 list_claims 查重,同资产
// 同声明的重复提交被入口直接拒收。
func (env *unitEnv) submitTestClaimTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_test_claim",
		Description: toolDescSubmitTestClaim,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"statement":    map[string]any{"type": "string", "description": "存在性判断:一句话可检验命题(什么可被观察到),写明定位要素使反证单位可独立重放。" + StructuredFieldStyle},
				"impact":       map[string]any{"type": "string", "description": "影响面判断(该存在性实际造成什么影响,与存在性分开)。一句话写明资产与定位要素," + StructuredFieldStyle},
				"evidence":     map[string]any{"type": "string", "description": "本次执行取得的支撑证据:须含可独立复核的关键原文(请求/响应片段、命令与其输出);只写结论不算证据"},
				"asset_id":     map[string]any{"type": "string", "description": "资产锚点:指向的资产节点 ID(必填)"},
				"vulnclass":    map[string]any{"type": "string", "description": "漏洞类型(可选,如 信息暴露/弱口令/注入)"},
				"alternatives": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "其他可能原因清单(每条一句话反面假设,如 缓存污染/不可复现/影响高估/归属错误;可选)"},
			},
			"required": []string{"statement", "impact", "evidence", "asset_id"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Statement    string   `json:"statement"`
				Impact       string   `json:"impact"`
				Evidence     string   `json:"evidence"`
				AssetID      string   `json:"asset_id"`
				Vulnclass    string   `json:"vulnclass"`
				Alternatives []string `json:"alternatives"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			id, err := env.SubmitTestClaim(ctx, args.Statement, args.Impact, args.Evidence, args.AssetID, args.Vulnclass, args.Alternatives)
			if err != nil {
				return tool.Errorf("提交判断失败: " + err.Error()), nil
			}
			return tool.Text(fmt.Sprintf("已提交待验证的判断 %s(冻结竞争解释 %d 条),待主宰派独立单位反证", id, len(BuildAlternatives(args.Alternatives)))), nil
		},
	})
}

// SubmitTestClaim 受控入口:候选 TestClaim 节点(status=candidate)+
// 血缘边 derived_from(声明→发现 Step)+ 资产锚点边 about(声明→资产)。
// detail 冻结:判定标准与竞争解释清单(仲裁者确认裁决的唯一依据,
// P2-02)、发现单位与发现步骤(R12 反证独立性核验锚点)、证据(R18:
// 大证据入 blob,账本存引用)。
func (env *unitEnv) SubmitTestClaim(ctx context.Context, statement, impact, evidence, assetID, vulnclass string, alternatives []string) (string, error) {
	if strings.TrimSpace(statement) == "" || strings.TrimSpace(impact) == "" || strings.TrimSpace(evidence) == "" {
		return "", fmt.Errorf("swarm: 漏洞判断须同时具备存在性判断、影响面判断与证据(分开申报,缺一不可)")
	}
	if strings.TrimSpace(assetID) == "" {
		return "", fmt.Errorf("swarm: 漏洞判断必须锚定具体资产(asset_id)")
	}
	if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
		return "", fmt.Errorf("swarm: 判断锚定资产 %s: %w", assetID, err)
	}
	// P2-08f 查重:同资产 + 归一后相同的存在性声明 = 重复报同一漏洞,
	// 直接拒收(防浪费整轮反证);模型读到拒收原因后自行核对已报清单。
	if dupID, dupStatus, dup := env.duplicateClaim(ctx, assetID, statement); dup {
		return "", fmt.Errorf("swarm: 与已报判断 %s[%s] 重复:同一资产上同一漏洞不得重复报;若有新证据或不同影响面,改写存在性判断另立,或引用 %s 补充(先可用 list_claims 核对清单)", dupID, dupStatus, dupID)
	}
	// P4-25 ① 环回自指排除:锚点是发现型环回资产、或证据流量指向本机
	// 环回(镜/面板等系统设施),不立判断——无判断节点产生,拒收并记
	// 环境观察(信息不丢,供排查)。种子/手动资产(本地靶标)是操作者
	// 声明的目标,不受排除。
	if a, hit := env.selfRefAssetAnchor(ctx, assetID); hit {
		env.recordEnvObservation("漏洞判断", a, statement, evidence)
		return "", fmt.Errorf("swarm: 锚点资产 %s 属本机环回(发现型,测试环境自身),不立判断;观察已记为环境观察供排查,任务目标不受影响", a)
	}
	if e, hit := env.selfRefEvidence(ctx, evidence); hit {
		env.recordEnvObservation("漏洞判断", e, statement, evidence)
		return "", fmt.Errorf("swarm: 证据流量指向本机环回 %s(测试环境自身,镜等系统设施),不立判断;观察已记为环境观察供排查,任务目标不受影响", e)
	}
	alts := BuildAlternatives(alternatives)
	rawAlts := make([]any, 0, len(alts))
	for _, a := range alts {
		rawAlts = append(rawAlts, map[string]any{"id": a.ID, "label": a.Label})
	}
	id := shortID("tc")
	detail := map[string]any{
		"unit_id":       env.UnitID,
		"step_id":       env.StepID,
		"transcript_id": env.TranscriptID,
		"impact":        impact,
		"standard":      arbiter.DefaultClaimStandard(),
		"alternatives":  rawAlts,
	}
	if vulnclass != "" {
		detail["vulnclass"] = vulnclass
	}
	if ref, head := env.Executor.Blobs.spill(evidence); ref != "" {
		detail["evidence_ref"], detail["evidence_head"] = ref, head
	} else {
		detail["evidence"] = evidence
	}
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeTestClaim, Title: statement, Status: rings.TestClaimCandidate, Detail: detail,
		}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: id, ToNode: env.StepID,
		}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + env.UnitID, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: id, ToNode: assetID,
		}},
	}
	if _, err := env.Executor.Store.Append(ctx, env.TaskID, events...); err != nil {
		return "", fmt.Errorf("swarm: 待验证判断写入: %w", err)
	}
	return id, nil
}

// submitFalsifyResultTool 反证单位写回结构化结论的受控入口(P2-02
// 完整版):存在性与影响面分开反证;竞争解释逐条按 ID 写回排除结论;
// 影响面成立时必须给出四级定级(级别跟影响面证据走的证据侧来源)。
// 文本结论不构成裁决依据,必须经本入口落结构化记录。ReadOnly:同上,
// 只写自家账本。
func (env *unitEnv) submitFalsifyResultTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "submit_falsify_result",
		Description: toolDescSubmitFalsifyResult,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim_id":          map[string]any{"type": "string", "description": "你正在反证的判断 ID(任务包反证节给出)"},
				"existence_refuted": map[string]any{"type": "boolean", "description": "是否推翻了存在性判断(true=判断不成立)"},
				"impact_supported":  map[string]any{"type": "boolean", "description": "影响面证据是否足以支撑申报的影响"},
				"impact_level":      map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}, "description": "影响面定级(impact_supported=true 时必填;定级锚:critical=系统级权限或大量敏感数据;high=管理员或敏感数据访问;medium=普通业务越权或有限敏感信息;low=需苛刻前提才有影响——级别只跟直接影响走)"},
				"alternatives": map[string]any{
					"type":        "array",
					"description": "逐条其他可能原因的结论(任务包反证节列出的 ID)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":      map[string]any{"type": "string", "description": "解释 ID(任务包反证节给出的编号)"},
							"refuted": map[string]any{"type": "boolean", "description": "本轮是否排除了该解释"},
						},
						"required": []string{"id", "refuted"},
					},
				},
				"evidence": map[string]any{"type": "string", "description": "本轮反证取得的证据:须含可独立复核的关键原文(请求/响应片段、命令与其输出),只写结论不参与机器判定"},
				"note":     map[string]any{"type": "string", "description": "备注(可选;不参与机器判定)"},
			},
			"required": []string{"claim_id", "existence_refuted", "impact_supported", "evidence"},
		},
		ReadOnly:    alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				ClaimID          string            `json:"claim_id"`
				ExistenceRefuted bool              `json:"existence_refuted"`
				ImpactSupported  bool              `json:"impact_supported"`
				ImpactLevel      string            `json:"impact_level"`
				Alternatives     []rings.AltResult `json:"alternatives"`
				Evidence         string            `json:"evidence"`
				Note             string            `json:"note"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if err := env.SubmitFalsifyResult(ctx, args.ClaimID, args.ExistenceRefuted, args.ImpactSupported,
				args.ImpactLevel, args.Alternatives, args.Evidence, args.Note); err != nil {
				return tool.Errorf("写回反证结论失败: " + err.Error()), nil
			}
			return tool.Text("反证结论已记录(testclaim.record),待仲裁者机器判定"), nil
		},
	})
}

// SubmitFalsifyResult 受控入口:反证结论落 testclaim.record(falsify_result)。
// 入口校验:声明在反证中、本 Step 确为该声明的反证 Step(防跨步骤伪造)、
// R12 反证单位 ≠ 发现单位(发现者不得自证)、本轮尚未写回(每轮一次)、
// 解释 ID ⊆ 冻结清单、影响面定级与 impact_supported 自洽(成立必填、
// 不成立不得携带)。轮次号从 step.detail.round 机器填写(模型无通道)。
// 证据经内容寻址落 blob(R18),账本只存引用与截头;仲裁者求值只吃
// 布尔、逐条解释结论与定级词汇,Note 文本不进裁决(契约④)。
func (env *unitEnv) SubmitFalsifyResult(ctx context.Context, claimID string, existenceRefuted, impactSupported bool,
	impactLevel string, alts []rings.AltResult, evidence, note string) error {
	if strings.TrimSpace(claimID) == "" {
		return fmt.Errorf("swarm: 反证结论必须指明 claim_id")
	}
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("swarm: 反证结论必须附本轮证据")
	}
	claim, err := env.Executor.Store.Node(ctx, env.TaskID, claimID)
	if err != nil {
		return fmt.Errorf("swarm: 反证判断 %s: %w", claimID, err)
	}
	if claim.Type != rings.NodeTestClaim {
		return fmt.Errorf("swarm: 节点 %s 是 %s 不是 testclaim", claimID, claim.Type)
	}
	if claim.Status != rings.TestClaimTesting {
		return fmt.Errorf("swarm: 判断 %s 当前状态 %s,不在反证中(状态机拒绝写回)", claimID, claim.Status)
	}
	step, err := env.Executor.Store.Node(ctx, env.TaskID, env.StepID)
	if err != nil {
		return fmt.Errorf("swarm: 反证步骤 %s: %w", env.StepID, err)
	}
	if bound, _ := step.Detail["testclaim_id"].(string); bound != claimID {
		return fmt.Errorf("swarm: 步骤 %s 不是判断 %s 的反证步骤(绑定 %q)", env.StepID, claimID, bound)
	}
	if discoverer, _ := claim.Detail["unit_id"].(string); discoverer == env.UnitID {
		return fmt.Errorf("swarm: R12 发现者不得自证:判断 %s 由本单位(%s)发现,反证必须由不同单位执行", claimID, env.UnitID)
	}
	// 影响面定级自洽:成立必填四级之一;不成立不得携带(级别只来自影响面证据)。
	impactLevel = strings.TrimSpace(strings.ToLower(impactLevel))
	if impactSupported {
		switch impactLevel {
		case "critical", "high", "medium", "low":
		default:
			return fmt.Errorf("swarm: impact_supported=true 时必须给出四级影响面定级(critical|high|medium|low),得到 %q", impactLevel)
		}
	} else if impactLevel != "" {
		return fmt.Errorf("swarm: impact_supported=false 时不得携带 impact_level(级别只跟影响面证据走)")
	}
	// 解释结论:ID 必须落在冻结清单内(竞争解释创建时冻结,反证不得增删)。
	frozen := map[string]bool{}
	if raw, ok := claim.Detail["alternatives"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				if id, _ := m["id"].(string); id != "" {
					frozen[id] = true
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range alts {
		if !frozen[a.ID] {
			return fmt.Errorf("swarm: 解释结论 %q 不在判断 %s 的冻结清单内(竞争解释不得增删)", a.ID, claimID)
		}
		if seen[a.ID] {
			return fmt.Errorf("swarm: 解释结论 %q 重复写回", a.ID)
		}
		seen[a.ID] = true
	}
	// 轮次号与每轮一次写回:轮次取 step.detail.round(机器填写)。
	round, _ := step.Detail["round"].(float64)
	rec := rings.TestClaimRecord{
		ClaimID: claimID, StepID: env.StepID, UnitID: env.UnitID, Type: rings.TestClaimRecFalsify,
		Round: int(round), ExistenceRefuted: existenceRefuted, ImpactSupported: impactSupported,
		ImpactLevel: impactLevel, Alternatives: alts,
		Note: strings.TrimSpace(note),
	}
	evs, err := env.Executor.Store.Events(ctx, env.TaskID, 0, 0)
	if err != nil {
		return fmt.Errorf("swarm: 查反证写回历史: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var prev rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &prev); err != nil {
			return fmt.Errorf("swarm: 解码历史反证记录: %w", err)
		}
		if prev.Type == rings.TestClaimRecFalsify && prev.ClaimID == claimID && prev.Round == rec.Round {
			return fmt.Errorf("swarm: 判断 %s 第 %d 轮已写回过结论,每轮只许一次(旧轮结论不得改写,R1)",
				claimID, rec.Round)
		}
	}
	if ref, head := env.Executor.Blobs.spill(evidence); ref != "" {
		rec.EvidenceRef, rec.EvidenceHead = ref, head
	} else {
		rec.EvidenceHead = clipLine(evidence, 2048)
	}
	if _, err := env.Executor.Store.Append(ctx, env.TaskID, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "swarm:" + env.UnitID, Payload: rec,
	}); err != nil {
		return fmt.Errorf("swarm: 反证结论写入: %w", err)
	}
	return nil
}

// toolset 工具全集(Q7 边界的全集侧):bash 通道 + 只读探测(TCP/HTTP)
// + 只读辅助(P3-03:read 本机文件 / web_search 外网搜索 / web_fetch
// 外网取文)+ 只读查重(list_claims,P2-08f)+ 五个受控回流入口。
// 经 RoE 过滤后进入会话。复测单位(P3-06)走受限集(retestToolset:
// 复测不是发现通道)。
func (env *unitEnv) toolset() []tool.CoreTool {
	if env.retest != nil {
		return env.retestToolset()
	}
	return []tool.CoreTool{
		env.bashTool(),
		env.portProbeTool(),
		env.httpProbeTool(),
		env.readTool(),
		env.webSearchTool(),
		env.webFetchTool(),
		env.listClaimsTool(),
		env.submitCandidateFactTool(),
		env.submitHypothesisTool(),
		env.submitHypothesisResultTool(),
		env.submitTestClaimTool(),
		env.submitFalsifyResultTool(),
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

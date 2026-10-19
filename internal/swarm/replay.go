package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// 重放校验(P3-07,子计划「重放校验 + 断点续接」前半):对一条声明的
// 绑定流量证据做确定性原样重放——从账本读回绑定交换的请求原文(方法/
// URL/头/体),经镜重发(新流量留痕、归因重放锚),将新响应与留痕时的
// 原响应比对(状态 + 响应头剔必变头后逐条 + 响应体逐字节),结论与逐条
// 交换事实入账。
//
// 传输层忠实度(P3-14b ②):重放客户端禁用自动 Accept-Encoding: gzip
// (否则请求多出留痕原文没有的头,经镜后响应以原始压缩字节留痕,与原
// 始的解压留痕不可比);请求头按留痕原文逐字重建(不经 Go 规范化重写,
// 原文无 User-Agent 时压制传输层缺省注入);响应对比只剔 Date 类每次
// 必变头(归一尺度:只剔必变头,剔多了抹掉真变化),字节级对比语义保留。
//
// 纪律:
//   - 零 LLM(R17 同精神):比对是确定性代码,结论词只陈述"原样重放后
//     响应是否一致",不冒充"是否已修复"的判定——修复状态轴归复测会话
//     (P3-06),重放不迁移声明状态;
//   - 与复测同款伪步骤形态(P3-06 注意事项 7):归因锚 ReplaySID
//     (replay-<claim>-rN)不进图、不经主宰派发,流量页按锚归因可回放;
//   - 重放只许绑定证据:逐条交换在账本入口物理校验(见
//     rings.validateReplayRecord),串号在结构上不可能;
//   - 全程流量经镜:无镜装配即拒绝(fail-safe,与 web_fetch 同纪律)。
//
// 结论词(rings 重放词表):
//   - reproduced 重放一致——全部绑定交换的原响应原样再现;
//   - diverged   重放有变化——至少一条交换的响应已变化(交操作者研判);
//   - unreachable 重放不可达——全部交换的请求失败(目标不可达)。

// 重放标识规约(与复测 rt-/retest- 区分,审计一眼可辨)。
const (
	replayUnitPrefix = "rp-"     // 重放执行者前缀(无会话,仅镜归因与账本 actor)
	replayStepPrefix = "replay-" // 伪步骤锚前缀
)

// ReplaySID 第 N 轮重放的归因锚:伪步骤 ID,兼镜归因键与账本记录锚。
func ReplaySID(claimID string, round int) string {
	return fmt.Sprintf("%s%s-r%d", replayStepPrefix, claimID, round)
}

// 重放发起的错误词汇(server 据此映射 HTTP 状态)。
var (
	// ErrReplayInFlight 该声明已有重放在途(同声明同时至多一轮)。
	ErrReplayInFlight = errors.New("swarm: 该声明重放校验进行中,等本轮结束再发起")
	// ErrReplayNoEvidence 声明无绑定流量证据(重放对象是绑定证据)。
	ErrReplayNoEvidence = errors.New("swarm: 该声明未绑定流量证据,无可重放对象(先在流量页绑定)")
	// ErrReplayUnavailable 重放依赖未装配(镜)。
	ErrReplayUnavailable = errors.New("swarm: 重放依赖未装配(镜缺失,面板激活主用模型后自动装配)")
)

// replayExchangeTimeout 单条交换重放的时限(重放是单请求,不是扫描)。
const replayExchangeTimeout = 30 * time.Second

// ReplayRun 一轮已登记的重放(BeginReplay 返回,DriveReplay 驱动)。
type ReplayRun struct {
	taskID  string
	claimID string
	sid     string
	round   int
	unitID  string
}

// Round 本轮轮次(自 1 起)。
func (r *ReplayRun) Round() int { return r.round }

// SID 本轮归因锚(伪步骤 ID;流量页按此归因,账本记录锚)。
func (r *ReplayRun) SID() string { return r.sid }

// ReplayRunning 该声明当前是否有活重放(进程内登记——进程重启后登记
// 清零,重放无会话,账本里只有完成的结论记录,不存在悬空收口问题)。
func (e *Executor) ReplayRunning(taskID, claimID string) bool {
	e.rpMu.Lock()
	defer e.rpMu.Unlock()
	return e.liveReplays[taskID+"\x00"+claimID]
}

// BeginReplay 登记一轮重放:前置校验(声明节点、绑定证据非空、镜装配、
// 并发互斥)→ 轮次计数(账本既有 replay 记录数 + 1)。确定性执行由
// DriveReplay 驱动(调用方异步跑)。
func (e *Executor) BeginReplay(ctx context.Context, taskID, claimID string) (*ReplayRun, error) {
	claim, err := e.Store.Node(ctx, taskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 重放声明 %s: %w", claimID, err)
	}
	if claim.Type != rings.NodeTestClaim {
		return nil, fmt.Errorf("swarm: 节点 %s 是 %s 不是 testclaim", claimID, claim.Type)
	}
	if e.Mirror == nil {
		return nil, ErrReplayUnavailable
	}
	exchanges, _, err := e.Store.ClaimEvidence(ctx, taskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 重放读绑定证据: %w", err)
	}
	if len(exchanges) == 0 {
		return nil, ErrReplayNoEvidence
	}

	e.rpMu.Lock()
	defer e.rpMu.Unlock()
	key := taskID + "\x00" + claimID
	if e.liveReplays[key] {
		return nil, ErrReplayInFlight
	}
	// 轮次计数:账本既有 replay 记录数(重放无悬空形态,完成的记录即轮次)。
	evs, err := e.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("swarm: 重放读事件流: %w", err)
	}
	round := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.Type == rings.TestClaimRecReplay && rec.ClaimID == claimID {
			round++
		}
	}
	round++

	run := &ReplayRun{
		taskID: taskID, claimID: claimID,
		sid: ReplaySID(claimID, round), round: round,
		unitID: shortID(replayUnitPrefix),
	}
	if e.liveReplays == nil {
		e.liveReplays = map[string]bool{}
	}
	e.liveReplays[key] = true
	return run, nil
}

// DriveReplay 驱动一轮重放到入账:起镜会话(归因重放锚)→ 逐条绑定
// 交换原样重发(请求原文重建:方法/URL/Host/头/体,剥逐跳头)→ 关会话
// (等留痕落账)→ 读回新交换与原交换比对 → 结论入账。确定性执行,
// 无会话无模型;活动登记在入账后注销。
func (e *Executor) DriveReplay(ctx context.Context, run *ReplayRun) error {
	key := run.taskID + "\x00" + run.claimID
	defer func() {
		e.rpMu.Lock()
		delete(e.liveReplays, key)
		e.rpMu.Unlock()
	}()

	exchanges, _, err := e.Store.ClaimEvidence(ctx, run.taskID, run.claimID)
	if err != nil {
		return fmt.Errorf("swarm: 重放读绑定证据: %w", err)
	}
	// 原始交换文档(首绑次序):从事件流找 blob 引用读回完整原文。
	originals, err := e.originalExchanges(ctx, run.taskID, exchanges)
	if err != nil {
		return err
	}

	sess, err := e.Mirror.Session(mirror.Meta{
		TaskID: run.taskID, StepID: run.sid, UnitID: run.unitID,
	})
	if err != nil {
		return fmt.Errorf("swarm: 重放起镜会话: %w", err)
	}
	client := sess.HTTPClient(replayExchangeTimeout)
	// 禁自动 gzip(P3-14b ②):传输层不再替请求添 Accept-Encoding: gzip,
	// 请求头与留痕原文逐字一致;目标响应是否压缩、镜侧如何留痕,都与
	// 原始交换走同一口径。
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.DisableCompression = true
	}
	// 单交换重放不追跳转:每条绑定证据是一跳(镜逐跳留痕),重放对齐
	// 同一跳——跳转目标的重放属另一条证据的职责(与镜出站同款口径)。
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	sent := 0
	for _, orig := range originals {
		if orig.Request == nil {
			continue // 绑定校验已拒隧道;防御性跳过(无请求原文不可重放)
		}
		if rerr := replaySend(client, orig); rerr != nil {
			// 发送侧失败也照常进行:镜侧可能有失败留痕(502),比对按事实。
			_ = rerr
		}
		sent++
	}
	_ = sess.Close() // 等在飞交换留痕落账后再读回

	// 新交换按账本序读回,与本轮重放序对齐(顺序发送,账本序即发送序)。
	news, err := mirror.Exchanges(ctx, e.Store, e.Blobs, run.taskID)
	if err != nil {
		return fmt.Errorf("swarm: 重放读回新交换: %w", err)
	}
	mine := make([]mirror.Exchange, 0, sent)
	for _, ex := range news {
		if ex.StepID == run.sid {
			mine = append(mine, ex)
		}
	}

	results := make([]rings.ReplayResult, 0, len(originals))
	i := 0
	for _, orig := range originals {
		res := rings.ReplayResult{ExchangeID: orig.ID}
		if orig.Request != nil {
			res.Method, res.URL = orig.Request.Method, orig.Request.URL
		}
		if orig.Response != nil {
			res.OldStatus = orig.Response.Status
			res.OldBytes = int64(len(orig.Response.Body))
		}
		if orig.Request == nil {
			res.Error = "原交换无请求原文(隧道/异常形态),不可重放"
			results = append(results, res)
			continue
		}
		if i < len(mine) {
			nw := mine[i]
			i++
			res.NewExchangeID = nw.ID
			if nw.Response != nil {
				res.NewStatus = nw.Response.Status
				res.NewBytes = int64(len(nw.Response.Body))
			}
			if nw.Error != "" {
				res.Error = "重放失败: " + nw.Error
			}
			if nw.Response != nil && orig.Response != nil {
				res.BodySame = bytes.Equal(nw.Response.Body, orig.Response.Body)
				res.HeadersSame = respHeadersEqual(orig.Response.Headers, nw.Response.Headers)
			}
		} else {
			res.Error = "重放未产生留痕(镜记录失败或会话异常)"
		}
		results = append(results, res)
	}

	verdict := replayVerdictOf(results)
	rec := rings.TestClaimRecord{
		ClaimID: run.claimID, StepID: run.sid, UnitID: run.unitID,
		Type: rings.TestClaimRecReplay, Round: run.round,
		ReplayVerdict: verdict, ReplayResults: results,
	}
	if _, err := e.Store.Append(ledgerCtx, run.taskID, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "swarm:" + run.unitID, Payload: rec,
	}); err != nil {
		return fmt.Errorf("swarm: 重放结论入账: %w", err)
	}
	return nil
}

// replayVerdictOf 结论推导:全部可比交换一致 → reproduced;全部请求
// 失败 → unreachable;其余(有变化/混合)→ diverged。事实在逐条结果
// 里,读侧不隐藏任何一侧。
func replayVerdictOf(results []rings.ReplayResult) string {
	failed, same := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
			continue
		}
		if r.NewStatus == r.OldStatus && r.BodySame && r.HeadersSame {
			same++
		}
	}
	if failed == len(results) {
		return rings.ReplayVerdictUnreachable
	}
	if same == len(results) {
		return rings.ReplayVerdictReproduced
	}
	return rings.ReplayVerdictDiverged
}

// volatileRespHeaders 响应对比剔除的每次必变头(P3-14b ② 归一尺度:
// 只剔必变头,剔多了抹掉真变化)。Date 由服务器逐响应生成,静态内容
// 也不可逐字复现;其余头(含 Set-Cookie/ETag 类)的变化都是真变化,
// 照实呈现给操作者研判。
var volatileRespHeaders = map[string]bool{"Date": true}

// respHeadersEqual 双侧响应头比对(名称大小写不敏感,值逐字;留痕侧
// 头按名排序且确定性,顺序天然对齐)。
func respHeadersEqual(oldH, newH []mirror.Header) bool {
	pick := func(hs []mirror.Header) []mirror.Header {
		out := make([]mirror.Header, 0, len(hs))
		for _, h := range hs {
			if !volatileRespHeaders[http.CanonicalHeaderKey(h.Name)] {
				out = append(out, h)
			}
		}
		return out
	}
	a, b := pick(oldH), pick(newH)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i].Name, b[i].Name) || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

// replaySend 按留痕原文重建请求并经镜重发一条交换:方法/URL/Host/头/体
// 原样(剥逐跳头与 Content-Length——后者由传输层按体重算)。头不经
// Go 规范化重写(留痕侧记录形态即发送形态);原文无 User-Agent 时压制
// 传输层缺省注入(逐字忠实,P3-14b ②)。响应体读弃(比对用镜留痕侧
// 的原文,不经客户端转手)。
func replaySend(client *http.Client, orig mirror.Exchange) error {
	req := orig.Request
	r, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("重建请求: %w", err)
	}
	if req.Host != "" {
		r.Host = req.Host
	}
	hasUA := false
	for _, h := range req.Headers {
		if mirror.IsHopByHopHeader(h.Name) || strings.EqualFold(h.Name, "Content-Length") {
			continue
		}
		if strings.EqualFold(h.Name, "User-Agent") {
			hasUA = true
		}
		r.Header[h.Name] = append(r.Header[h.Name], h.Value)
	}
	if !hasUA {
		// 空值条目使 Request.Write 不落缺省 Go User-Agent(键存在而值为空)。
		r.Header["User-Agent"] = []string{""}
	}
	resp, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("重放请求: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}

// originalExchanges 按交换 ID 清单从事件流读回原始交换文档(blob 原文)。
// 悬空引用(留痕被特权清除等极端情形)如实跳过——绑定事实仍在事件流,
// 比对少一条比造一条强。
func (e *Executor) originalExchanges(ctx context.Context, taskID string, ids []string) ([]mirror.Exchange, error) {
	all, err := mirror.Exchanges(ctx, e.Store, e.Blobs, taskID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 重放读原交换: %w", err)
	}
	byID := make(map[string]mirror.Exchange, len(all))
	for _, ex := range all {
		byID[ex.ID] = ex
	}
	out := make([]mirror.Exchange, 0, len(ids))
	for _, id := range ids {
		if ex, ok := byID[id]; ok {
			out = append(out, ex)
		}
	}
	return out, nil
}

package swarm

// P3-07 重放校验测试:绑定证据的确定性原样重放(零 LLM)——经镜新留痕
// 归因重放锚、原响应逐字节比对、三值结论入账(reproduced / diverged /
// unreachable);发起前置校验(无绑定证据/并发互斥/非声明节点/无镜)
// 与账本侧物理校验(重放只许绑定证据,串号入口即拒)。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// replayEnv 重放测试环境:可控响应的靶站 + 镜 + 一条已确认声明。
type replayEnv struct {
	store  *rings.Store
	exec   *Executor
	target *httptest.Server
	body   atomic.Value // string:靶站当前响应体
	status atomic.Int32 // 靶站当前响应状态
	mockH  atomic.Value // string:X-Mock 头当前值(P3-14b ② 头变化用)
	task   string
	claim  string
}

// newReplayEnv 起靶站(响应体/状态可变,模拟目标变化)并种声明。
func newReplayEnv(t *testing.T, task, claim string) *replayEnv {
	t.Helper()
	env := &replayEnv{task: task, claim: claim}
	env.body.Store("vulnerable-response-v1")
	env.status.Store(200)
	env.mockH.Store("target")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Mock", env.mockH.Load().(string))
		w.WriteHeader(int(env.status.Load()))
		_, _ = io.WriteString(w, env.body.Load().(string))
	})
	env.target = httptest.NewServer(mux)
	t.Cleanup(env.target.Close)

	dir := t.TempDir()
	env.store = openStore(t, dir)
	seedRetestableClaim(t, env.store, task, claim, "用户接口未授权可读")
	env.exec = NewExecutor(env.store, nil, filepath.Join(dir, "tasks"), Budget{})
	env.exec.Mirror = mirror.New(env.store, env.exec.Blobs, t.TempDir())
	return env
}

// makeEvidence 经镜真实发一条请求造绑定证据:返回交换 ID(留痕入账)。
func (env *replayEnv) makeEvidence(t *testing.T, path string) string {
	t.Helper()
	sess, err := env.exec.Mirror.Session(mirror.Meta{
		TaskID: env.task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
	})
	if err != nil {
		t.Fatalf("起镜会话: %v", err)
	}
	client := sess.HTTPClient(5 * time.Second)
	resp, err := client.Get(env.target.URL + path)
	if err != nil {
		t.Fatalf("造证据请求: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = sess.Close() // 等留痕落账

	var id string
	for _, rec := range mirrorRecordsOf(t, env.store, env.task) {
		if rec.StepID == "s1" && rec.BlobRef != "" {
			id = rec.ExchangeID
		}
	}
	if id == "" {
		t.Fatalf("未找到证据交换留痕")
	}
	// 绑定为声明证据(P3-02 同款入账)。
	if _, err := env.store.Append(context.Background(), env.task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "creator",
		Payload: rings.TestClaimRecord{
			ClaimID: env.claim, Type: rings.TestClaimRecEvidence,
			Exchanges: []string{id}, By: "creator",
		},
	}); err != nil {
		t.Fatalf("绑定证据失败: %v", err)
	}
	return id
}

// replayRecordsOf 声明的全部 replay 记录(入账序)。
func replayRecordsOf(t *testing.T, store *rings.Store, task, claimID string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.Type == rings.TestClaimRecReplay && rec.ClaimID == claimID {
			out = append(out, rec)
		}
	}
	return out
}

// driveReplay 同步跑一轮重放(测试不引 server 的异步驱动)。
func driveReplay(t *testing.T, env *replayEnv) {
	t.Helper()
	run, err := env.exec.BeginReplay(context.Background(), env.task, env.claim)
	if err != nil {
		t.Fatalf("BeginReplay: %v", err)
	}
	if err := env.exec.DriveReplay(context.Background(), run); err != nil {
		t.Fatalf("DriveReplay: %v", err)
	}
}

// TestReplayReproduced 重放一致:目标未变,原样重放后响应逐字节再现
// (状态 + 体),结论 reproduced 入账;新流量经镜留痕归因重放锚。
func TestReplayReproduced(t *testing.T) {
	env := newReplayEnv(t, "replay-1", "tc-rp1")
	id := env.makeEvidence(t, "/api/user")
	driveReplay(t, env)

	recs := replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 1 {
		t.Fatalf("应恰一条重放记录,得到 %d", len(recs))
	}
	rec := recs[0]
	if rec.ReplayVerdict != rings.ReplayVerdictReproduced {
		t.Fatalf("结论应为 reproduced,得到 %s(%+v)", rec.ReplayVerdict, rec.ReplayResults)
	}
	if rec.Round != 1 || rec.StepID != ReplaySID(env.claim, 1) {
		t.Fatalf("轮次/锚不符: round=%d step=%s", rec.Round, rec.StepID)
	}
	if len(rec.ReplayResults) != 1 {
		t.Fatalf("应恰一条交换结果,得到 %+v", rec.ReplayResults)
	}
	r := rec.ReplayResults[0]
	if r.ExchangeID != id || r.NewExchangeID == "" {
		t.Fatalf("结果应指回原交换并携带新交换: %+v", r)
	}
	if r.OldStatus != 200 || r.NewStatus != 200 || !r.BodySame || r.Error != "" {
		t.Fatalf("一致比对不符: %+v", r)
	}
	// 新流量经镜留痕、归因重放锚(流量页可查可回放)。
	var newRec *rings.MirrorRecord
	for i, m := range mirrorRecordsOf(t, env.store, env.task) {
		if m.ExchangeID == r.NewExchangeID {
			n := mirrorRecordsOf(t, env.store, env.task)[i]
			newRec = &n
		}
	}
	if newRec == nil {
		t.Fatalf("新交换无镜留痕")
	}
	if newRec.StepID != ReplaySID(env.claim, 1) || newRec.BlobRef == "" {
		t.Fatalf("新交换应归因重放锚并带 blob 原文: %+v", newRec)
	}
}

// TestReplayDivergedAndUnreachable 变化与不可达:目标改响应体 → diverged;
// 目标换状态码 → diverged;目标关闭 → unreachable(全部请求失败)。
func TestReplayDivergedAndUnreachable(t *testing.T) {
	env := newReplayEnv(t, "replay-2", "tc-rp2")
	env.makeEvidence(t, "/api/user")

	// 第 1 轮:响应体变化(修复后内容不同)。
	env.body.Store("fixed-response-no-leak")
	driveReplay(t, env)
	recs := replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 1 || recs[0].ReplayVerdict != rings.ReplayVerdictDiverged {
		t.Fatalf("响应变化应 diverged: %+v", recs)
	}
	if recs[0].ReplayResults[0].BodySame {
		t.Fatalf("体变化不应判一致: %+v", recs[0].ReplayResults)
	}

	// 第 2 轮:状态码变化(403)。
	env.body.Store("vulnerable-response-v1")
	env.status.Store(403)
	driveReplay(t, env)
	recs = replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 2 || recs[1].ReplayVerdict != rings.ReplayVerdictDiverged {
		t.Fatalf("状态变化应 diverged: %+v", recs)
	}
	if recs[1].Round != 2 || recs[1].ReplayResults[0].NewStatus != 403 ||
		recs[1].ReplayResults[0].OldStatus != 200 {
		t.Fatalf("第 2 轮事实不符: %+v", recs[1])
	}

	// 第 3 轮:目标不可达(关靶站)→ unreachable。
	env.target.Close()
	driveReplay(t, env)
	recs = replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 3 || recs[2].ReplayVerdict != rings.ReplayVerdictUnreachable {
		t.Fatalf("目标关闭应 unreachable: %+v", recs)
	}
	if recs[2].ReplayResults[0].Error == "" {
		t.Fatalf("不可达应有失败原因: %+v", recs[2].ReplayResults)
	}
}

// TestReplayGuards 发起前置校验:无绑定证据拒;并发互斥拒;非声明节点
// 拒;无镜拒;重放对象必须是本声明的绑定证据(账本物理校验,串号入口
// 即拒)。
func TestReplayGuards(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "replay-guard"
	seedGraph(t, store, task, "确认本地靶标暴露面")
	seedRetestableClaim(t, store, task, "tc-g1", "声明甲")
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ctx := context.Background()

	// 无镜:Unavailable。
	if _, err := ex.BeginReplay(ctx, task, "tc-g1"); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("无镜应拒 Unavailable,得到 %v", err)
	}
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())

	// 无绑定证据:NoEvidence。
	if _, err := ex.BeginReplay(ctx, task, "tc-g1"); !errors.Is(err, ErrReplayNoEvidence) {
		t.Fatalf("无证据应拒 NoEvidence,得到 %v", err)
	}
	// 非声明节点。
	if _, err := ex.BeginReplay(ctx, task, "s1"); err == nil ||
		errors.Is(err, ErrReplayNoEvidence) || errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("非声明节点应拒,得到 %v", err)
	}

	// 并发互斥:登记在飞后同声明再发起被拒,他声明不受影响。
	env := newReplayEnv(t, "replay-guard2", "tc-g2")
	env.makeEvidence(t, "/api/user")
	run, err := env.exec.BeginReplay(ctx, env.task, env.claim)
	if err != nil {
		t.Fatalf("登记重放: %v", err)
	}
	if !env.exec.ReplayRunning(env.task, env.claim) {
		t.Fatalf("在飞登记应可见")
	}
	if _, err := env.exec.BeginReplay(ctx, env.task, env.claim); !errors.Is(err, ErrReplayInFlight) {
		t.Fatalf("在飞应拒 InFlight,得到 %v", err)
	}
	_ = env.exec.DriveReplay(ctx, run)
	if env.exec.ReplayRunning(env.task, env.claim) {
		t.Fatalf("入账后登记应注销")
	}

	// 账本物理校验:重放记录携带未绑定交换 → 入口拒(串号防住)。
	env2 := newReplayEnv(t, "replay-guard3", "tc-g3")
	foreign := env2.makeEvidence(t, "/api/user") // env2 自己任务里的交换
	_ = foreign
	// 用 env2 的账本试入一条指向"别的任务交换"的记录:不同任务根本查不到,
	// 直接在本任务内造一条未绑定的 http 留痕再引用它。
	sess, err := env2.exec.Mirror.Session(mirror.Meta{TaskID: env2.task, StepID: "s9", UnitID: "u9"})
	if err != nil {
		t.Fatalf("起镜会话: %v", err)
	}
	c := sess.HTTPClient(5 * time.Second)
	resp, err := c.Get(env2.target.URL + "/api/user?unbound=1")
	if err != nil {
		t.Fatalf("造未绑定留痕: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = sess.Close()
	var unbound string
	for _, m := range mirrorRecordsOf(t, env2.store, env2.task) {
		if m.StepID == "s9" && m.BlobRef != "" {
			unbound = m.ExchangeID
		}
	}
	if unbound == "" {
		t.Fatalf("未找到未绑定留痕")
	}
	_, err = env2.store.Append(ctx, env2.task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "swarm:rp-x",
		Payload: rings.TestClaimRecord{
			ClaimID: env2.claim, StepID: "replay-" + env2.claim + "-r1", UnitID: "rp-x",
			Type: rings.TestClaimRecReplay, Round: 1, ReplayVerdict: rings.ReplayVerdictReproduced,
			ReplayResults: []rings.ReplayResult{{ExchangeID: unbound, BodySame: true}},
		},
	})
	if err == nil || !errors.Is(err, rings.ErrReplayRecordInvalid) {
		t.Fatalf("未绑定交换的重放记录应被入口拒,得到 %v", err)
	}
}

// ---- P3-14b ② 传输层忠实度 ----

// TestReplayStaticTwiceConsistent 完成标准:同一无动态内容流量原地重放
// 两次,结论必须"一致"。静态靶站每次响应只有 Date 变(必变头剔除),
// 逐字节体比对 + 头逐条比对双过 → 两轮均 reproduced。
func TestReplayStaticTwiceConsistent(t *testing.T) {
	env := newReplayEnv(t, "replay-twice", "tc-twice")
	env.makeEvidence(t, "/api/user")
	driveReplay(t, env)
	driveReplay(t, env)

	recs := replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 2 {
		t.Fatalf("两轮重放应恰 2 条记录,得到 %d", len(recs))
	}
	for i, rec := range recs {
		if rec.Round != i+1 || rec.ReplayVerdict != rings.ReplayVerdictReproduced {
			t.Fatalf("第 %d 轮应 reproduced: %+v", i+1, rec)
		}
		r := rec.ReplayResults[0]
		if !r.BodySame || !r.HeadersSame || r.NewStatus != r.OldStatus {
			t.Fatalf("第 %d 轮比对不符(体/头/状态): %+v", i+1, r)
		}
	}
}

// TestReplayHeaderChangeDiverged 头变化是真变化:归一只剔必变头——
// 静态体不变、仅 X-Mock 头值变化时,重放必须 diverged(剔多了会抹掉
// 真变化)。
func TestReplayHeaderChangeDiverged(t *testing.T) {
	env := newReplayEnv(t, "replay-hdr", "tc-hdr")
	env.makeEvidence(t, "/api/user")
	env.mockH.Store("hardened")
	driveReplay(t, env)

	recs := replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 1 || recs[0].ReplayVerdict != rings.ReplayVerdictDiverged {
		t.Fatalf("仅头变化应 diverged: %+v", recs)
	}
	r := recs[0].ReplayResults[0]
	if !r.BodySame || r.HeadersSame {
		t.Fatalf("体应一致而头应检出变化: %+v", r)
	}
}

// TestReplayTransportFidelity 请求侧逐字忠实:原始证据由不声明压缩、
// 不带 User-Agent 的裸客户端经镜产生(压缩协商由镜出站传输层自动补,
// 留痕为解压后明文);重放不得替请求添 Accept-Encoding / 缺省 UA——
// gzip 协商不对称会让镜以原始压缩字节留痕,与原始明文不可比(修复前
// 必 false-diverged);UA 注入则请求原文失真。
func TestReplayTransportFidelity(t *testing.T) {
	env := newReplayEnv(t, "replay-fid", "tc-fid")

	// 靶站按协商压缩:请求声明 gzip 才回 gzip(确定性无时间戳)。
	var gzipsServed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/plain", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gzipsServed.Add(1)
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write([]byte("plain-body-v1"))
			_ = zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(200)
			_, _ = w.Write(buf.Bytes())
			return
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "plain-body-v1")
	})
	gzTarget := httptest.NewServer(mux)
	t.Cleanup(gzTarget.Close)

	// 造证据:裸 TCP 直写镜代理形态请求(无 User-Agent、无 Accept-Encoding)。
	sess, err := env.exec.Mirror.Session(mirror.Meta{
		TaskID: env.task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
	})
	if err != nil {
		t.Fatalf("起镜会话: %v", err)
	}
	u, _ := url.Parse(gzTarget.URL)
	conn, err := net.DialTimeout("tcp", sess.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("连镜: %v", err)
	}
	_, _ = fmt.Fprintf(conn, "GET %s/api/plain HTTP/1.1\r\nHost: %s\r\n\r\n", gzTarget.URL, u.Host)
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
	_ = sess.Close()

	// 原始留痕:请求无 UA/AE;响应体是解压后的明文(镜出站自动协商 gzip
	// 并透明解压)。
	var orig mirror.Exchange
	for _, ex := range exchangesOf(t, env.store, env.exec.Blobs, env.task) {
		if ex.StepID == "s1" {
			orig = ex
		}
	}
	if orig.ID == "" {
		t.Fatalf("未找到原始证据交换")
	}
	for _, h := range orig.Request.Headers {
		if strings.EqualFold(h.Name, "User-Agent") || strings.EqualFold(h.Name, "Accept-Encoding") {
			t.Fatalf("原始请求不应携带 %s(裸客户端): %+v", h.Name, orig.Request.Headers)
		}
	}
	if !bytes.Equal(orig.Response.Body, []byte("plain-body-v1")) {
		t.Fatalf("原始留痕应为解压后明文: %q", orig.Response.Body)
	}
	if _, err := env.store.Append(context.Background(), env.task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "creator",
		Payload: rings.TestClaimRecord{
			ClaimID: env.claim, Type: rings.TestClaimRecEvidence,
			Exchanges: []string{orig.ID}, By: "creator",
		},
	}); err != nil {
		t.Fatalf("绑定证据失败: %v", err)
	}

	driveReplay(t, env)
	recs := replayRecordsOf(t, env.store, env.task, env.claim)
	if len(recs) != 1 || recs[0].ReplayVerdict != rings.ReplayVerdictReproduced {
		t.Fatalf("忠实重放应 reproduced(修复前 gzip 协商不对称必 diverged): %+v", recs)
	}

	// 重放请求侧逐字:新交换留痕同样无 UA / 无 Accept-Encoding;响应体
	// 为明文(不是 gzip 原始字节 0x1f 0x8b 开头)。
	var next mirror.Exchange
	for _, ex := range exchangesOf(t, env.store, env.exec.Blobs, env.task) {
		if ex.ID == recs[0].ReplayResults[0].NewExchangeID {
			next = ex
		}
	}
	if next.ID == "" {
		t.Fatalf("未找到重放新交换")
	}
	for _, h := range next.Request.Headers {
		if strings.EqualFold(h.Name, "User-Agent") {
			t.Fatalf("重放不得注入缺省 User-Agent: %+v", next.Request.Headers)
		}
		if strings.EqualFold(h.Name, "Accept-Encoding") {
			t.Fatalf("重放不得替请求协商压缩: %+v", next.Request.Headers)
		}
	}
	if len(next.Response.Body) < 2 || next.Response.Body[0] == 0x1f {
		t.Fatalf("重放留痕应为解压后明文(镜像侧同口径): %q", next.Response.Body)
	}
	if gzipsServed.Load() != 2 {
		t.Fatalf("靶站应两次按协商 gzip(证据转发 + 重放转发),实际 %d", gzipsServed.Load())
	}
}

// exchangesOf 全量交换文档(blob 读回)。
func exchangesOf(t *testing.T, store *rings.Store, blobs mirror.BlobSource, task string) []mirror.Exchange {
	t.Helper()
	out, err := mirror.Exchanges(context.Background(), store, blobs, task)
	if err != nil {
		t.Fatalf("读交换文档: %v", err)
	}
	return out
}

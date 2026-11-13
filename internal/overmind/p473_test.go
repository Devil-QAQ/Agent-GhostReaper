package overmind

// P4-73 验收套件(问题清单 #89,确认必有复现包):
//   - ① 引用入契约与种子直进——交换引用命中/URL 回查兜底/直连工具记录
//     组包回查三类命中通道,种子不经候选池(error/锚定/去同型过滤不适用);
//   - ② 确认门槛校验——追溯不到的证据不作为确认依据(落"证据不足,理由:
//     证据不可追溯"不确认),确认裁决带 evidence_traceable 条件行;
//   - ③ 模型职责改变——种子之上补全流程(多步链前置步骤),挑选失败/
//     模型不可用仍出种子包并如实标注"流程步骤未生成（挑选失败）";
//   - ④ 参数不同结果型(本案形态:同型 200 data:null 与含手机号并存,
//     种子=含数据那条——去同型顶掉证明交换的缺陷反转);
//   - ⑤ 重开再确认走新机制(新战役种子重解析,旧战役交换不再入选);
//   - ⑥ 六形态各至少一例 + 证明交换内容与确认证据一致(响应体含所证
//     数据)+ 账本物理校验(标注只伴随存在的包)。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p473SeedClaim 铺完整可判定的声明(冻结标准 + 解释清单 + 发现单位)+
// about 锚点边 + 发现步骤血缘(derived_from,重开反证继承分级用);status
// 起候选,随反证步骤迁移 testing。
func p473SeedClaim(t *testing.T, store *rings.Store, task, claimID, assetID string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "sd-" + claimID, NodeType: rings.NodeStep,
				Title: "发现步骤", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: claimID, NodeType: rings.NodeTestClaim,
				Title: claimID + " 存在性陈述", Status: rings.TestClaimCandidate,
				Detail: map[string]any{
					"unit_id":      "u1",
					"standard":     arbiter.DefaultClaimStandard(),
					"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染"}},
				}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: assetID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: "sd-" + claimID}},
	); err != nil {
		t.Fatalf("铺声明 %s 失败: %v", claimID, err)
	}
}

// p473FalsifyRound 铺一轮反证(终态步骤 + 结构化结论)并置声明 testing。
// evidence/evidenceExchanges 为结论依据;unit=u9(≠发现者 u1,R12)。
func p473FalsifyRound(t *testing.T, store *rings.Store, task, claimID, stepID string,
	evidence string, evidenceExchanges []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind,
			Payload: rings.NodeAdded{NodeID: stepID, NodeType: rings.NodeStep, Status: "done",
				Detail: map[string]any{"kind": "falsify", "testclaim_id": claimID, "round": 1,
					"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染"}},
					"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
			Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u9",
			Payload: rings.TestClaimRecord{
				ClaimID: claimID, StepID: stepID, UnitID: "u9", Type: rings.TestClaimRecFalsify,
				Round: 1, ImpactSupported: true, ImpactLevel: "high",
				Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}},
				EvidenceHead: evidence, EvidenceExchanges: evidenceExchanges,
			}},
	); err != nil {
		t.Fatalf("铺反证轮 %s 失败: %v", stepID, err)
	}
}

// p473Mirror 铺一条镜留痕。
func p473Mirror(t *testing.T, store *rings.Store, task, stepID, exID, url string, status int, blobRef string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror",
		Payload: rings.MirrorRecord{
			ExchangeID: exID, Type: rings.MirrorRecHTTP, StepID: stepID, UnitID: "u9",
			Method: "GET", URL: url, Status: status, BlobRef: blobRef,
		},
	}); err != nil {
		t.Fatalf("铺镜留痕 %s 失败: %v", exID, err)
	}
}

// p473ExchangeBlob 写一条镜交换原文进仓库(响应体含 body)。
func p473ExchangeBlob(t *testing.T, blobs *swarm.BlobStore, exID, url, body string) string {
	t.Helper()
	doc, err := json.Marshal(mirror.Exchange{
		ID: exID, Type: rings.MirrorRecHTTP, TaskID: "p473", StepID: "s-f", UnitID: "u9",
		Request:  &mirror.Request{Method: "GET", URL: url, Proto: "HTTP/1.1", Host: "t.example:8080"},
		Response: &mirror.Response{Proto: "HTTP/1.1", Status: 200, StatusText: "OK", Body: []byte(body)},
	})
	if err != nil {
		t.Fatalf("序列化交换失败: %v", err)
	}
	ref, err := blobs.Write(doc)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	return ref
}

// p473LoopWithProvider 起 loop(scripted 模型:复现挑选会话按 reproJSON 答,
// 其余会话回坏文本走确定性回退)。
func p473LoopWithProvider(t *testing.T, task string, store *rings.Store, ex *swarm.Executor,
	reproJSON string) (*Loop, *gatedScriptedModel) {
	t.Helper()
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "复现步骤事实") {
			return scriptedReply{Text: reproJSON, Finish: "stop"}
		}
		return scriptedReply{Text: "不是 JSON", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起 scripted 模型失败: %v", err)
	}
	return &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}, model
}

// p473ConfirmedRepro 取声明最新定稿记录(无则 nil)。
func p473LatestRepro(t *testing.T, store *rings.Store, task, claimID string) *rings.TestClaimRecord {
	t.Helper()
	var out *rings.TestClaimRecord
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID || r.Type != rings.TestClaimRecRepro {
			continue
		}
		rr := r
		out = &rr
	}
	return out
}

// p473Base 铺公共底座:任务 + 镜内资产 + 声明 + 仓库;返回仓库句柄。
func p473Base(t *testing.T, task, claimID string) (*rings.Store, *swarm.Executor) {
	t.Helper()
	store := openStore(t)
	mkTask(t, store, task)
	p462SeedAsset(t, store, task, "a-t", "t.example:8080")
	p473SeedClaim(t, store, task, claimID, "a-t")
	ex := swarm.NewExecutor(store, nil, t.TempDir(), swarm.Budget{})
	return store, ex
}

// TestP473SingleStepSeededPackage ①单步型 + ②证明交换内容与确认证据一致:
// 引用命中的交换直进包(恰 1 步),响应体含所证数据(手机号);确认裁决
// 带 evidence_traceable 条件行(可追溯,种子数入账)。
func TestP473SingleStepSeededPackage(t *testing.T) {
	const task, claim = "p473-single", "tc-1"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	ref := p473ExchangeBlob(t, ex.Blobs, "ms-p1", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	p473FalsifyRound(t, store, task, claim, "s-f",
		"独立重放返回手机号 13800138000(交换引用在案)", []string{"ms-p1"})
	p473Mirror(t, store, task, "s-f", "ms-p1", "http://t.example:8080/api/user?phone=222", 200, ref)

	reproJSON := `{"steps":[{"seq":1,"short":"查手机号","desc":"向 t.example:8080 发 GET /api/user?phone=222,无需凭据;返回 200 且响应含 13800138000 即复现","purpose":"直接复现该问题"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("引用命中应确认: %s", node.Status)
	}
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil {
		t.Fatalf("确认应同批落定稿")
	}
	if len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ms-p1" {
		t.Fatalf("单步型应恰含证明交换: %+v", rec.Steps)
	}
	if rec.NoReproReason != "" || rec.StepsIncomplete != "" {
		t.Fatalf("正常形态不得带未生成标注: %+v", rec)
	}
	// ② 内容一致:定稿步骤的原始数据响应体含所证手机号。
	doc, err := ex.Blobs.Read(rec.Steps[0].BlobRef)
	if err != nil {
		t.Fatalf("读证明交换原文失败: %v", err)
	}
	var exDoc mirror.Exchange
	if json.Unmarshal(doc, &exDoc) != nil || !strings.Contains(string(exDoc.Response.Body), "13800138000") {
		t.Fatalf("证明交换响应体应含所证数据: %s", doc)
	}
	// 条件行:确认裁决带 evidence_traceable(可追溯,种子数入账)。
	var traceSeen bool
	for _, ev := range p473Events(t, store, task, rings.TestClaimRecVerdict, claim) {
		for _, c := range ev.Conditions {
			if c.Kind == arbiter.CondEvidenceTraceable {
				traceSeen = true
				if !c.Satisfied || c.Actual != 1 {
					t.Fatalf("evidence_traceable 应满足(种子 1): %+v", c)
				}
			}
		}
	}
	if !traceSeen {
		t.Fatalf("确认裁决应带 evidence_traceable 条件行")
	}
}

// TestP473ChainSeedModelAddsPrereq ①多步链型:种子=证明交换(最终接口),
// 模型在种子之上补前置步骤(登录取凭据)——两步按账本次序合并。
func TestP473ChainSeedModelAddsPrereq(t *testing.T) {
	const task, claim = "p473-chain", "tc-2"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	p473FalsifyRound(t, store, task, claim, "s-f",
		"链路重放成立(引用最终接口)", []string{"ms-api"})
	p473Mirror(t, store, task, "s-f", "ms-login", "http://t.example:8080/login", 200, "b-login")
	p473Mirror(t, store, task, "s-f", "ms-api", "http://t.example:8080/api/report", 200, "b-api")

	// 模型补前置(seq 1=login)并覆盖种子(seq 2=api)的三层说明。
	reproJSON := `{"steps":[{"seq":1,"short":"登录取凭据","desc":"向 t.example:8080 发 POST /login;返回 200 即取得凭据","purpose":"取得后续请求所需的会话凭据"},{"seq":2,"short":"带凭据拉报表","desc":"带凭据请求 t.example:8080 的 /api/report;返回 200 即复现","purpose":"直接复现该问题"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil || len(rec.Steps) != 2 {
		t.Fatalf("多步链型应含前置+证明交换两步: %+v", rec)
	}
	if rec.Steps[0].ExchangeID != "ms-login" || rec.Steps[1].ExchangeID != "ms-api" {
		t.Fatalf("两步应按账本次序(前置在前,证明交换在后): %+v", rec.Steps)
	}
	if rec.Steps[1].Short != "带凭据拉报表" || rec.Steps[0].Purpose == "" {
		t.Fatalf("补选与种子的三层说明应冻结: %+v", rec.Steps)
	}
	if rec.StepsIncomplete != "" {
		t.Fatalf("补全成功不得带未生成标注: %+v", rec)
	}
}

// TestP473ParamDiffSeedPicksDataExchange ①参数不同结果型(本案形态):
// 同型(method+路径+状态,查询串不敏感)两条 200 并存——data:null 在前、
// 含手机号在后;去同型旧缺陷是把含数据那条顶掉,种子机制反转:引用
// 命中的含数据交换直进包,池内 data:null 条目由模型决定(本例不补)。
func TestP473ParamDiffSeedPicksDataExchange(t *testing.T) {
	const task, claim = "p473-param", "tc-3"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	refNull := p473ExchangeBlob(t, ex.Blobs, "ms-null", "http://t.example:8080/api/user?phone=111", `{"data":null}`)
	refData := p473ExchangeBlob(t, ex.Blobs, "ms-data", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	p473FalsifyRound(t, store, task, claim, "s-f",
		"phone=222 返回手机号、phone=111 为空(引用含数据那条)", []string{"ms-data"})
	p473Mirror(t, store, task, "s-f", "ms-null", "http://t.example:8080/api/user?phone=111", 200, refNull)
	p473Mirror(t, store, task, "s-f", "ms-data", "http://t.example:8080/api/user?phone=222", 200, refData)

	// 模型只覆盖种子(合并后 seq 2=含数据条),不补 data:null 对比交换。
	reproJSON := `{"steps":[{"seq":2,"short":"查手机号","desc":"向 t.example:8080 发 GET /api/user?phone=222,无需凭据;返回 200 且响应含 13800138000 即复现","purpose":"直接复现该问题"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil {
		t.Fatalf("确认应落定稿")
	}
	if len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ms-data" {
		t.Fatalf("种子应为含数据那条(未被去同型顶掉): %+v", rec.Steps)
	}
	doc, _ := ex.Blobs.Read(rec.Steps[0].BlobRef)
	var paramEx mirror.Exchange
	if json.Unmarshal(doc, &paramEx) != nil || !strings.Contains(string(paramEx.Response.Body), "13800138000") {
		t.Fatalf("证明交换响应体应含手机号: %s", doc)
	}
}

// TestP473DirectToolRecordSeed ①直连工具记录型 + ④缺引用走 URL 回查:
// 无镜交换(直连旁路),证据文本中的 URL 命中可确定性组包的 bash curl
// 工具记录——种子走 P4-62 C(b) 通道组包,请求包 Burp 就绪。
func TestP473DirectToolRecordSeed(t *testing.T) {
	const task, claim = "p473-direct", "tc-4"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	p473FalsifyRound(t, store, task, claim, "s-f",
		"直连重放 http://t.example:8080/x 返回 200 含手机号(证据在命令输出)", nil)
	result := "HTTP/1.1 200 OK\r\nServer: nginx\r\nContent-Type: application/json\r\n\r\n" +
		`{"data":{"phone":"13800138000"}}`
	ref, err := ex.Blobs.Write([]byte(result))
	if err != nil {
		t.Fatalf("铺结果 blob 失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:u9",
		Payload: rings.SwarmRecord{StepID: "s-f", UnitID: "u9", Type: rings.SwarmRecToolCall,
			ToolName: "bash", ToolArgs: json.RawMessage(`{"command":"curl -sS -i -m 5 http://t.example:8080/x"}`),
			ResultRef: ref},
	}); err != nil {
		t.Fatalf("铺工具调用失败: %v", err)
	}

	reproJSON := `{"steps":[{"seq":1,"short":"直连验证","desc":"向 t.example:8080 发 GET /x,无需凭据;返回 200 且响应含 13800138000 即复现","purpose":"直接复现该问题"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("URL 回查命中工具记录应确认: %s", node.Status)
	}
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil || len(rec.Steps) != 1 || rec.Steps[0].Source != reproToolSource {
		t.Fatalf("直连形态种子应走工具记录组包: %+v", rec)
	}
	doc, err := ex.Blobs.Read(rec.Steps[0].BlobRef)
	if err != nil {
		t.Fatalf("读组包文档失败: %v", err)
	}
	var exDoc mirror.Exchange
	if json.Unmarshal(doc, &exDoc) != nil {
		t.Fatalf("组包文档应与镜留痕同构: %s", doc)
	}
	if exDoc.Request.Method != "GET" || !strings.Contains(string(exDoc.Response.Body), "13800138000") {
		t.Fatalf("组包应含所证数据: %s", doc)
	}
}

// TestP473MirrorURLFallback ④镜形态回查:缺引用时证据文本 URL 精确命中
// (同路径不同查询串的两条里命中含数据那条——精确匹配优先于查询串
// 不敏感的兜底层);同一 URL 多次重放只锚最近一条(活体实锤修正:证据
// 反复引用同一 URL 不灌多种子)。
func TestP473MirrorURLFallback(t *testing.T) {
	const task, claim = "p473-urlfb", "tc-5"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	refNull := p473ExchangeBlob(t, ex.Blobs, "ms-null", "http://t.example:8080/api/user?phone=111", `{"data":null}`)
	refData := p473ExchangeBlob(t, ex.Blobs, "ms-data", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	refData2 := p473ExchangeBlob(t, ex.Blobs, "ms-data2", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	p473FalsifyRound(t, store, task, claim, "s-f",
		"独立重放 http://t.example:8080/api/user?phone=222 返回手机号 13800138000,再重放 http://t.example:8080/api/user?phone=222 复核一致", nil)
	p473Mirror(t, store, task, "s-f", "ms-null", "http://t.example:8080/api/user?phone=111", 200, refNull)
	p473Mirror(t, store, task, "s-f", "ms-data", "http://t.example:8080/api/user?phone=222", 200, refData)
	p473Mirror(t, store, task, "s-f", "ms-data2", "http://t.example:8080/api/user?phone=222", 200, refData2)

	seeds, err := ResolveEvidenceSeeds(ctx, store, ex.Blobs, task, claim)
	if err != nil {
		t.Fatalf("种子解析失败: %v", err)
	}
	if len(seeds) != 1 || seeds[0].Mirror == nil || seeds[0].Mirror.ExchangeID != "ms-data2" ||
		seeds[0].Via != seedViaURL {
		t.Fatalf("URL 回查应只锚精确命中的最近一条(末次验证): %+v", seeds)
	}
}

// TestP473UntraceableNotConfirmed ③证据不可追溯不确认:引用指向不存在的
// 交换、证据文本无 URL——确认改判待确认,理由逐字"证据不足,理由:
// 证据不可追溯",不落定稿;条件行 evidence_traceable 不满足。
func TestP473UntraceableNotConfirmed(t *testing.T) {
	const task, claim = "p473-untrace", "tc-6"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	p473FalsifyRound(t, store, task, claim, "s-f",
		"独立重放确认成立(既无引用也无地址)", []string{"ms-ghost"})

	reproJSON := `{"steps":[{"seq":1,"short":"x","desc":"y","purpose":"z"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimPending {
		t.Fatalf("不可追溯不得确认: %s", node.Status)
	}
	digests, _ := store.ClaimDigests(ctx, task)
	if got := digests[claim].PendingReason; got != arbiter.UntraceablePendingReason {
		t.Fatalf("搁置理由须逐字'证据不足,理由:证据不可追溯': %q", got)
	}
	if rec := p473LatestRepro(t, store, task, claim); rec != nil {
		t.Fatalf("不确认不得落定稿: %+v", rec)
	}
	var trace *rings.ArbiterConditionResult
	for _, ev := range p473Events(t, store, task, rings.TestClaimRecVerdict, claim) {
		if ev.Decision != rings.VerdictPending || !strings.Contains(ev.PendingReason, "证据不可追溯") {
			t.Fatalf("裁决应改判待确认且理由入账: %+v", ev)
		}
		for i := range ev.Conditions {
			if ev.Conditions[i].Kind == arbiter.CondEvidenceTraceable {
				trace = &ev.Conditions[i]
			}
		}
	}
	if trace == nil || trace.Satisfied || trace.Actual != 0 {
		t.Fatalf("evidence_traceable 应不满足: %+v", trace)
	}
}

// TestP473ReopenReseeds ①重开再确认:新战役反证的新交换为种子,旧战役
// 交换不再入选(历史 repro 保留,R1)。
func TestP473ReopenReseeds(t *testing.T) {
	const task, claim = "p473-reopen", "tc-7"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	ref1 := p473ExchangeBlob(t, ex.Blobs, "ms-r1", "http://t.example:8080/api/user?phone=111", `{"data":null}`)
	p473FalsifyRound(t, store, task, claim, "s-f1", "首轮成立(引用 ms-r1)", []string{"ms-r1"})
	p473Mirror(t, store, task, "s-f1", "ms-r1", "http://t.example:8080/api/user?phone=111", 200, ref1)

	reproJSON := `{"steps":[{"seq":1,"short":"查手机号","desc":"向 t.example:8080 发 GET /api/user?phone=222;返回 200 且响应含 13800138000 即复现","purpose":"直接复现该问题"}]}`
	l, _ := p473LoopWithProvider(t, task, store, ex, reproJSON)
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("首轮裁决失败: %v", err)
	}
	if rec := p473LatestRepro(t, store, task, claim); rec == nil || len(rec.Steps) != 1 ||
		rec.Steps[0].ExchangeID != "ms-r1" {
		t.Fatalf("首轮定稿应含 ms-r1: %+v", rec)
	}

	// 重开 → 新战役(round 2)引用新交换 ms-r2。
	if err := ReopenClaim(ctx, store, task, claim, "manual", "复核证据", "creator"); err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	var newStep string
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id == claim && s.ID != "s-f1" {
			newStep = s.ID
		}
	}
	if newStep == "" {
		t.Fatalf("重开应派新反证步骤")
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "test",
			Payload: rings.NodeStateChanged{NodeID: newStep, Status: "done"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u9",
			Payload: rings.TestClaimRecord{
				ClaimID: claim, StepID: newStep, UnitID: "u9", Type: rings.TestClaimRecFalsify,
				Round: 2, ImpactSupported: true, ImpactLevel: "high",
				Alternatives:      []rings.AltResult{{ID: "alt1", Refuted: true}},
				EvidenceHead:      "重开复核成立(引用 ms-r2)",
				EvidenceExchanges: []string{"ms-r2"},
			}},
	); err != nil {
		t.Fatalf("铺重开轮失败: %v", err)
	}
	ref2 := p473ExchangeBlob(t, ex.Blobs, "ms-r2", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	p473Mirror(t, store, task, newStep, "ms-r2", "http://t.example:8080/api/user?phone=222", 200, ref2)

	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatalf("重开轮裁决失败: %v", err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("重开再确认应成立: %s", node.Status)
	}
	// 新机制:最新定稿种子=新战役交换;旧战役交换不再入选;历史 repro 在案。
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil || len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ms-r2" {
		t.Fatalf("重开定稿应只含新战役证明交换: %+v", rec)
	}
	n := 0
	for _, ev := range p473Events(t, store, task, rings.TestClaimRecRepro, claim) {
		_ = ev
		n++
	}
	if n != 2 {
		t.Fatalf("两代定稿都应在案(R1): %d", n)
	}
}

// TestP473PickFailureKeepsSeeds ①构造挑选失败(打桩模型失败):种子包
// 仍在(证明交换+确定性兜底描述)+如实标注"流程步骤未生成（挑选失败）";
// 重试恰一次;模型不可用零外呼同款。
func TestP473PickFailureKeepsSeeds(t *testing.T) {
	const task, claim = "p473-pickfail", "tc-8"
	store, ex := p473Base(t, task, claim)
	ctx := context.Background()
	ref := p473ExchangeBlob(t, ex.Blobs, "ms-p8", "http://t.example:8080/api/user?phone=222",
		`{"data":{"phone":"13800138000"}}`)
	p473FalsifyRound(t, store, task, claim, "s-f", "成立(引用在案)", []string{"ms-p8"})
	p473Mirror(t, store, task, "s-f", "ms-p8", "http://t.example:8080/api/user?phone=222", 200, ref)

	t.Run("打桩模型失败仍出种子包", func(t *testing.T) {
		l, model := p473LoopWithProvider(t, task, store, ex, "不是 JSON")
		if _, err := l.judgeClaims(ctx); err != nil {
			t.Fatalf("裁决失败: %v", err)
		}
		rec := p473LatestRepro(t, store, task, claim)
		if rec == nil || len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ms-p8" {
			t.Fatalf("挑选失败应仍出种子包: %+v", rec)
		}
		if rec.StepsIncomplete != reproFlowIncomplete {
			t.Fatalf("应如实标注流程步骤未生成: %q", rec.StepsIncomplete)
		}
		if rec.NoReproReason != "" {
			t.Fatalf("有包不得带无复现包原因: %q", rec.NoReproReason)
		}
		if rec.Steps[0].Desc == "" || rec.Steps[0].Short == "" {
			t.Fatalf("种子应保留确定性兜底描述: %+v", rec.Steps[0])
		}
		picks := 0
		for _, rr := range model.requests() {
			if strings.Contains(rr.LastUser, "复现步骤事实") {
				picks++
			}
		}
		if picks != 2 {
			t.Fatalf("挑选失败应重试恰一次(共 2 次挑选请求): %d", picks)
		}
	})

	t.Run("模型不可用零外呼同款", func(t *testing.T) {
		const claim2 = "tc-8b"
		p473SeedClaim(t, store, task, claim2, "a-t")
		p473FalsifyRound(t, store, task, claim2, "s-f2", "成立(引用在案)", []string{"ms-p8b"})
		p473Mirror(t, store, task, "s-f2", "ms-p8b", "http://t.example:8080/api/user?phone=333", 200, "b-8b")
		l := &Loop{TaskID: task, Store: store, Swarm: ex}
		if _, err := l.judgeClaims(ctx); err != nil {
			t.Fatalf("裁决失败: %v", err)
		}
		rec := p473LatestRepro(t, store, task, claim2)
		if rec == nil || len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ms-p8b" ||
			rec.StepsIncomplete != reproFlowIncomplete {
			t.Fatalf("模型不可用应零外呼出种子包+标注: %+v", rec)
		}
	})
}

// TestP473SeedBypassesPoolFilters ②种子不经池过滤:error 交换与锚定外
// 交换被单位引用时照样直进(error 过滤/host 锚定不适用于种子——证明物
// 直进,池入口两道过滤只作用于候选池)。
func TestP473SeedBypassesPoolFilters(t *testing.T) {
	const task, claim = "p473-bypass", "tc-9"
	store, _ := p473Base(t, task, claim)
	ctx := context.Background()
	// 锚定外的任务内资产(池 B② 会拦,种子不受限)。
	p462SeedAsset(t, store, task, "a-other", "other.example:9000")
	p473FalsifyRound(t, store, task, claim, "s-f",
		"成立(引用在案)", []string{"ms-err", "ms-off"})
	// error 交换(镜记录带错误)与锚定外成功交换各一条。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror",
			Payload: rings.MirrorRecord{
				ExchangeID: "ms-err", Type: rings.MirrorRecHTTP, StepID: "s-f", UnitID: "u9",
				Method: "GET", URL: "http://t.example:8080/x", BlobRef: "b-err",
				Error: "转发失败: connection refused"}},
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror",
			Payload: rings.MirrorRecord{
				ExchangeID: "ms-off", Type: rings.MirrorRecHTTP, StepID: "s-f", UnitID: "u9",
				Method: "GET", URL: "http://other.example:9000/y", Status: 200, BlobRef: "b-off"}},
	); err != nil {
		t.Fatalf("铺镜留痕失败: %v", err)
	}
	seeds, err := ResolveEvidenceSeeds(ctx, store, nil, task, claim)
	if err != nil {
		t.Fatalf("种子解析失败: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("被引用的 error 与锚定外交换都应是种子(证明物直进): %+v", seeds)
	}
	for _, sd := range seeds {
		if sd.Via != seedViaReference {
			t.Fatalf("命中通道应为引用: %+v", sd)
		}
	}
	// 池口径回归:同流量无引用时只进过程清单(诊断计数在案)。
	const claim2 = "tc-9b"
	p462SeedClaim(t, store, task, claim2, "a-t")
	p473FalsifyRound(t, store, task, claim2, "s-f2", "独立重放确认成立(无引用无地址)", nil)
	seeds2, err := ResolveEvidenceSeeds(ctx, store, nil, task, claim2)
	if err != nil || len(seeds2) != 0 {
		t.Fatalf("无引用无 URL 不构成种子: %+v err=%v", seeds2, err)
	}
}

// TestP473ReferenceDedupSameURL 引用命中同址去重(活体实锤修正):同一
// URL 的多次重放被逐条引用时按地址归并保最近(同一证明不重复进包);
// 不同地址的引用各自保留(多步链各环节证明不受影响)。
func TestP473ReferenceDedupSameURL(t *testing.T) {
	const task, claim = "p473-refdedup", "tc-10"
	store, _ := p473Base(t, task, claim)
	ctx := context.Background()
	p473FalsifyRound(t, store, task, claim, "s-f",
		"重放成立(引用在案)", []string{"ms-a1", "ms-a2", "ms-b"})
	p473Mirror(t, store, task, "s-f", "ms-a1", "http://t.example:8080/api/user?phone=222", 200, "b-a1")
	p473Mirror(t, store, task, "s-f", "ms-b", "http://t.example:8080/login", 200, "b-b")
	p473Mirror(t, store, task, "s-f", "ms-a2", "http://t.example:8080/api/user?phone=222", 200, "b-a2")

	seeds, err := ResolveEvidenceSeeds(ctx, store, nil, task, claim)
	if err != nil {
		t.Fatalf("种子解析失败: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("同址重复引用应归并为 2 条种子(同址最近 1 条+异址 1 条): %+v", seeds)
	}
	if seeds[0].Mirror.ExchangeID != "ms-a2" || seeds[1].Mirror.ExchangeID != "ms-b" {
		t.Fatalf("同址应保最近(ms-a2),异址照收(ms-b): %+v", seeds)
	}
	for _, sd := range seeds {
		if sd.Via != seedViaReference {
			t.Fatalf("命中通道应为引用: %+v", sd)
		}
	}
}

// TestP473StepsIncompleteLedgerRule ⑥账本物理校验:流程步骤未生成标注只
// 伴随存在的包(有步骤时合法、零步骤时拒收)。
func TestP473StepsIncompleteLedgerRule(t *testing.T) {
	store := openStore(t)
	const task = "p473-ledger"
	mkTask(t, store, task)
	ctx := context.Background()
	good := rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro,
		StepsIncomplete: reproFlowIncomplete,
		Steps:           []rings.ReproStep{{Seq: 1, Method: "GET", URL: "http://t.example:8080/", Status: 200}}}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: good,
	}); err != nil {
		t.Fatalf("有步骤的标注应被接受: %v", err)
	}
	bad := rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro,
		StepsIncomplete: reproFlowIncomplete}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: bad,
	}); err == nil {
		t.Fatal("零步骤的未生成标注应被账本拒绝(那是无复现包语义,走 NoReproReason)")
	}
}

// p473Events 取声明名下指定类型的记录。
func p473Events(t *testing.T, store *rings.Store, task, typ, claimID string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.ClaimID == claimID && r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

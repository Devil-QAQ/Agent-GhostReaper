package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

func TestContentFollowupPreludeUsesStoredDisplay(t *testing.T) {
	srv, h, store := newP414Server(t)
	task := seedP414Claim(t, store, h)
	ctx := t.Context()
	claim, _ := store.Node(ctx, task, "tc-f")
	raw := srv.claimFollowupPrelude(ctx, task, claim)
	if !strings.Contains(raw, claim.Title) {
		t.Fatal("旧数据须保留原文回退")
	}
	statement := strings.Repeat("备份文件公开暴露后台凭据。", 30) + "未验证其他后台功能。"
	source, err := store.ClaimTextSourceRev(ctx, task, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, store, task, rings.Event{Kind: rings.EventClaimText, Actor: "overmind", Payload: rings.ClaimTextPolish{
		ClaimID: claim.ID, SourceRev: source, Title: "公开备份泄露凭据", Statement: statement, Impact: "可访问后台，未验证数据操作。",
	}})
	prelude := srv.claimFollowupPrelude(ctx, task, claim)
	for _, want := range []string{"【成果】tc-f:公开备份泄露凭据", statement, "影响面:可访问后台，未验证数据操作。", "来龙去脉(定稿叙述)", "判断当前状态:confirmed"} {
		if !strings.Contains(prelude, want) {
			t.Fatalf("追问未读取同源内容: %s", want)
		}
	}
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm", Payload: rings.NodeAdded{NodeID: "f-followup", NodeType: rings.NodeFact, Title: "旧事实长文", Status: "confirmed"}},
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{FactID: "f-followup", Title: "后台拒绝未登录访问", Statement: "未登录时无法访问后台。"}},
	)
	for _, id := range []string{claim.ID, "f-followup"} {
		post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/"+id+"/messages", `{"text":"说明这条结果"}`, http.StatusOK, nil)
	}
	var view struct {
		Followups []shepherdFollowupRow `json:"followups"`
	}
	get(t, h, "/api/tasks/"+task+"/shepherd", http.StatusOK, &view)
	rows := map[string]shepherdFollowupRow{}
	for _, row := range view.Followups {
		rows[row.ClaimID] = row
	}
	if rows[claim.ID].Kind != "claim" || rows[claim.ID].Statement != "公开备份泄露凭据" || rows["f-followup"].Kind != "fact" || rows["f-followup"].Statement != "后台拒绝未登录访问" {
		t.Fatalf("追问列表丢失名称或条目类型: %+v", rows)
	}
	_, store2, task2, _ := p464Setup(t)
	srv.Store = store2
	fact, _ := store2.Node(ctx, task2, "f-cred")
	appendEvents(t, store2, task2, rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{
		FactID: fact.ID, Title: "备份含后台凭据", Statement: statement,
	}})
	prelude = srv.factFollowupPrelude(ctx, task2, fact)
	if !strings.Contains(prelude, "【事实】f-cred:备份含后台凭据") || !strings.Contains(prelude, statement) || !strings.Contains(prelude, "本轮追问锚定以上这一条事实") {
		t.Fatal("事实追问缺显示稿或条目边界")
	}
	current, _ := store2.Node(ctx, task2, fact.ID)
	if !reflect.DeepEqual(fact, current) {
		t.Fatal("追问读取不能改写事实")
	}
}

func TestContentIntentDisplayUsesLedgerText(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)
	ctx := context.Background()
	original, err := store.Node(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventStepTitle, Actor: "overmind", Payload: rings.StepTitlePolish{StepID: "s1", Title: "检查备份下载入口"}},
		rings.Event{Kind: rings.EventStepTitle, Actor: "overmind", Payload: rings.StepTitlePolish{StepID: "s1", Title: "检查备份文件访问限制"}},
		rings.Event{Kind: rings.EventStepTitle, Actor: "overmind", Payload: rings.StepTitlePolish{StepID: "s2", Title: "验证后台账号是否可登录"}},
	)
	before, _ := store.CurrentRevision(ctx, task)
	var rows []intentRow
	get(t, h, "/api/tasks/"+task+"/intents", http.StatusOK, &rows)
	got := map[string]intentRow{}
	for _, r := range rows {
		got[r.ID] = r
	}
	if got["s1"].Title != "检查备份文件访问限制" || got["s2"].Title != "验证后台账号是否可登录" {
		t.Fatalf("列表未使用最新显示稿: %+v", got)
	}
	if got["s3"].Title != "旁路探测" || got["s4"].Title != "无关意图" {
		t.Fatal("无显示稿原文回退被改变")
	}
	after, _ := store.CurrentRevision(ctx, task)
	node, _ := store.Node(ctx, task, "s1")
	if before != after || !reflect.DeepEqual(original, node) {
		t.Fatal("读取显示稿改写了执行原文或新增账本事件")
	}
	if len(got["s1"].Outcomes) != 1 || got["s1"].Outcomes[0].ID != "f-1" {
		t.Fatal("显示修复改变事实产出归属")
	}
}

func TestContentFactSummarySelectionStable(t *testing.T) {
	fact := func(id, text string) rings.FactStatement { return rings.FactStatement{FactID: id, Statement: text} }
	cases := []struct {
		name    string
		digests []rings.ClaimDigest
		want    map[string]string
	}{
		{"摘要版本优先于裁决版本", []rings.ClaimDigest{
			{ClaimID: "a", SummaryRev: 10, VerdictRev: 100, Facts: []rings.FactStatement{fact("f", "旧说明"), fact("only-old", "保留独有事实")}},
			{ClaimID: "b", SummaryRev: 20, VerdictRev: 2, Facts: []rings.FactStatement{fact("f", "新说明")}},
		}, map[string]string{"f": "新说明", "only-old": "保留独有事实"}},
		{"新稿空字段不吞掉有内容的来源", []rings.ClaimDigest{
			{ClaimID: "a", SummaryRev: 10, Facts: []rings.FactStatement{fact("f", "已有说明")}},
			{ClaimID: "b", SummaryRev: 20, Facts: []rings.FactStatement{fact("f", "")}},
		}, map[string]string{"f": "已有说明"}},
		{"旧稿缺版本稳定回退", []rings.ClaimDigest{
			{ClaimID: "b", Facts: []rings.FactStatement{fact("f", "旧稿乙")}},
			{ClaimID: "a", Facts: []rings.FactStatement{fact("f", "旧稿甲")}},
		}, map[string]string{"f": "旧稿乙"}},
		{"有版本稿优先于无版本稿", []rings.ClaimDigest{
			{ClaimID: "z", Facts: []rings.FactStatement{fact("f", "无版本")}},
			{ClaimID: "a", SummaryRev: 1, Facts: []rings.FactStatement{fact("f", "有版本")}},
		}, map[string]string{"f": "有版本"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for round := 0; round < 40; round++ {
				m := map[string]rings.ClaimDigest{}
				for offset := range tc.digests {
					d := tc.digests[(offset+round)%len(tc.digests)]
					m[d.ClaimID] = d
				}
				if got := polishedFactMapOf(m); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("不稳定或取错说明: %+v want %+v", got, tc.want)
				}
			}
		})
	}
}

func TestContentSummaryRevisionFromExistingProjection(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)
	ctx := context.Background()
	revs, err := store.Append(ctx, task, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Statement: "已确认的描述", Facts: []rings.FactStatement{{FactID: "f-1", Statement: "事实说明"}}}})
	if err != nil {
		t.Fatal(err)
	}
	digests, err := store.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if digests["tc-1"].SummaryRev != revs[0] {
		t.Fatalf("摘要自身的已有版本未下传: %+v", digests["tc-1"])
	}
	// 改变裁决元数据，不能把摘要的新旧改成裁决事件的时间。
	appendEvents(t, store, task, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecVerdict, Conditions: []rings.ArbiterConditionResult{{Kind: "evidence_traceable", Satisfied: true, Actual: 1, Required: 1}}, Satisfied: true, Decision: rings.VerdictConfirm, PendingReason: "补充元数据"}})
	digests, err = store.ClaimDigests(ctx, task)
	if err != nil || digests["tc-1"].SummaryRev != revs[0] {
		t.Fatal("裁决事件覆盖摘要版本")
	}
}

func TestContentFactNameAndExplanationStaySeparate(t *testing.T) {
	h, store, task, _ := p464Setup(t)
	ctx := context.Background()
	raw, err := store.Node(ctx, task, "f-cred")
	if err != nil {
		t.Fatal(err)
	}
	explanation := strings.Repeat("说明保留完整的观察和必要条件。", 25) + "仅适用于已验证的接口。"
	appendEvents(t, store, task, rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{FactID: "f-cred", Title: "备份文件带有后台口令", Statement: explanation}})
	var body struct {
		Facts []factRow `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)
	found := false
	for _, f := range body.Facts {
		if f.ID == "f-cred" {
			found = true
			if f.Title != "备份文件带有后台口令" || f.Statement != explanation || !reflect.DeepEqual(f.ResultTypes, []rings.FactResultType{rings.FactResultCredential}) || len(f.Assets) != 1 {
				t.Fatalf("名称、完整说明、分类或资产混用: %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("缺事实")
	}
	var detail struct {
		Title     string `json:"title"`
		Name      string `json:"polished_title"`
		Statement string `json:"polished_statement"`
	}
	get(t, h, "/api/tasks/"+task+"/nodes/f-cred", http.StatusOK, &detail)
	if detail.Title != raw.Title || detail.Name != "备份文件带有后台口令" || detail.Statement != explanation {
		t.Fatal("详情丢失原始陈述或截断完整显示稿")
	}
	current, _ := store.Node(ctx, task, "f-cred")
	if !reflect.DeepEqual(raw, current) {
		t.Fatal("显示稿改写机器事实")
	}
}

func TestContentLivePublishesCompletedDisplayText(t *testing.T) {
	for _, kind := range []rings.EventKind{rings.EventStepTitle, rings.EventFactText, rings.EventOvermindPolish, rings.EventClaimText, rings.EventClaimRetextNote, rings.EventNodeAmended} {
		t.Run(string(kind), func(t *testing.T) {
			srv, ts, _ := newLiveTestServer(t)
			createLiveTask(t, srv, "copy-live")
			commitEvents(t, srv, "copy-live",
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s", NodeType: rings.NodeStep, Title: "原始意图"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Title: "原始事实", Status: "confirmed"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "c", NodeType: rings.NodeTestClaim, Title: "原始漏洞", Status: "confirmed"}},
			)
			c := dialWS(t, ts.URL)
			defer c.close()
			c.expectMsg(2*time.Second, map[string]any{"t": "ready"})
			var payload any
			want := map[string]bool{}
			switch kind {
			case rings.EventStepTitle:
				payload = rings.StepTitlePolish{StepID: "s", Title: "显示名称"}
				want["tasks"], want["pview:copy-live"] = true, true
			case rings.EventFactText:
				payload = rings.FactTextPolish{FactID: "f", Title: "显示名称", Statement: "完整说明"}
				want["tasks"], want["claims:copy-live"], want["pview:copy-live"] = true, true, true
			case rings.EventOvermindPolish:
				payload = rings.OvermindPolish{RecRev: 1, Note: "当前规划说明"}
				want["tasks"] = true
			case rings.EventNodeAmended:
				payload = rings.NodeAmended{NodeID: "f", Detail: map[string]any{"result_types": []string{"plain"}}, Note: "核对为普通观察"}
				want["tasks"], want["claims:copy-live"] = true, true
			case rings.EventClaimText:
				source, err := srv.Store.ClaimTextSourceRev(context.Background(), "copy-live", "c")
				if err != nil {
					t.Fatal(err)
				}
				payload = rings.ClaimTextPolish{ClaimID: "c", SourceRev: source, Title: "显示名称", Statement: "完整说明"}
				want["tasks"], want["claims:copy-live"], want["pview:copy-live"] = true, true, true
			case rings.EventClaimRetextNote:
				payload = rings.RetestNotePolish{ClaimID: "c", Round: 1, Note: "复测说明"}
				want["claims:copy-live"] = true
			}
			commitEvents(t, srv, "copy-live", rings.Event{Kind: kind, Actor: "overmind", Payload: payload})
			for len(want) > 0 {
				c.drainUntil(2*time.Second, func(m map[string]any) bool {
					k, _ := m["k"].(string)
					if want[k] {
						delete(want, k)
						return true
					}
					return false
				})
			}
		})
	}
}

func TestContentFactResultsNeverGuessFromWording(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: "negative", NodeType: rings.NodeFact, Title: "/admin/返回404，未取得Cookie或权限", Status: "confirmed", Detail: map[string]any{"result_types": []string{"plain"}, "evidence": "HTTP404"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: "mixed", NodeType: rings.NodeFact, Title: "已取得口令和数据", Status: "confirmed", Detail: map[string]any{"result_types": []string{"data", "credential"}, "evidence": "已保存的真实内容"}}},
	)
	var out struct {
		Facts []factRow `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &out)
	got := map[string]factRow{}
	for _, f := range out.Facts {
		got[f.ID] = f
	}
	if !reflect.DeepEqual(got["negative"].ResultTypes, []rings.FactResultType{rings.FactResultObservation}) {
		t.Fatal("负向观察误标为战果")
	}
	if !reflect.DeepEqual(got["mixed"].ResultTypes, []rings.FactResultType{rings.FactResultCredential, rings.FactResultData}) {
		t.Fatal("多类成果未保留")
	}
	if !reflect.DeepEqual(got["f-1"].ResultTypes, []rings.FactResultType{rings.FactResultUnclassified}) {
		t.Fatal("无类型旧记录被猜测分类")
	}
	appendEvents(t, store, task, rings.Event{Kind: rings.EventNodeAmended, Actor: "creator", Payload: rings.NodeAmended{NodeID: "negative", Detail: map[string]any{"result_types": []string{"unclassified"}}, Note: "暂时撤回分类，等待重新核对"}})
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &out)
	for _, f := range out.Facts {
		if f.ID == "negative" && !reflect.DeepEqual(f.ResultTypes, []rings.FactResultType{rings.FactResultUnclassified}) {
			t.Fatal("分类更正未生效")
		}
	}
}

func TestContentIntentDetailsExposeFullResults(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)
	ctx := context.Background()
	original, _ := store.Node(ctx, task, "s1")
	explanation := "检查备份入口是否限制访问。\n\n这是一项检查计划，结果另行列出。"
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventStepTitle, Actor: "overmind", Payload: rings.StepTitlePolish{StepID: "s1", Title: "备份访问检查", Statement: explanation}},
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{FactID: "f-1", Title: "备份可读取", Statement: "当前入口允许读取备份。"}},
	)
	var body struct {
		Intent struct {
			Title     string `json:"title"`
			Name      string `json:"polished_title"`
			Statement string `json:"polished_statement"`
		} `json:"intent"`
		Results []struct {
			ID        string         `json:"id"`
			Name      string         `json:"polished_title"`
			Statement string         `json:"polished_statement"`
			Detail    map[string]any `json:"detail"`
		} `json:"results"`
	}
	get(t, h, "/api/tasks/"+task+"/pview/details/s1", http.StatusOK, &body)
	if body.Intent.Title != original.Title || body.Intent.Name != "备份访问检查" || body.Intent.Statement != explanation {
		t.Fatalf("意图完整说明或原文缺失: %+v", body.Intent)
	}
	if len(body.Results) != 2 || body.Results[0].ID != "f-1" || body.Results[0].Name != "备份可读取" || body.Results[0].Statement != "当前入口允许读取备份。" {
		t.Fatalf("实际结果显示稿缺失: %+v", body.Results)
	}
	get(t, h, "/api/tasks/"+task+"/pview/details/s4", http.StatusOK, &body)
	if len(body.Results) != 0 {
		t.Fatal("无结果意图编造了事实")
	}
}

func TestContentEvidenceKeepsOriginalAvailable(t *testing.T) {
	srv, h, _ := newShepherdTestServer(t)
	const task = "content-evidence"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "证据展示", rings.RoeUpdated{}, nil); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("HTTP 原始记录，不经模型改写。\n", 1000)
	ref, err := srv.blobStore().Write([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		detail map[string]any
		code   int
		want   string
	}{
		{"f-inline", map[string]any{"evidence": full}, 200, full},
		{"f-file", map[string]any{"evidence_ref": ref, "evidence_head": "只显示摘要"}, 200, full},
		{"f-empty", map[string]any{}, 404, ""},
		{"f-missing", map[string]any{"evidence_ref": "sha256:" + strings.Repeat("0", 64), "evidence_head": "摘要不能冒充完整原文"}, 404, ""},
		{"f-invalid", map[string]any{"evidence_ref": "../../outside"}, 404, ""},
	} {
		appendEvents(t, srv.Store, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: tc.id, NodeType: rings.NodeFact, Title: "观察", Detail: tc.detail}})
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task+"/nodes/"+tc.id+"/evidence", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code || tc.code == 200 && w.Body.String() != tc.want {
			t.Fatalf("证据读取错误 %s %d", tc.id, w.Code)
		}
		if tc.code == 200 && (!strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Content-Type-Options") != "nosniff") {
			t.Fatal("证据须安全作为原文下载")
		}
		if tc.code != 200 && w.Header().Get("Content-Disposition") != "" {
			t.Fatal("读取失败不应下载错误文件")
		}
	}
	for _, url := range []string{"/api/tasks/missing/nodes/f-file/evidence", "/api/tasks/" + task + "/nodes/missing/evidence"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != 404 {
			t.Fatal("跨任务或无节点不能读取证据")
		}
	}
}

func TestContentClaimNameAcrossReaders(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)
	source, err := store.ClaimTextSourceRev(t.Context(), task, "tc-1")
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, store, task, rings.Event{Kind: rings.EventClaimText, Actor: "overmind", Payload: rings.ClaimTextPolish{ClaimID: "tc-1", SourceRev: source, Title: "备份可匿名读取", Statement: "未登录也能读取这份备份。", Impact: "备份中的配置可被读取。"}})
	var rows []claimView
	get(t, h, "/api/tasks/"+task+"/claims", 200, &rows)
	found := false
	for _, row := range rows {
		if row.ID == "tc-1" {
			found = true
			if row.Title != "备份可匿名读取" || row.Statement != "未登录也能读取这份备份。" {
				t.Fatal("列表名称与说明混用")
			}
		}
	}
	if !found {
		t.Fatal("缺漏洞")
	}
	var detail claimDetailView
	get(t, h, "/api/tasks/"+task+"/claims/tc-1", 200, &detail)
	if detail.Claim.Title != "备份可匿名读取" {
		t.Fatal("详情未读取同一显示稿")
	}
	titles := (&Server{Store: store}).polishedNodeTitleOf(t.Context(), task)
	if titles["tc-1"] != "备份可匿名读取" {
		t.Fatal("图读侧名称未统一")
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
)

// ---- P4-14 成果追问会话面板 API(问题清单 #24) ----

// newP414Server 测试后端:挂牧者服务(零 Provider——追问入账后异步落
// 失败态,不影响读侧断言),与生产装配同形。
func newP414Server(t *testing.T) (*Server, *http.ServeMux, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	srv, h := newClaimTestServer(t, store, swarm.NewLedgerGate(store), dir)
	srv.Shepherd = &shepherd.Service{Store: store, RootDir: dir}
	return srv, h, store
}

// seedP414Claim 铺一条确认定稿(含 P4-13 叙述)的声明,返回任务 ID。
func seedP414Claim(t *testing.T, store *rings.Store, h *http.ServeMux) string {
	t.Helper()
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"title":"P4-14 追问验证"}`, http.StatusOK, &created)
	task := created["id"]
	seedFullClaim(t, store, task)
	// 确认定稿(叙述冻结)——预载包与预载摘要卡的数据源。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", Type: rings.TestClaimRecRepro, Severity: "high",
			Narrative: "单位 u1 执行「审计主站」时发现备份列举;u3 独立反证 2 轮排除竞争解释,仲裁者判定成立。"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimConfirmed}},
	)
	return task
}

// TestClaimFollowupAPI 追问端点主径:POST 入队开第 1 轮(预载随首问
// 落账且含定稿叙述/发现单位/确认时刻)→ GET 会话视图(轮次/问数/
// 只读判定)→ 发现详情 Followup 摘要与牧者组 followups 列表 → 新开
// 一轮 → 旧轮回看;未知成果与空问如实拒绝;导出零影响(问答不进报告)。
func TestClaimFollowupAPI(t *testing.T) {
	_, h, store := newP414Server(t)
	task := seedP414Claim(t, store, h)

	// 未知成果 404;空问 400。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-none/messages",
		`{"text":"问"}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-f/messages",
		`{"text":"  "}`, http.StatusBadRequest, nil)

	// 首问:开第 1 轮。
	var posted map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-f/messages",
		`{"text":"这个发现的证据在哪一步?"}`, http.StatusOK, &posted)
	if posted["round"] != float64(1) {
		t.Fatalf("首问应开第 1 轮: %v", posted)
	}
	// 续问:同轮。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-f/messages",
		`{"text":"竞争解释是什么?"}`, http.StatusOK, &posted)
	if posted["round"] != float64(1) {
		t.Fatalf("续问应留在第 1 轮: %v", posted)
	}
	// 新开一轮:第 2 轮。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-f/messages",
		`{"text":"换个角度再问一遍","new_round":true}`, http.StatusOK, &posted)
	if posted["round"] != float64(2) {
		t.Fatalf("新开一轮应到第 2 轮: %v", posted)
	}

	// 预载只随每轮首问落账,内容为确定性成果链路包(定稿叙述在位)。
	evs, _ := store.Events(t.Context(), task, 0, 0)
	firstPrelude := ""
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && rec.Prelude != "" {
			firstPrelude = rec.Prelude
			break
		}
	}
	for _, want := range []string{"tc-f", "/old-backup/ 备份目录可公开匿名列举",
		"来龙去脉(定稿叙述)", "单位 u1 执行", "发现单位:u1", "反证:2 轮", "确认时刻"} {
		if !strings.Contains(firstPrelude, want) {
			t.Fatalf("预载包缺 %q: %s", want, firstPrelude)
		}
	}

	// GET 会话视图:最新轮(缺省)= 第 2 轮一问;旧轮可指定回看。
	var v struct {
		Round      int              `json:"round"`
		Rounds     int              `json:"rounds"`
		Questions  int              `json:"questions"`
		Latest     int              `json:"latest_round"`
		Messages   []map[string]any `json:"messages"`
		ProviderOn bool             `json:"provider_on"`
	}
	get(t, h, "/api/tasks/"+task+"/shepherd/claim/tc-f", http.StatusOK, &v)
	if v.Round != 2 || v.Rounds != 2 || v.Questions != 1 || v.Latest != 2 || len(v.Messages) != 1 {
		t.Fatalf("最新轮视图不符: %+v", v)
	}
	if v.Messages[0]["text"] != "换个角度再问一遍" {
		t.Fatalf("最新轮应只含第 2 轮问话: %+v", v.Messages)
	}
	get(t, h, "/api/tasks/"+task+"/shepherd/claim/tc-f?round=1", http.StatusOK, &v)
	if v.Round != 1 || v.Questions != 2 || len(v.Messages) != 2 {
		t.Fatalf("第 1 轮回看视图不符: %+v", v)
	}

	// 发现详情 Followup 摘要:最新轮 + 问数。
	var d map[string]any
	get(t, h, "/api/tasks/"+task+"/claims/tc-f", http.StatusOK, &d)
	fu := d["followup"].(map[string]any)
	if fu["round"] != float64(2) || fu["rounds"] != float64(2) || fu["questions"] != float64(1) {
		t.Fatalf("Followup 摘要不符: %+v", fu)
	}

	// 牧者会话响应携带 followups 列表(会话页牧者组入口)。
	var sh struct {
		Followups []map[string]any `json:"followups"`
	}
	get(t, h, "/api/tasks/"+task+"/shepherd", http.StatusOK, &sh)
	if len(sh.Followups) != 2 {
		t.Fatalf("牧者组应挂两条追问条目(两轮),实为 %d", len(sh.Followups))
	}
	row := sh.Followups[1] // 首问次序:第 1 轮在前
	if row["claim_id"] != "tc-f" || row["round"] != float64(2) || row["questions"] != float64(1) ||
		row["statement"] != "/old-backup/ 备份目录可公开匿名列举" {
		t.Fatalf("追问条目不符: %+v", row)
	}

	// 问答不进报告:单条导出 md 不含任何问话原文。
	rec := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/claims/tc-f/export?format=md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("导出应 200,实为 %d", rec.Code)
	}
	md := rec.Body.String()
	for _, banned := range []string{"这个发现的证据在哪一步?", "竞争解释是什么?", "换个角度再问一遍", "(成果追问预载)"} {
		if strings.Contains(md, banned) {
			t.Fatalf("导出 md 不应含追问内容: %q 出现", banned)
		}
	}
}

// TestClaimFollowupInfoBar 追问会话信息条独立计:token 行按该轮会话锚
// 过滤求和,与牧者主对话分开(铺两行 token 用量对账)。
func TestClaimFollowupInfoBar(t *testing.T) {
	srv, h, store := newP414Server(t)
	task := seedP414Claim(t, store, h)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/tc-f/messages",
		`{"text":"问一句"}`, http.StatusOK, nil)

	// 铺 token 行:追问会话锚两行 + 牧者主对话锚一行。
	rows := []rings.TokenUsage{
		{TaskID: task, Agent: shepherd.ActorShepherd, TranscriptID: shepherd.ClaimQASID("tc-f", 1),
			Model: "m1", InputTokens: 100, OutputTokens: 20},
		{TaskID: task, Agent: shepherd.ActorShepherd, TranscriptID: shepherd.ClaimQASID("tc-f", 1),
			Model: "m1", InputTokens: 50, OutputTokens: 10},
		{TaskID: task, Agent: shepherd.ActorShepherd, TranscriptID: shepherd.SessionID(task),
			Model: "m1", InputTokens: 999, OutputTokens: 999},
	}
	if err := store.RecordTokens(t.Context(), rows...); err != nil {
		t.Fatalf("铺 token 行: %v", err)
	}

	var v struct {
		Info struct {
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
			Calls        int    `json:"calls"`
			Model        string `json:"model"`
		} `json:"info"`
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		get(t, h, "/api/tasks/"+task+"/shepherd/claim/tc-f", http.StatusOK, &v)
		if v.Info.Calls == 2 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if v.Info.InputTokens != 150 || v.Info.OutputTokens != 30 || v.Info.Model != "m1" {
		t.Fatalf("追问信息条应只计本会话锚的行: %+v", v.Info)
	}
	// 主对话信息条不受追问行影响。
	var sh struct {
		Info struct {
			InputTokens int `json:"input_tokens"`
			Calls       int `json:"calls"`
		} `json:"info"`
	}
	get(t, h, "/api/tasks/"+task+"/shepherd", http.StatusOK, &sh)
	if sh.Info.InputTokens != 999 || sh.Info.Calls != 1 {
		t.Fatalf("主对话信息条不应混入追问行: %+v", sh.Info)
	}
	_ = srv
}

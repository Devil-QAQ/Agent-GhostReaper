package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-59 ①(问题清单 #80①③)读侧优先:发现列表/详情的声明文本(存在性
// 描述/影响面)与事实描述(锤炼引用行/事实页签)优先定稿校对稿,未生成
// 退节点原文;机器判定与审计仍读节点(节点原文一字不动)。

// p459Setup 在 factsSetup 种子账本上铺一条带声明摘要的确认判断:链上
// 事实 f-cred 同挂 derived_from(FactRefs 消费)。
func p459Setup(t *testing.T) (http.Handler, *rings.Store, string) {
	t.Helper()
	h, store, task, _ := factsSetup(t)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-sum", NodeType: rings.NodeTestClaim, Title: "原机器语判断原文",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "impact": "原影响面原文", "vulnclass": "未授权访问"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-sum", ToNode: "f-cred"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-sum", Type: rings.TestClaimRecRepro, Severity: "high",
			Statement: "backup-01 的备份接口没有登录校验,任何知道地址的人都能下载备份",
			Impact:    "无需任何账号口令,可下载 2 个数据库备份",
			Facts:     []rings.FactStatement{{FactID: "f-cred", Statement: "备份文件里有后台账号与明文口令,拿到就能直接登录"}}}},
	)
	return h, store, task
}

func TestP459ClaimsListPrefersSummary(t *testing.T) {
	h, _, task := p459Setup(t)
	var claims []struct {
		ID        string `json:"id"`
		Statement string `json:"statement"`
		Impact    string `json:"impact"`
		FactRefs  []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"fact_refs"`
	}
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	for _, c := range claims {
		if c.ID != "tc-sum" {
			continue
		}
		if c.Statement != "backup-01 的备份接口没有登录校验,任何知道地址的人都能下载备份" ||
			c.Impact != "无需任何账号口令,可下载 2 个数据库备份" {
			t.Fatalf("列表声明文本应取校对稿: %+v", c)
		}
		if len(c.FactRefs) != 1 || c.FactRefs[0].ID != "f-cred" ||
			c.FactRefs[0].Title != "备份文件里有后台账号与明文口令,拿到就能直接登录" {
			t.Fatalf("锤炼引用行应取校对后事实描述: %+v", c.FactRefs)
		}
		return
	}
	t.Fatal("tc-sum 未出现在列表")
}

func TestP459ClaimDetailPrefersSummary(t *testing.T) {
	h, _, task := p459Setup(t)
	var body struct {
		Claim struct {
			Statement string `json:"statement"`
			Impact    string `json:"impact"`
		} `json:"claim"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-sum", http.StatusOK, &body)
	if body.Claim.Statement != "backup-01 的备份接口没有登录校验,任何知道地址的人都能下载备份" ||
		body.Claim.Impact != "无需任何账号口令,可下载 2 个数据库备份" {
		t.Fatalf("详情声明文本应取校对稿: %+v", body.Claim)
	}
}

func TestP459FactsListPrefersSummary(t *testing.T) {
	h, _, task := p459Setup(t)
	var body struct {
		Facts []struct {
			ID        string `json:"id"`
			Statement string `json:"statement"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)
	for _, f := range body.Facts {
		if f.ID == "f-cred" {
			if f.Statement != "备份文件里有后台账号与明文口令,拿到就能直接登录" {
				t.Fatalf("事实页签应取校对后事实描述: %q", f.Statement)
			}
			return
		}
	}
	t.Fatal("f-cred 未出现在事实页签")
}

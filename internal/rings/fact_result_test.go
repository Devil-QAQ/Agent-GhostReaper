package rings

import (
	"context"
	"reflect"
	"testing"
)

func TestFactResultsUseExplicitEvidenceMetadata(t *testing.T) {
	for _, statement := range []string{"没有取得密码，登录失败", "/admin/ 返回 404，没有权限", "只观察到 Cookie 响应头", "尝试 RCE 未生效", "取得密码与数据但此旧记录未分类"} {
		n := NodeFull{NodeView: NodeView{Type: NodeFact, Title: statement, Status: "confirmed"}}
		if got := FactResultsOf(n); !reflect.DeepEqual(got, []FactResultType{FactResultUnclassified}) {
			t.Fatalf("从文字猜测结果: %q %v", statement, got)
		}
		n.Detail = map[string]any{"result_types": []string{"plain"}}
		if got := FactResultsOf(n); !reflect.DeepEqual(got, []FactResultType{FactResultObservation}) {
			t.Fatalf("关键词覆盖明确的观察类型: %q %v", statement, got)
		}
	}
	for _, kind := range []FactResultType{FactResultCredential, FactResultData, FactResultPermission, FactResultFoothold} {
		n := NodeFull{NodeView: NodeView{Type: NodeFact, Title: "显示名称可以重写"}, Detail: map[string]any{"result_types": []FactResultType{kind}}}
		if got := FactResultsOf(n); !reflect.DeepEqual(got, []FactResultType{kind}) {
			t.Fatal("没有读取明确类型", got)
		}
	}
	n := NodeFull{NodeView: NodeView{Type: NodeFact}, Detail: map[string]any{"result_types": []string{"data", "credential", "data"}}}
	if got := FactResultsOf(n); !reflect.DeepEqual(got, []FactResultType{FactResultCredential, FactResultData}) {
		t.Fatal("多类成果被丢弃或重复", got)
	}
}

func TestFactResultValidationAndAuditedCorrection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "fact-results"
	for _, detail := range []map[string]any{
		{"result_types": "credential"}, {"result_types": []string{}}, {"result_types": []string{"invented"}},
		{"result_types": []string{"plain", "credential"}}, {"result_types": []string{"unclassified", "data"}},
		{"result_types": []string{"credential"}, "inferred": true},
	} {
		if _, err := s.Append(ctx, task, na(NodeFact, "bad", "观察", "confirmed", "", detail)); err == nil {
			t.Errorf("无效类型入账: %+v", detail)
		}
	}
	mustAppend(t, s, task, na(NodeFact, "f", "/admin/ 返回 404，未得到权限", "confirmed", "", map[string]any{"evidence": "HTTP 404"}))
	before, _ := s.Node(ctx, task, "f")
	mustAppend(t, s, task, Event{Kind: EventNodeAmended, Actor: "creator", Payload: NodeAmended{NodeID: "f", Detail: map[string]any{"result_types": []string{"plain"}}, Note: "核对响应仅为404，属于普通观察"}})
	after, _ := s.Node(ctx, task, "f")
	if before.Title != after.Title || before.Status != after.Status || after.Detail["evidence"] != "HTTP 404" {
		t.Fatal("分类修正改写事实或证据")
	}
	if got := FactResultsOf(*after); !reflect.DeepEqual(got, []FactResultType{FactResultObservation}) {
		t.Fatal(got)
	}
	changed := "改写原文"
	for _, p := range []NodeAmended{
		{NodeID: "f", Title: &changed, Detail: map[string]any{"result_types": []string{"plain"}}, Note: "改分类"},
		{NodeID: "f", Detail: map[string]any{"evidence": "替换证据", "result_types": []string{"plain"}}, Note: "改分类"},
		{NodeID: "f", Detail: map[string]any{"result_types": []string{"data"}}},
	} {
		if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "creator", Payload: p}); err == nil {
			t.Fatal("分类通道允许改原文、证据或缺依据")
		}
	}
}

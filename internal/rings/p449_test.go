package rings

import (
	"context"
	"testing"
)

// ---- P4-49(问题清单 #70):建任务注意事项字段 ----
// 事件/投影/读侧:指针语义(nil = 本次不动、非空 = 整体覆盖、空串 =
// 清空)、归一(空白/CRLF/空行折叠)、全量重放确定性。

func TestP449CreatorNotesProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-p449"

	// 非空指针:写入并读回。
	notes := "用小字典,不要用大字典\n不要修改任何数据"
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "manager", Payload: RoeUpdated{CreatorNotes: &notes}})
	r1, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE 失败: %v", err)
	}
	if r1.CreatorNotes != notes {
		t.Fatalf("注意事项应写入: got %q want %q", r1.CreatorNotes, notes)
	}

	// nil = 本次不动:透传其余字段的 roe.updated 快照(规则/范围端点
	// 的既有形态)不带注意事项,值保持。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		DeniedTools: []string{"bash"}, Note: "收紧工具"}})
	r2, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE(第二笔)失败: %v", err)
	}
	if r2.CreatorNotes != notes {
		t.Fatalf("nil 注意事项应保持现值: got %q", r2.CreatorNotes)
	}

	// 空串指针 = 清空。
	empty := ""
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{CreatorNotes: &empty}})
	r3, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE(第三笔)失败: %v", err)
	}
	if r3.CreatorNotes != "" {
		t.Fatalf("空串指针应清空注意事项: got %q", r3.CreatorNotes)
	}

	// 归一:首尾空白、CRLF、空行折叠。
	messy := "  用小字典 \r\n\r\n温柔点 \r\n"
	want := "用小字典\n温柔点"
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{CreatorNotes: &messy}})
	r4, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE(第四笔)失败: %v", err)
	}
	if r4.CreatorNotes != want {
		t.Fatalf("注意事项应归一: got %q want %q", r4.CreatorNotes, want)
	}

	// 全量重放确定性:清投影按事件流重建,注意事项逐笔叠加同结果。
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("全量重放失败: %v", err)
	}
	r5, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE(重放后)失败: %v", err)
	}
	if r5.CreatorNotes != want {
		t.Fatalf("重放后注意事项应一致: got %q want %q", r5.CreatorNotes, want)
	}
}

func TestP449NormalizeCreatorNotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"a", "a"},
		{" a \n b ", "a\nb"},
		{"a\r\nb", "a\nb"},
		{"a\n\n\nb", "a\nb"},
		{"\r\n a \r\n\r\n\r\n b \r\n", "a\nb"},
	}
	for _, c := range cases {
		if got := NormalizeCreatorNotes(c.in); got != c.want {
			t.Errorf("NormalizeCreatorNotes(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

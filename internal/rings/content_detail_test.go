package rings

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestContentIntentExplanationSnapshot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "content-detail"
	p465SeedStep(t, s, task, "s1", "执行原文与参数，不能被显示稿更改")
	original, _ := s.Node(ctx, task, "s1")
	explanation := strings.Repeat("完整说明保留条件。", 40) + "仅验证本次范围。"
	for _, tc := range []struct{ name, title, statement string }{
		{"带独立说明的新稿", "检查上传入口", explanation},
		{"旧版标题稿覆盖时不沿用过期说明", "检查备份入口", ""},
		{"再次完整替换", "检查登录限制", "计划验证错误次数限制，并非已经绕过。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustAppend(t, s, task, Event{Kind: EventStepTitle, Actor: ActorOvermindSafe, Payload: StepTitlePolish{StepID: "s1", Title: tc.title, Statement: tc.statement}})
			np, err := s.NodePolish(ctx, task)
			if err != nil || np["s1"].Title != tc.title || np["s1"].Statement != tc.statement {
				t.Fatalf("显示快照错误: %+v %v", np["s1"], err)
			}
			current, _ := s.Node(ctx, task, "s1")
			if !reflect.DeepEqual(original, current) {
				t.Fatal("显示说明改写了执行原文或字段")
			}
		})
	}
}

package rings

import (
	"context"
	"testing"
)

// P4-59 ①(问题清单 #80①③):声明摘要的账本与投影链路——repro 记录的
// Statement/Impact/Facts 落账回读、claim_cases.summary 小列随 repro 刷新
//(取代语义:新定稿未生成摘要时旧摘要一并清空)、ClaimDigests(列表)
// 与 ClaimCaseOf(详情)读侧透出。

func TestP459SummaryProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p459-sum"
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "tc-x", NodeType: NodeTestClaim, Title: "原始存在性判断原文", Status: TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1"}}},
		Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: TestClaimRecord{
			ClaimID: "tc-x", Type: TestClaimRecRepro, Severity: "high",
			Statement: "10.0.0.5 的管理接口没有登录校验,任何知道地址的人都能进入管理后台",
			Impact:    "无需任何账号口令,可查看全部 3,214 名用户的姓名与手机号",
			Facts:     []FactStatement{{FactID: "f-1", Statement: "登录接口不校验会话,未登录也能拿到管理数据"}},
			Steps: []ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2", Method: "GET",
				URL: "http://k3.local/admin", Status: 200, BlobRef: "b1"}},
		}},
	)
	// 列表摘要读侧:声明摘要随 ClaimDigests 透出(不触 repro 大列)。
	digests, err := s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("ClaimDigests: %v", err)
	}
	d := digests["tc-x"]
	if d.Statement == "" || d.Impact == "" || len(d.Facts) != 1 || d.Facts[0].FactID != "f-1" {
		t.Fatalf("摘要应随列表摘要透出: %+v", d)
	}
	// 卷宗读侧(详情/报告装配):Summary 全量在位。
	proj, ok, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil || !ok {
		t.Fatalf("ClaimCaseOf: ok=%v err=%v", ok, err)
	}
	if proj.Summary == nil || proj.Summary.Statement != d.Statement ||
		proj.Summary.Impact != d.Impact || len(proj.Summary.Facts) != 1 {
		t.Fatalf("卷宗摘要不符: %+v", proj.Summary)
	}

	// 重开后再确认的新定稿未生成摘要 → 摘要列一并清空(取代语义)。
	mustAppend(t, s, task,
		Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: TestClaimRecord{
			ClaimID: "tc-x", Type: TestClaimRecRepro, Severity: "high",
			Steps: []ReproStep{{Seq: 1, StepID: "s2", UnitID: "u3", Method: "GET",
				URL: "http://k3.local/admin", Status: 200, BlobRef: "b2"}},
		}},
	)
	digests, err = s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("ClaimDigests(二次): %v", err)
	}
	if d2 := digests["tc-x"]; d2.Statement != "" || d2.Impact != "" || len(d2.Facts) != 0 {
		t.Fatalf("新定稿无摘要应清空旧摘要: %+v", d2)
	}
	proj, _, err = s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil || proj.Summary != nil {
		t.Fatalf("卷宗摘要应清空: %+v err=%v", proj.Summary, err)
	}
}

// ActorOvermindSafe 测试侧 actor 常量(overmind 包外不可引,此处同词)。
const ActorOvermindSafe = "overmind"

package rings

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// P4-30 ③(问题清单 #51):report_review 复核标记记录的账本入口校验
// ——理由必填、锚点必须是本任务图上的 TestClaim;只追加,不涂改。

func TestReportReviewRecordValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p430-ring"
	mustAppend(t, s, task,
		na(NodeAsset, "web1", "http://mock", "active", AssetService, nil),
		na(NodeTestClaim, "tc-1", "登录滑块验证码可被脚本解算", TestClaimConfirmed, "",
			map[string]any{"unit_id": "u1"}),
	)

	// 合规标记:追加成功。
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "overmind",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecReportReview,
			Reason: "纯前置条件,无直接危害"}})

	// 理由为空:拒收。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "overmind",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecReportReview}}); err == nil || !strings.Contains(err.Error(), "复核理由为空") {
		t.Fatalf("无理由的复核标记须被拒收,得到 %v", err)
	}
	// 锚点不是判断:拒收。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "overmind",
		Payload: TestClaimRecord{ClaimID: "web1", Type: TestClaimRecReportReview, Reason: "信息性"}}); err == nil || !errors.Is(err, ErrReportReviewInvalid) {
		t.Fatalf("锚点非判断须被拒收(ErrReportReviewInvalid),得到 %v", err)
	}
	// 判断不在图上:拒收。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "overmind",
		Payload: TestClaimRecord{ClaimID: "tc-x", Type: TestClaimRecReportReview, Reason: "信息性"}}); err == nil || !errors.Is(err, ErrReportReviewInvalid) {
		t.Fatalf("不存在的判断须被拒收(ErrReportReviewInvalid),得到 %v", err)
	}
	// 账本不涂改:判断节点状态不动。
	n, err := s.Node(ctx, task, "tc-1")
	if err != nil {
		t.Fatalf("查判断失败: %v", err)
	}
	if n.Status != TestClaimConfirmed {
		t.Fatalf("标记后判断状态须保持 confirmed,实际 %s", n.Status)
	}
}

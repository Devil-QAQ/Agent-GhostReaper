package report

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-59 ①(问题清单 #80①③):报告侧声明文本优先定稿校对稿——标题/
// 存在性判断/影响面判断取 Summary(报告级文风),未生成退节点原文;
// 同账本重导字节一致(R17 零 LLM)。

func TestP459RenderPrefersSummary(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-p459"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	// 第二次定稿(取代语义)携带声明摘要:描述/危害按报告级锚改写。
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Statement: "10.10.0.5:8080 的备份目录没有登录校验,任何知道地址的人都能下载备份文件",
			Impact:    "无需任何账号口令,可下载 2 个数据库备份与 1 个配置备份",
			Facts:     []rings.FactStatement{{FactID: "f1", Statement: "备份目录 /old-backup/ 不需要登录即可列举"}},
			Narrative: "单位 u1 执行「审计主站敏感路径」时,在 /old-backup/ 的 200 响应正文里发现备份列举;u2 独立反证 1 轮排除缓存污染与归属错误,仲裁者判定成立。",
		}}); err != nil {
		t.Fatalf("铺带摘要定稿失败: %v", err)
	}
	ctx := context.Background()
	md, err := Render(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	// 标题与存在性判断取校对稿,原节点原文不再出现在该发现小节。
	for _, want := range []string{
		"### C-1 10.10.0.5:8080 的备份目录没有登录校验,任何知道地址的人都能下载备份文件",
		"- 存在性判断:10.10.0.5:8080 的备份目录没有登录校验,任何知道地址的人都能下载备份文件",
		"- 影响面判断:无需任何账号口令,可下载 2 个数据库备份与 1 个配置备份",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告应优先定稿校对稿(%s):\n%s", want, md)
		}
	}
	if strings.Contains(md, "存在性判断:/old-backup/ 备份目录可公开匿名列举") {
		t.Fatalf("存在性判断不应再用节点原文:\n%s", md)
	}
	// 待确认发现(tc-2,无定稿)退节点原文。
	if !strings.Contains(md, "存在性判断:/api/version 公开暴露服务版本号") {
		t.Fatalf("无定稿发现应退节点原文:\n%s", md)
	}
	// R17:同账本重导字节一致(声明摘要读投影列,零 LLM)。
	again, err := Render(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("二次渲染失败: %v", err)
	}
	if md != again {
		t.Fatalf("同账本重导应字节一致")
	}
}

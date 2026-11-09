package server

import (
	"context"
	"fmt"
	"strings"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 存量一次归位(P4-55 ⑤,问题清单 #75):P4-55 前的旧语义把两类本不该
// 待确认的声明落成了 pending——被推翻类(存在性被反证推翻,旧判
// "待复核")与"待重派"类(反证轮中断,旧判"待重派反证后复核"却无
// 重派机制,装死)。升级扫描把两类各归其位:
//   - 被推翻类 pending → 不成立(refuted):verdict(refuted,理由"被
//     反证推翻")+ 状态迁移,账本追加事件、历史不涂改;
//   - "待重派"类 pending → 回 testing:非终态任务由活循环(或唤醒后)
//     经收割侧中断检测自动重派;终态任务(stopped/completed)无循环
//     可触发,如实保持待确认(任务已收场,P4-43 语义)。
// 分类依据 = 声明卷宗投影的 pending_reason(latest-wins:verdict 与
// suspend 共写)——操作者挂起("方向被收窄")与任务终局清点
// ("任务终止/完成中断反证")不在两类之内,不动。
// 与 P4-21 迁移同模式:标记键写过即不再触碰,幂等。

// keyP455ClaimsMigrated P4-55 存量归位一次性迁移标记键。
const keyP455ClaimsMigrated = "migrate.p455.pending_claims"

// migratePendingClaimsOnce P4-55 ⑤ 存量归位:标记键已写直接返回;否则
// 扫描全部任务(墓碑除外)的 pending 声明按搁置理由归位,完成后写
// 标记键。分类理由为子串匹配,覆盖完整反证与单轮标准两套历史文案。
func migratePendingClaimsOnce(store *rings.Store) error {
	ctx := context.Background()
	if e, ok, err := store.Setting(ctx, keyP455ClaimsMigrated); err != nil {
		return fmt.Errorf("server: 读 P4-55 存量归位标记: %w", err)
	} else if ok && e.Value != "" {
		return nil
	}
	tasks, err := store.Tasks(ctx)
	if err != nil {
		return fmt.Errorf("server: P4-55 存量归位查任务: %w", err)
	}
	refutedN, testingN := 0, 0
	for _, t := range tasks {
		if t.Deleted {
			continue
		}
		terminal := t.Status == "stopped" || t.Status == "completed"
		digests, err := store.ClaimDigests(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("server: P4-55 存量归位查判断摘要 %s: %w", t.ID, err)
		}
		pendings, err := store.NodesByType(ctx, t.ID, rings.NodeTestClaim, rings.TestClaimPending)
		if err != nil {
			return fmt.Errorf("server: P4-55 存量归位查待确认判断 %s: %w", t.ID, err)
		}
		if len(pendings) == 0 {
			continue
		}
		rev, err := store.CurrentRevision(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("server: P4-55 存量归位查图版本 %s: %w", t.ID, err)
		}
		var events []rings.Event
		for _, c := range pendings {
			reason := digests[c.ID].PendingReason
			switch {
			case strings.Contains(reason, "存在性被反证推翻"):
				events = append(events,
					rings.Event{Kind: rings.EventTestClaimRecord, Actor: "system", Payload: rings.TestClaimRecord{
						ClaimID: c.ID, Type: rings.TestClaimRecVerdict,
						Decision: rings.VerdictRefuted, EvaluatedRev: rev,
						PendingReason: "被反证推翻",
						Reason:        "存量归位:旧语义把被推翻判成待复核,转不成立",
						Conditions: []rings.ArbiterConditionResult{{
							Kind: arbiter.CondExistenceNotRefuted, Required: 1, Actual: 0, Satisfied: false,
						}},
					}},
					rings.Event{Kind: rings.EventNodeStateChanged, Actor: "system",
						Payload: rings.NodeStateChanged{NodeID: c.ID, Status: rings.TestClaimRefuted,
							Note: "存量归位:被反证推翻转不成立"}},
				)
				refutedN++
			case strings.Contains(reason, "待重派反证后复核") && !terminal:
				events = append(events, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: "system",
					Payload: rings.NodeStateChanged{NodeID: c.ID, Status: rings.TestClaimTesting,
						Note: "存量归位:反证中断回锤炼,自动重派"},
				})
				testingN++
			}
		}
		if len(events) > 0 {
			if _, err := store.Append(ctx, t.ID, events...); err != nil {
				return fmt.Errorf("server: P4-55 存量归位记账 %s: %w", t.ID, err)
			}
		}
	}
	if err := store.SetSetting(ctx, keyP455ClaimsMigrated, "1", "system"); err != nil {
		return fmt.Errorf("server: 写 P4-55 存量归位标记: %w", err)
	}
	if refutedN > 0 || testingN > 0 {
		runlog.Infof("P4-55 存量归位:被推翻类转不成立 %d 个,待重派类回锤炼 %d 个(历史不涂改)",
			refutedN, testingN)
	}
	return nil
}

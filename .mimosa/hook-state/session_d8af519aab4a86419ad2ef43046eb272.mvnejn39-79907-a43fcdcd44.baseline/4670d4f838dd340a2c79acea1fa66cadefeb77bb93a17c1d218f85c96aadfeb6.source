// Package blackbox 实现黑匣子(BlackBox):系统级事件(预算状态变化、
// 聚合里程碑、故障转移、崩溃恢复)横切记账入年轮。设计依据:《设计决策
// 关键思想》第四章 §6 账本分工——预算状态变化(exhausted、settling、
// 硬停、终态)与聚合里程碑(预算过半)作为 blackbox.record 事件入账;
// 系统崩了它还在(记录用独立 ctx,不随任务熔断断链)。
package blackbox

import (
	"context"

	"ghostreaper/internal/rings"
)

// Record 把一条系统级事件写入年轮黑匣子通道。actor 标明留痕者
// (swarm:uN / overmind / manager),Scope(Step/Unit)把事件钉回图上
// 对象。失败原样上抛——调用方如实暴露到 LastErr,不静默吞掉审计缺口。
func Record(ctx context.Context, store *rings.Store, taskID, actor string, rec rings.BlackboxRecord) error {
	_, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventBlackboxRecord, Actor: actor, Payload: rec,
	})
	if err != nil {
		return err
	}
	return nil
}

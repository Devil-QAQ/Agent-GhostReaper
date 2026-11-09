// P4-51 ①②③(问题清单 #73②③):范围等待批准的对账唤醒。冻结在
// scope_wait 态的步骤不靠事件通道唤回——按状态对账:每次主宰轮起点
// 与"加入资产/禁止测试"两键裁决时,把全部 scope_wait 步骤的目标按
// 当前 RoE(现资产+放行/摘除清单)重判——已入范围即回可领取(插队
// 队首,scope_head 标记),已被排除即落中止(禁止测试,与摘除止损同
// 一收口)。通道无关由构造保证:审批卡/RoE 面板勾选/牧者对话改范围,
// 最终都经 roe.updated 落账并唤醒主宰,轮起点的对账自然看到现值;
// 两键裁决与 RoE 面板写路径另行直接调一次(免等下一轮,单位即时回队)。
// 重启收养同样覆盖(P4-42:拉起即保证一轮)。
package overmind

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// scopeAbortNote 禁止测试收口的步骤中止标记(与"范围摘除"同族,人话
// 可溯;不进 detail,只留事件流)。
const scopeAbortNote = "范围禁止测试"

// ResolveScopeWaits 范围等待对账:重判全部 scope_wait 步骤的目标并收口
// ——已入范围者回 dispatched(插队队首),已排除者落 aborted;有变更
// 即续派(有空位立即拉起,无空位排队中按 StepLaunchLess 队首等位)。
// 幂等:无 scope_wait 步骤零落笔;并发调用(轮起点与两键裁决)以
// AppendAtRevision 防重(版本过期方整批重读)。返回本轮唤醒与收口数。
// 可从面板裁决协程并发调用(与循环轮起点的对账互为竞态,靠版本重试
// 收敛;错误经 setErr 上锁写入)。
func (l *Loop) ResolveScopeWaits(ctx context.Context) (woken, terminated int) {
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, rings.StepScopeWait)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 范围对账查等待步骤: %w", err))
		return 0, 0
	}
	if len(steps) == 0 {
		return 0, 0
	}
	roe, err := l.Store.CurrentRoE(ctx, l.TaskID)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 范围对账读 RoE: %w", err))
		return 0, 0
	}
	assets, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeAsset, "")
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 范围对账查资产: %w", err))
		return 0, 0
	}
	approved := roe.ScopeApprovedSet()
	if approved == nil {
		approved = map[string]bool{}
	}
	excluded := roe.ExcludedAssetSet()
	// 摘除即去权(P4-54):排除资产不贡献对账闭包——被摘除目标的
	// scope_wait 步骤按排除分支收口,不再因旧闭包残留被误唤醒。
	closure := rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), approved, excluded)
	for pass := 0; pass < 3; pass++ {
		if pass > 0 {
			select {
			case <-ctx.Done():
				return woken, terminated
			default:
			}
			// 轮起点与两键裁决并发对账的竞态收敛:重读现表再判,直到
			// 无可处理条目(版本竞争的败者自然退出)。
			steps, err = l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, rings.StepScopeWait)
			if err != nil {
				return woken, terminated
			}
		}
		var evs []rings.Event
		var wakeIDs, abortIDs []string
		targets := map[string]bool{} // 本次唤醒/收口的目标("host\x00port",卡面收口面)
		for _, s := range steps {
			host, _ := s.Detail["scope_wait_host"].(string)
			if host == "" {
				continue // 无目标标记(历史/异常):留待人工,不猜
			}
			port, _ := s.Detail["scope_wait_port"].(string)
			if excluded != nil && rings.ExcludedCoversTarget(assets, excluded, host, port) {
				evs = append(evs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
					Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "aborted", Note: scopeAbortNote},
				})
				abortIDs = append(abortIDs, s.ID)
				targets[host+"\x00"+port] = true
				continue
			}
			if closure.CoversTarget(host, port, rings.DefaultScopeResolver) {
				// 回可领取+插队队首:scope_head 机器标记随迁移合并进
				// detail(StepLaunchLess 首位,被打断的老单位优先于新
				// 排队者)。
				evs = append(evs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
					Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "dispatched", Note: "scope_head"},
				})
				wakeIDs = append(wakeIDs, s.ID)
				targets[host+"\x00"+port] = true
			}
		}
		// 通道无关联动的卡面收口(P4-51 ⑥):目标经其他通道(RoE 面板/
		// 资产页/牧者)解锁或排除后,该目标的待决卡使命已终——按判向
		// 自动落裁决记录(已入范围=批准语义、已排除=拒绝语义),红点即
		// 消不留僵尸卡。卡两键的显式裁决先落记录,此处查不到未决卡,
		// 零重复;裁决者记 creator(范围变更的动作发起者)。
		evs = append(evs, l.scopeCardCloseEvents(ctx, assets, excluded, closure, targets)...)
		if len(evs) == 0 {
			break
		}
		rev, rerr := l.Store.CurrentRevision(ctx, l.TaskID)
		if rerr != nil {
			l.setErr(fmt.Errorf("overmind: 范围对账查图版本: %w", rerr))
			return woken, terminated
		}
		if _, aerr := l.Store.AppendAtRevision(ctx, l.TaskID, rev, evs...); aerr != nil {
			if errors.Is(aerr, rings.ErrStaleRevision) {
				continue // 并发写竞争:整批重读重判
			}
			l.setErr(fmt.Errorf("overmind: 范围对账收口落账: %w", aerr))
			return woken, terminated
		}
		woken += len(wakeIDs)
		terminated += len(abortIDs)
		if len(wakeIDs) > 0 || len(abortIDs) > 0 {
			runlog.Infof("范围对账: %s 唤醒 %d 个等待单位(队首),收口 %d 个(禁止测试)",
				l.TaskID, len(wakeIDs), len(abortIDs))
		}
		break
	}
	// 有空位立即拉起(无空位排队中按队首等位,名额空出的续派交接既有)。
	if woken > 0 {
		l.schedule(ctx)
	}
	return woken, terminated
}

// scopeCardCloseEvents 受影响目标的未决范围卡收口事件:目标已入范围→
// 批准语义记录,已排除→拒绝语义记录(Reason 注明经其他通道自动收口)。
// 同批事件随对账落地(与步骤收口同版本防重)。
func (l *Loop) scopeCardCloseEvents(ctx context.Context, assets []rings.NodeFull,
	excluded map[string]bool, closure *rings.ScopeClosure, targets map[string]bool) []rings.Event {
	var out []rings.Event
	for key := range targets {
		host, port, _ := strings.Cut(key, "\x00")
		rid := rings.ScopeCardID(host, port)
		rows, err := l.Store.ApprovalByRequest(ctx, l.TaskID, rid)
		if err != nil || len(rows) == 0 {
			continue
		}
		card := approvalRowToRecord(rows[len(rows)-1])
		if card.Type != rings.ApprovalRecScopeWait {
			continue // 已裁决(含卡两键显式裁决):零重复
		}
		deny := excluded != nil && rings.ExcludedCoversTarget(assets, excluded, host, port)
		if !deny && !closure.CoversTarget(host, port, rings.DefaultScopeResolver) {
			continue // 目标既未入范围也未排除(理论不可达,防御)
		}
		card.DecidedBy = "creator"
		card.Reason = "目标已入范围(经其他通道调整),卡自动收口"
		if deny {
			card.Type = rings.ApprovalRecDenied
			card.DenyLine = rings.DenyLineRoeScope
			card.Reason = "目标已排除(经其他通道调整),卡自动收口"
		} else {
			card.Type = rings.ApprovalRecApproved
			card.DenyLine = ""
		}
		out = append(out, rings.Event{
			Kind: rings.EventApprovalRecord, Actor: ActorOvermind, Payload: card,
		})
		runlog.Infof("范围卡收口: %s %s %s", l.TaskID, rid, card.Reason)
	}
	return out
}

// approvalRowToRecord 投影行 → 审批记录(卡面回写复用;与 server 侧
// 同名助手同构,读侧各自持有)。
func approvalRowToRecord(row rings.ApprovalRow) rings.ApprovalRecord {
	return rings.ApprovalRecord{
		RequestID: row.RequestID, StepID: row.StepID, UnitID: row.UnitID,
		Type: row.Type, ActionLevel: rings.ActionLevel(row.ActionLevel),
		HarmClass: rings.HarmClass(row.HarmClass), Intent: row.Intent,
		ToolName: row.ToolName, ToolArgs: []byte(row.ToolArgs),
		Note: row.Note, DenyLine: row.DenyLine, DecidedBy: row.DecidedBy,
		Reason: row.Reason, WaitMillis: row.WaitMillis,
	}
}

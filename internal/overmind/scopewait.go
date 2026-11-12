// P4-51 ①②③(问题清单 #73②③):范围等待批准的对账唤醒。冻结在
// scope_wait 态的步骤不靠事件通道唤回——按状态对账:每次主宰轮起点
// 与范围卡裁决("加入资产/允许访问/禁止测试")时,把全部 scope_wait
// 步骤的目标按当前 RoE(现资产+放行/摘除清单+信息源清单)重判——已
// 入范围或已获访问授权(P4-68 ① 信息源清单,同一状态多种入口)即回
// 可领取(插队队首,scope_head 标记),已被排除按 P4-69 ① 分族收口
// (步骤意图覆盖被排除目标→终止留痕;不覆盖→唤醒换路,续跑指令注入
// "目标已排除"反馈)。通道无关由构造保证:审批卡/RoE 面板勾选/牧者
// 对话改范围,最终都经 roe.updated 落账并唤醒主宰,轮起点的对账自然
// 看到现值;卡三键与 RoE 面板写路径另行直接调一次(免等下一轮,单位
// 即时回队)。重启收养同样覆盖(P4-42:拉起即保证一轮)。
// P4-69 ①(摘除联动收口):未决范围卡全量重判——目标已排除/已入范围
// /已获访问授权的卡一律按判向自动落裁决记录收口,不再依赖"该目标还
// 有挂等步骤"(红队可否决卡无挂等步骤,僵尸卡形态由此根治:实锤
// sc-example.com:80 卡挂死)。
package overmind

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// scopeAbortNote 禁止测试收口的步骤中止标记(与"范围摘除"同族,人话
// 可溯;不进 detail,只留事件流)。
const scopeAbortNote = "范围禁止测试"

// ResolveScopeWaits 范围等待对账:重判全部 scope_wait 步骤的目标并收口
// ——已入范围者回 dispatched(插队队首),已排除者按意图覆盖分族(覆盖
// →aborted 终止留痕;不覆盖→回 dispatched 唤醒换路,P4-69 ①);同时
// 全量重判未决范围卡(目标已排除→拒绝语义收口、已入范围→批准语义、
// 已获访问授权→允许访问语义)。有变更即续派(有空位立即拉起,无空位
// 排队中按 StepLaunchLess 队首等位)。幂等:无 scope_wait 步骤且无未决
// 卡零落笔;并发调用(轮起点与两键裁决)以 AppendAtRevision 防重(版本
// 过期方整批重读)。返回本轮唤醒与收口数。可从面板裁决协程并发调用
// (与循环轮起点的对账互为竞态,靠版本重试收敛;错误经 setErr 上锁
// 写入)。
func (l *Loop) ResolveScopeWaits(ctx context.Context) (woken, terminated int) {
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, rings.StepScopeWait)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 范围对账查等待步骤: %w", err))
		return 0, 0
	}
	// 未决范围卡全量重判面(P4-69 ①:与步骤对账互相独立——卡存在
	// 不要求挂等步骤仍在)。
	cards, cerr := l.Store.PendingScopeCards(ctx, l.TaskID)
	if cerr != nil {
		l.setErr(fmt.Errorf("overmind: 范围对账查未决卡: %w", cerr))
		return 0, 0
	}
	if len(steps) == 0 && len(cards) == 0 {
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
		for _, s := range steps {
			host, _ := s.Detail["scope_wait_host"].(string)
			if host == "" {
				continue // 无目标标记(历史/异常):留待人工,不猜
			}
			port, _ := s.Detail["scope_wait_port"].(string)
			if excluded != nil && rings.ExcludedCoversTarget(assets, excluded, host, port) {
				// P4-69 ① 分族(判定键＝图上意图-目标覆盖关系,与
				// P4-68 ② 命令路同一单源):步骤意图覆盖被排除目标
				//(工作线是它)→终止留痕;意图不覆盖(命令顺带触碰,
				// 挂等只因一条连通性命令)→唤醒回队队首换路——续跑
				// 指令按目标现值注入"目标已排除"反馈(scopeExcluded-
				// ResumePrompt),对该目标的后续命令由命令路只拒该条。
				covering := rings.ExcludedAssetsCovering(assets, excluded, host, port)
				if swarm.StepIntentCoversAssets(ctx, l.Store, l.TaskID, s.ID, covering, assets) {
					evs = append(evs, rings.Event{
						Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
						Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "aborted", Note: scopeAbortNote},
					})
					abortIDs = append(abortIDs, s.ID)
					continue
				}
				evs = append(evs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
					Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "dispatched", Note: "scope_head"},
				})
				wakeIDs = append(wakeIDs, s.ID)
				continue
			}
			// P4-68 ①:信息源清单命中(卡第三键"允许访问"/牧者标记/分诊
			// 自动授——同一状态,卡上手授＝分诊自动授):目标获访问授权,
			// 挂等单位回可领取插队队首,重领单位原会话续跑(续跑指令按
			// 信息源现值区分措辞——只授访问权不是测试目标);重发的访问
			// 命令经清单命中放行,对目标的主动攻击动作仍走攻击行为卡。
			if roe.InfoSourceHit(host) {
				evs = append(evs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
					Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "dispatched", Note: "scope_head"},
				})
				wakeIDs = append(wakeIDs, s.ID)
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
			}
		}
		// 通道无关联动的卡面收口(P4-51 ⑥ → P4-69 ① 全量重判):目标
		// 经其他通道(RoE 面板/资产页/牧者)解锁或排除后,该目标的待决
		// 卡使命已终——按判向自动落裁决记录(已入范围=批准语义、已排除
		// =拒绝语义、已获访问授权=允许访问语义〔P4-68〕),红点即消不
		// 留僵尸卡。卡两键的显式裁决先落记录,此处查不到未决卡,零重复;
		// 裁决者记 creator(范围变更的动作发起者)。
		evs = append(evs, l.scopeCardCloseEvents(assets, excluded, closure, roe)...)
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

// scopeCardCloseEvents 全部未决范围卡的收口事件(P4-69 ① 全量重判,
// 不依赖挂等步骤——红队可否决卡无挂等步骤同受治):目标已入范围→
// 批准语义记录;已获访问授权(信息源清单命中,P4-68)→允许访问语义
// 记录;已排除→拒绝语义记录(Reason 注明经其他通道自动收口)。同批
// 事件随对账落地(与步骤收口同版本防重)。
func (l *Loop) scopeCardCloseEvents(assets []rings.NodeFull,
	excluded map[string]bool, closure *rings.ScopeClosure, roe rings.RoeState) []rings.Event {
	cards, err := l.Store.PendingScopeCards(context.Background(), l.TaskID)
	if err != nil || len(cards) == 0 {
		return nil
	}
	var out []rings.Event
	for _, c := range cards {
		rid := c.RequestID
		card := approvalRowToRecord(c)
		host := strings.TrimPrefix(rid, rings.ScopeCardIDPrefix)
		port := ""
		if a, ok := rings.ScopeAnchorOf(host); ok {
			host, port = a.Host, a.Port
		}
		deny := excluded != nil && rings.ExcludedCoversTarget(assets, excluded, host, port)
		access := !deny && roe.InfoSourceHit(host)
		if !deny && !access && !closure.CoversTarget(host, port, rings.DefaultScopeResolver) {
			continue // 目标既未入范围也未排除:继续等裁决,不猜
		}
		card.DecidedBy = "creator"
		card.Reason = "目标已入范围(经其他通道调整),卡自动收口"
		switch {
		case deny:
			card.Type = rings.ApprovalRecDenied
			card.DenyLine = rings.DenyLineRoeScope
			card.Reason = "目标已排除(经其他通道调整),卡自动收口"
		case access:
			card.Type = rings.ApprovalRecAccess
			card.DenyLine = ""
			card.Reason = "目标已获访问授权(经其他通道调整),卡自动收口"
		default:
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

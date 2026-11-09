// Package server 的事实列表 API(P2-10,8.3 发现域·事实页签):已确认
// 事实的一等列表——成果类(凭据/数据/权限/立足点,report.LootClass
// 确定性查表)、资产锚点、入账/确认时刻一次发齐,面板按类型/资产/时间
// 筛选;与图上事实节点经跳转协议互跳(?focus= 定位)。

package server

import (
	"fmt"
	"net/http"

	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
)

// factAssetView 事实条目上的资产锚点(about 边 Fact→Asset)。
type factAssetView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// factRow 事实页签条目:节点投影 + 成果类 + 资产锚点 + 时刻(自审计流
// 取 occurred_at,确定性口径同报告——不取读侧墙钟)。
// P4-56 四页签口径:新进候选事实一并返回(IsNew=true,面板标"新"——
// 主宰收割确认后自动转已确认);已成立假设显示层迁入本页(Kind=
// hypothesis,FromHypothesis=true 标"由假设升级",点击开假设抽屉),
// 假设页不留成立条目。
type factRow struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"` // fact|hypothesis(迁入的已成立假设)
	Statement      string          `json:"statement"`
	Loot           string          `json:"loot,omitempty"` // credential|data|permission|foothold(空=普通观察)
	Assets         []factAssetView `json:"assets"`
	UnitID         string          `json:"unit_id,omitempty"`
	EvidenceHead   string          `json:"evidence_head,omitempty"`
	EvidenceRef    string          `json:"evidence_ref,omitempty"`
	CreatedRev     int64           `json:"created_rev"`
	ConfirmedRev   int64           `json:"confirmed_rev"`
	CreatedAt      string          `json:"created_at,omitempty"`
	ConfirmedAt    string          `json:"confirmed_at,omitempty"`
	IsNew          bool            `json:"is_new,omitempty"`          // 候选事实(P4-56:短暂标"新")
	FromHypothesis bool            `json:"from_hypothesis,omitempty"` // 由假设升级(迁入来路,P4-56 ④)
}

// handleFactsList 事实页签列表(GET /api/tasks/{id}/facts):已确认事实
// 一等列表 + 新进候选(标"新",收割后自动转正)+ 已成立假设(P4-56
// 显示层迁入,带"由假设升级"来路);入账序返回,面板自上而下取最新即
// 倒序读(与虫群单位列表同口径)。
// P4-36:入账/确认时刻读 nodes 投影列(added_at/confirmed_at 由写侧
// 同事务维护)——不再扫描事件流。
func (s *Server) handleFactsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	facts, err := s.Store.NodesByType(ctx, taskID, rings.NodeFact, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 已成立假设(P4-56 ②:验证成立迁入事实页,本页不留;假设侧状态机
	// 不动,仅显示层归位)。
	hypos, err := s.Store.NodesByType(ctx, taskID, rings.NodeHypothesis, rings.HypothesisConfirmed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	assetTitle := map[string]string{}
	if ag, err := s.Store.AssetGraph(ctx, taskID); err == nil {
		for _, a := range ag.Assets {
			assetTitle[a.ID] = a.Title
		}
	}
	// 定稿校对后的事实描述优先表(P4-59 ①,问题清单 #80①③):反查自
	// 各判断的声明摘要(claim_cases.summary 小列),后定稿覆盖先定稿;
	// 未被任何确认判断携带的事实无校对稿,退节点原文(其陈述受生成时
	// 结构化字段约束)。
	polishedFact := map[string]string{}
	if digests, derr := s.Store.ClaimDigests(ctx, taskID); derr == nil {
		polishedFact = polishedFactMapOf(digests)
	}

	out := make([]factRow, 0, len(facts)+len(hypos))
	for _, f := range facts {
		if f.Status != "confirmed" && f.Status != "candidate" {
			continue // 其余状态(防御性词位)如实跳过,不进一等列表
		}
		row := factRow{
			ID: f.ID, Kind: "fact", Statement: f.Title, Loot: report.LootClass(f.Title),
			CreatedRev: f.CreatedRev, IsNew: f.Status == "candidate",
			Assets: []factAssetView{},
		}
		if p := polishedFact[f.ID]; p != "" {
			row.Statement = p
		}
		row.CreatedAt = f.AddedAt
		if f.ConfirmedAt != "" {
			row.ConfirmedAt, row.ConfirmedRev = f.ConfirmedAt, f.ConfirmedRev
		} else {
			row.ConfirmedAt, row.ConfirmedRev = row.CreatedAt, f.CreatedRev
		}
		row.UnitID, _ = f.Detail["unit_id"].(string)
		row.EvidenceHead, _ = f.Detail["evidence_head"].(string)
		row.EvidenceRef, _ = f.Detail["evidence_ref"].(string)
		if row.EvidenceHead == "" {
			row.EvidenceHead, _ = f.Detail["evidence"].(string)
		}
		for _, e := range abouts {
			if e.From == f.ID {
				row.Assets = append(row.Assets, factAssetView{ID: e.To, Title: assetTitle[e.To]})
			}
		}
		out = append(out, row)
	}
	for _, h := range hypos {
		row := factRow{
			ID: h.ID, Kind: "hypothesis", Statement: h.Title, Loot: report.LootClass(h.Title),
			CreatedRev: h.CreatedRev, FromHypothesis: true,
			Assets: []factAssetView{},
		}
		row.CreatedAt = h.AddedAt
		if h.ConfirmedAt != "" {
			row.ConfirmedAt, row.ConfirmedRev = h.ConfirmedAt, h.ConfirmedRev
		} else {
			row.ConfirmedAt, row.ConfirmedRev = row.CreatedAt, h.CreatedRev
		}
		row.UnitID, _ = h.Detail["unit_id"].(string)
		for _, e := range abouts {
			if e.From == h.ID {
				row.Assets = append(row.Assets, factAssetView{ID: e.To, Title: assetTitle[e.To]})
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"facts": out})
}

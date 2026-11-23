package swarm

// 只读查询工具(P5-04 虫群知情化):知情不扩权——纯只读、无任何执行
// 权,查到范围外资产真去测时 RoE 照拦。五个能力:查事实(search_facts)、
// 查漏洞(list_claims,既有查重工具)、查资产(list_assets)、搜其他
// 虫群轨迹(search_trails)、查历史流量(traffic_search / traffic_get /
// traffic_blob——虫群与牧者同款配发,问题清单 #7/#94)。
// 说明即引导:每个工具的说明里写一句大白话(先查再干/先查流量),
// 模型决定用不用工具的瞬间读到的就是这句,不靠别的机制。工具失败
// 返回错误信息本身,绝不阻塞其他工具与执行。数据全部走既有投影查询
// (NodesByType / Edges / EventsByKinds / TrafficRows),零新扫描路径。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// ---- 工具目录(名称 + 描述单一来源,ToolCatalog 共用) ----

const (
	toolDescSearchFacts = "只读查询:按关键词或资产查本任务已确认的事实清单(编号+摘要)。先查再干,别人验过的别重验——打算验证一个结论前,先查它是否已在事实里。keyword 与 asset_id 至少给一个,两者都给时取交集。"

	toolDescListAssets = "只读查询:列出本任务资产及其已确认关系和依据事实编号，可按keyword或asset_id查询。关系不代表授权或新的执行指令。动手前先对清单，已有资产直接用编号引用，不要重复发现或自己猜编号。"

	toolDescSearchTrails = "只读查询:按关键词搜其他虫群单位的执行轨迹(每次工具调用与其参数)和收场结论。试过的方法、踩过的坑都在轨迹里——排活前先搜,别人走过的路别重走。"

	toolDescTrafficSearch = "只读查询:按 host 搜本任务的历史流量(全部请求的镜留痕)。必须限定 host 才给查;可选 method(GET/POST…)与 status(2~5 对应 2xx~5xx)。结果只回轻量摘要(编号/方法/URL/状态码,不带正文),默认 10 条、最多 20 条;看某条的详情用 traffic_get,看请求/响应原文用 traffic_blob 按编号单取。先查流量,别重复 curl 同一 URL。"

	toolDescTrafficGet = "只读查询:按编号取一条历史流量的元数据(方法/URL/状态码/响应头/正文大小/发生时刻),不带正文——原始报文总长在此,取原文用 traffic_blob 按段读。"

	toolDescTrafficBlob = "只读查询:按编号取一条历史流量的原始报文分段(请求行/头/体 + 状态行/头/体的完整原文)。offset/limit 分段读(默认 16KB、单段上限 64KB),side 可选 req(仅请求包)或 resp(仅返回包)。先用 traffic_search 拿编号、traffic_get 看大小,再按段取原文——不要盲目从头翻。"
)

// roQueryCap 通用只读查询的结果条数上限(轻量返回,防刷屏;全量走
// 分页或更窄的过滤)。
const roQueryCap = 20

// ---- 查事实 ----

// searchFactsTool 按关键词/资产查已确认事实(确认层的按需查询面;
// 任务包只带本步骤相关的事实,这里是全局按需查)。
func (env *unitEnv) searchFactsTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "search_facts",
		Description: toolDescSearchFacts,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":  map[string]any{"type": "string", "description": "关键词(事实摘要的子串匹配,大小写不敏感)"},
				"asset_id": map[string]any{"type": "string", "description": "资产编号(只看挂在该资产上的事实)"},
			},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Keyword string `json:"keyword"`
				AssetID string `json:"asset_id"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			keyword := strings.ToLower(strings.TrimSpace(args.Keyword))
			assetID := strings.TrimSpace(args.AssetID)
			if keyword == "" && assetID == "" {
				return tool.Errorf("keyword 与 asset_id 至少给一个:按关键词搜,或按资产编号过滤"), nil
			}
			facts, err := env.Executor.Store.NodesByType(ctx, env.TaskID, rings.NodeFact, "confirmed")
			if err != nil {
				return tool.Errorf("查事实失败: " + err.Error()), nil
			}
			// 资产过滤面:about 边(fact → asset)。
			assetFilter := map[string]bool{}
			if assetID != "" {
				if _, err := env.Executor.Store.Node(ctx, env.TaskID, assetID); err != nil {
					return tool.Errorf("资产 " + assetID + " 不在本任务图上: " + err.Error()), nil
				}
				if edges, eerr := env.Executor.Store.Edges(ctx, env.TaskID, rings.EdgeAbout); eerr == nil {
					for _, e := range edges {
						if e.To == assetID {
							assetFilter[e.From] = true
						}
					}
				}
			}
			var lines []string
			for i := len(facts) - 1; i >= 0 && len(lines) < roQueryCap; i-- {
				f := facts[i]
				if assetID != "" && !assetFilter[f.ID] {
					continue
				}
				if keyword != "" && !strings.Contains(strings.ToLower(f.Title), keyword) {
					continue
				}
				lines = append(lines, fmt.Sprintf("%s: %s", f.ID, clipLine(f.Title, 160)))
			}
			if len(lines) == 0 {
				return tool.Text("没有命中的已确认事实(换关键词再试,或该资产名下暂无事实)。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "命中 %d 条(已确认事实,最新在前):\n", len(lines))
			for _, l := range lines {
				b.WriteString("  - " + l + "\n")
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// ---- 查资产 ----

// listAssetsTool 查任务资产清单(锚定引用的编号面——事实/交卷的
// asset_id 都从这里取;只给编号/类别/标识,不给覆盖进度)。
func (env *unitEnv) listAssetsTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "list_assets",
		Description: toolDescListAssets,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":         map[string]any{"type": "string", "description": "关键词(标识/类别的子串匹配,可空=全部)"},
				"asset_id":        map[string]any{"type": "string", "description": "限定本任务资产编号，同时读取其父子层级和已有明确关联"},
				"relation_offset": map[string]any{"type": "integer", "minimum": 0, "description": "已确认资产关系的分页起点，默认0，每页最多20条；有下一页时按返回提示继续查询"},
			},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Keyword        string `json:"keyword"`
				AssetID        string `json:"asset_id"`
				RelationOffset int    `json:"relation_offset"`
			}
			if err := json.Unmarshal(in, &args); err != nil || args.RelationOffset < 0 {
				return tool.Errorf("查询参数无效，关系分页起点须为非负整数"), nil
			}
			keyword := strings.ToLower(strings.TrimSpace(args.Keyword))
			ag, err := env.Executor.Store.AssetGraph(ctx, env.TaskID)
			if err != nil {
				return tool.Errorf("查资产清单失败: " + err.Error()), nil
			}
			assets := ag.Assets
			selected := map[string]bool{}
			var lines []string
			for _, a := range assets {
				if strings.TrimSpace(args.AssetID) != "" && a.ID != strings.TrimSpace(args.AssetID) {
					continue
				}
				if keyword != "" &&
					!strings.Contains(strings.ToLower(a.Title+" "+string(a.AssetClass)+" "+a.ID), keyword) {
					continue
				}
				lines = append(lines, fmt.Sprintf("%s [%s] %s", a.ID, a.AssetClass, a.Title))
				selected[a.ID] = true
				if len(lines) >= roQueryCap {
					break
				}
			}
			if len(lines) == 0 {
				return tool.Text("任务资产清单为空或无命中(图上暂无资产,或换关键词再试)。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "任务资产 %d 条:\n", len(assets))
			for _, l := range lines {
				b.WriteString("  - " + l + "\n")
			}
			if len(assets) > len(lines) {
				fmt.Fprintf(&b, "  (共 %d 条,只列前 %d 条——用 keyword 缩小范围)\n", len(assets), len(lines))
			}
			b.WriteString("\n资产层级:\n")
			n := 0
			for _, e := range ag.Hierarchy {
				if selected[e.From] || selected[e.To] {
					fmt.Fprintf(&b, "  - %s 属于 %s\n", e.From, e.To)
					n++
					if n >= roQueryCap {
						break
					}
				}
			}
			b.WriteString("已确认的资产关系(不增加执行权):\n")
			n = 0
			matched := 0
			for _, r := range ag.Relations {
				if selected[r.From] || selected[r.To] {
					matched++
					if matched <= args.RelationOffset {
						continue
					}
					if n >= roQueryCap {
						fmt.Fprintf(&b, "  (还有关系，保持查询条件并用relation_offset=%d继续)\n", args.RelationOffset+n)
						break
					}
					b.WriteString("  - " + r.Summary() + "\n")
					n++
				}
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// ---- 搜其他虫群轨迹 ----

// searchTrailsTool 按关键词搜其他单位的执行轨迹(工具调用与参数、
// 收场结论)——别人做过什么、结论是什么,排活前先搜,防重复劳动。
func (env *unitEnv) searchTrailsTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "search_trails",
		Description: toolDescSearchTrails,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword": map[string]any{"type": "string", "description": "关键词(工具名/参数/结论的子串匹配,大小写不敏感)"},
			},
			"required": []string{"keyword"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Keyword string `json:"keyword"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			keyword := strings.ToLower(strings.TrimSpace(args.Keyword))
			if keyword == "" {
				return tool.Errorf("keyword 不能为空:写要搜的工具名/路径/结论关键词"), nil
			}
			evs, err := env.Executor.Store.EventsByKinds(ctx, env.TaskID, rings.EventSwarmRecord)
			if err != nil {
				return tool.Errorf("搜轨迹失败: " + err.Error()), nil
			}
			var lines []string
			for _, ev := range evs {
				var rec rings.SwarmRecord
				if json.Unmarshal(ev.Payload, &rec) != nil {
					continue
				}
				if rec.UnitID == env.UnitID {
					continue // 其他单位的轨迹(自己的在会话历史里)
				}
				var line string
				switch rec.Type {
				case rings.SwarmRecToolCall:
					if !strings.Contains(strings.ToLower(rec.ToolName+" "+string(rec.ToolArgs)), keyword) {
						continue
					}
					mark := ""
					if rec.IsError {
						mark = " (出错)"
					}
					line = fmt.Sprintf("%s %s 单位%s · %s %s%s", ev.OccurredAt.Local().Format("01-02 15:04"),
						rec.StepID, rec.UnitID, rec.ToolName, toolCallBriefOf(rec.ToolArgs), mark)
				case rings.SwarmRecSessionEnd:
					if !strings.Contains(strings.ToLower(rec.Note), keyword) {
						continue
					}
					line = fmt.Sprintf("%s %s 单位%s · 收场结论: %s", ev.OccurredAt.Local().Format("01-02 15:04"),
						rec.StepID, rec.UnitID, clipLine(rec.Note, 160))
				default:
					continue
				}
				lines = append(lines, line)
				if len(lines) > roQueryCap {
					lines = lines[1:] // 滚动保留最新(倒扫前先收满,尾部即最新)
				}
			}
			if len(lines) == 0 {
				return tool.Text("没有命中的其他单位轨迹(换关键词再试)。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "命中 %d 条(其他单位轨迹,最新在后):\n", len(lines))
			for _, l := range lines {
				b.WriteString("  - " + l + "\n")
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// toolCallBriefOf 工具参数的一行摘要(截断,完整参数在账本)。
func toolCallBriefOf(args json.RawMessage) string {
	s := strings.TrimSpace(string(args))
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// ---- 查历史流量(虫群与牧者同款,P5-04;防慢三规矩随说明下发) ----
//
// 三规矩(不满足过滤条件的裸搜不许上线):搜必须先限定 host;默认
// 只回几条轻量摘要(编号/URL/状态码,不带正文);看原文按编号单取。

const (
	trafficSearchDefault = 10 // traffic_search 默认条数
	trafficSearchMax     = 20 // 上限(轻量摘要,防刷屏)
	trafficBlobSegDef    = 16 << 10
	trafficBlobSegMax    = 64 << 10
)

// TrafficSearchTool 按 host 搜历史流量(轻量摘要)。虫群与牧者同款
// 配发——单一实现,两边工具面共用。
func TrafficSearchTool(store *rings.Store, taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "traffic_search",
		Description: toolDescTrafficSearch,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host":   map[string]any{"type": "string", "description": "host(必填:域名或 IP,子串匹配)"},
				"method": map[string]any{"type": "string", "description": "请求方法过滤(GET/POST…,精确等值;可空)"},
				"status": map[string]any{"type": "integer", "description": "状态类过滤:2/3/4/5 对应 2xx~5xx(可空)"},
				"limit":  map[string]any{"type": "integer", "description": "返回条数(默认 10,最多 20)"},
			},
			"required": []string{"host"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Host   string `json:"host"`
				Method string `json:"method"`
				Status int    `json:"status"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			host := strings.TrimSpace(args.Host)
			if host == "" {
				return tool.Errorf("host 必填:先限定 host 再搜(不限定 host 的全量翻查不给做)"), nil
			}
			limit := args.Limit
			if limit <= 0 {
				limit = trafficSearchDefault
			}
			if limit > trafficSearchMax {
				limit = trafficSearchMax
			}
			f := rings.TrafficFilter{TaskID: taskID, Host: host, Method: args.Method, Limit: limit}
			if args.Status != 0 {
				if args.Status < 2 || args.Status > 5 {
					return tool.Errorf("status 须为 2~5(对应 2xx~5xx)"), nil
				}
				f.StatusClass = args.Status
			}
			rows, total, err := store.TrafficRows(ctx, f)
			if err != nil {
				return tool.Errorf("查流量失败: " + err.Error()), nil
			}
			if len(rows) == 0 {
				return tool.Text("没有命中的流量记录(确认 host 写法,或换 method/status 过滤)。"), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "命中 %d 条(共 %d 条匹配,最新在前;要看详情用 traffic_get,看原文用 traffic_blob):\n", len(rows), total)
			for _, r := range rows {
				line := fmt.Sprintf("  - %s %s %s status=%d", r.ExchangeID, r.Method, r.URL, r.Status)
				if r.Error != "" {
					line += " error=" + clipLine(r.Error, 60)
				}
				b.WriteString(line + "\n")
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// TrafficGetTool 按编号取一条流量的元数据(不带正文)。
func TrafficGetTool(store *rings.Store, blobs *BlobStore, taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "traffic_get",
		Description: toolDescTrafficGet,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "流量编号(traffic_search 结果里的 exchange_id)"},
			},
			"required": []string{"id"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			exID := strings.TrimSpace(args.ID)
			if exID == "" {
				return tool.Errorf("id 必填:traffic_search 结果里的流量编号"), nil
			}
			rec, ex, at, err := MirrorExchange(ctx, store, blobs, taskID, exID)
			if err != nil {
				return tool.Errorf("取流量记录失败: " + err.Error()), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "id=%s type=%s\n", exID, rec.Type)
			if rec.Method != "" || rec.URL != "" {
				fmt.Fprintf(&b, "%s %s status=%d\n", rec.Method, rec.URL, rec.Status)
			}
			fmt.Fprintf(&b, "time=%s elapsed_ms=%d\n", at.Local().Format("2006-01-02 15:04:05"), rec.ElapsedMS)
			if rec.Error != "" {
				fmt.Fprintf(&b, "error=%s\n", rec.Error)
			}
			if ex != nil && ex.Request != nil {
				fmt.Fprintf(&b, "request: body %d bytes", len(ex.Request.Body))
				if ex.Request.BodyTruncated {
					b.WriteString("(留痕时已截断)")
				}
				b.WriteString("\n")
			}
			if ex != nil && ex.Response != nil {
				fmt.Fprintf(&b, "response: status=%d body %d bytes", ex.Response.Status, len(ex.Response.Body))
				if ex.Response.BodyTruncated {
					b.WriteString("(留痕时已截断)")
				}
				b.WriteString(" headers:")
				for _, h := range ex.Response.Headers {
					fmt.Fprintf(&b, " %s=%s", h.Name, clipLine(h.Value, 60))
				}
				b.WriteString("\n")
			} else {
				b.WriteString("response: (无——隧道或未完成请求)\n")
			}
			if raw, ok := rawTotalOf(ex); ok {
				fmt.Fprintf(&b, "raw_total=%d(原始报文总长,取原文用 traffic_blob 按 offset 分段)\n", raw)
			} else {
				b.WriteString("raw_total=0(该记录无原文——隧道仅元数据留痕)\n")
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// TrafficBlobTool 按编号取原始报文分段(Burp 风格原文,与面板流量页
// 同一装配——展示与操作同源)。
func TrafficBlobTool(store *rings.Store, blobs *BlobStore, taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "traffic_blob",
		Description: toolDescTrafficBlob,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "流量编号"},
				"offset": map[string]any{"type": "integer", "description": "起始字节(默认 0)"},
				"limit":  map[string]any{"type": "integer", "description": "本段长度(默认 16384,上限 65536)"},
				"side":   map[string]any{"type": "string", "description": "req=仅请求包 / resp=仅返回包(默认全量)"},
			},
			"required": []string{"id"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				ID     string `json:"id"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
				Side   string `json:"side"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			exID := strings.TrimSpace(args.ID)
			if exID == "" {
				return tool.Errorf("id 必填:traffic_search 结果里的流量编号"), nil
			}
			side := strings.TrimSpace(args.Side)
			if side != "" && side != "req" && side != "resp" {
				return tool.Errorf("side 须为 req 或 resp(可空=全量)"), nil
			}
			_, ex, _, err := MirrorExchange(ctx, store, blobs, taskID, exID)
			if err != nil {
				return tool.Errorf("取流量原文失败: " + err.Error()), nil
			}
			if ex == nil || ex.Request == nil {
				return tool.Text("该记录无原文(隧道仅元数据留痕,或原文缺失)。"), nil
			}
			var full string
			switch side {
			case "":
				full, _ = mirror.RenderExchange(ex, 0)
			case "req", "resp":
				full = mirror.RenderExchangeSide(ex, side)
			}
			offset, limit := args.Offset, args.Limit
			if offset < 0 {
				return tool.Errorf("offset 须为非负整数"), nil
			}
			if limit <= 0 {
				limit = trafficBlobSegDef
			}
			if limit > trafficBlobSegMax {
				limit = trafficBlobSegMax
			}
			if offset > len(full) {
				return tool.Errorf(fmt.Sprintf("offset %d 超出原文长度 %d", offset, len(full))), nil
			}
			end := offset + limit
			if end > len(full) {
				end = len(full)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "--- %s 原文(offset=%d, 本段 %d 字节, 总长 %d, side=%s) ---\n",
				exID, offset, end-offset, len(full), side)
			b.WriteString(full[offset:end])
			if end < len(full) {
				fmt.Fprintf(&b, "\n[未完: 继续用 offset=%d 取下一段]", end)
			}
			return tool.Text(b.String()), nil
		},
	})
}

// rawTotalOf 原始报文总长(无原文形态返回 false)。
func rawTotalOf(ex *mirror.Exchange) (int, bool) {
	if ex == nil || ex.Request == nil {
		return 0, false
	}
	full, _ := mirror.RenderExchange(ex, 0)
	return len(full), true
}

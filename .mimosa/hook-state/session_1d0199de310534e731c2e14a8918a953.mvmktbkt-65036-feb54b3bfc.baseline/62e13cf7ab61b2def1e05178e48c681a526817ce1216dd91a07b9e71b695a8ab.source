// Package server 的诊断包导出与日志级别配置(P3-13b ④):一键打包
// 最近日志 + 版本 + 平台 + 配置摘要(脱敏)为 zip,可按任务筛选或
// 全选(勾选任务的账本摘要进包)——朋友点一下发给造物主即可远程排查。
// 日志级别可配(排查期开 DEBUG):POST 保存即生效(运行日志设施读取
// 同键)。
//
//	GET  /api/diagnostics?tasks=t1,t2   诊断包(缺省 tasks = 全部任务)
//	POST /api/config/log-level          日志级别写(debug|info|warn|error)
package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"ghostreaper/internal/buildinfo"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/vault"
)

// handleLogLevelGet 当前日志级别(GET /api/config/log-level;设置页
// 诊断包卡的级别下拉数据源,与运行值同源)。
func (s *Server) handleLogLevelGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"level": runlog.Current().String()})
}

// handleLogLevelSet 日志级别写(POST /api/config/log-level):写设置键
// 持久(重启后仍生效)并即时调整运行级别。
func (s *Server) handleLogLevelSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Level)) {
	case "debug", "info", "warn", "warning", "error":
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("级别须为 debug、info、warn 或 error"))
		return
	}
	lv := runlog.ParseLevel(req.Level)
	if err := s.Store.SetSetting(r.Context(), runlog.SettingKeyLogLevel, strings.ToLower(lv.String()), "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	runlog.SetLevel(lv)
	runlog.Infof("日志级别已调整为 %s", lv.String())
	writeJSON(w, http.StatusOK, map[string]string{"level": lv.String()})
}

// handleDiagnosticsExport 诊断包导出(GET /api/diagnostics):zip 流
// (Content-Disposition 文件名规约,前端 downloadExport 直接落盘)。
// 包结构:诊断说明 / 配置摘要 / logs/(运行日志全量,保留上限即上限)
// / tasks/<任务>.txt(勾选任务或全部任务的账本摘要)。
func (s *Server) handleDiagnosticsExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// 任务范围:tasks 逗号清单;缺省 = 全部(全选模式)。墓碑任务不进包。
	rows, err := s.Store.Tasks(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	byID := map[string]rings.TaskRow{}
	for _, t := range rows {
		if !t.Deleted {
			byID[t.ID] = t
		}
	}
	selected := byID
	if raw := strings.TrimSpace(r.URL.Query().Get("tasks")); raw != "" {
		selected = map[string]rings.TaskRow{}
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if t, ok := byID[id]; ok {
				selected[id] = t
			}
		}
	}

	explicit := strings.TrimSpace(r.URL.Query().Get("tasks")) != ""
	name := fmt.Sprintf("ghostreaper-diagnostics-%s.zip", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	zw := zip.NewWriter(w)
	defer zw.Close()

	// 1) 诊断说明(版本 / 平台 / 范围)。
	scope := "全部任务"
	if explicit {
		ids := make([]string, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		scope = fmt.Sprintf("勾选任务 %d 个(%s)", len(ids), strings.Join(ids, ", "))
	}
	writeZipText(zw, "诊断说明.txt", fmt.Sprintf(
		"GhostReaper 诊断包\n版本: %s\n平台: %s/%s\nGo: %s\n生成时间: %s\n范围: %s\n\n"+
			"内容:logs/ 为运行日志;tasks/ 为各任务的账本摘要(节点计数、token 合计、近期系统事件);\n"+
			"配置摘要.txt 为启动配置与生效参数(敏感值已打码)。\n",
		buildinfo.Version, runtime.GOOS, runtime.GOARCH, runtime.Version(),
		time.Now().Format("2006-01-02 15:04:05"), scope))

	// 2) 配置摘要(脱敏:key 只呈现打码形态,凭据只呈现名称)。
	writeZipText(zw, "配置摘要.txt", s.diagnosticsConfigSummary(ctx))

	// 3) 运行日志(logs/ 目录全量拷入;未开日志时如实说明)。
	if dir := runlog.Dir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
					continue
				}
				body, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				writeZipBytes(zw, "logs/"+e.Name(), body)
			}
		}
	} else {
		writeZipText(zw, "logs/说明.txt", "运行日志未开启(本进程启动时日志目录不可用)。\n")
	}

	// 4) 任务账本摘要(勾选或全部)。
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if selected[ids[i]].Pinned != selected[ids[j]].Pinned {
			return selected[ids[i]].Pinned
		}
		return selected[ids[i]].CreatedRev < selected[ids[j]].CreatedRev
	})
	for _, id := range ids {
		writeZipText(zw, "tasks/"+id+".txt", s.diagnosticsTaskSummary(ctx, id, selected[id]))
	}
	runlog.Infof("诊断包导出: %s(%s)", name, scope)
}

// diagnosticsConfigSummary 配置摘要(全部脱敏:模型 key 与代理打码、
// 凭据只记名称与用户名、搜索 key 打码——与设置页呈现同源口径)。
func (s *Server) diagnosticsConfigSummary(ctx context.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "生成时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "== 系统 ==\n监听: %s\n账本: %s\n任务目录: %s\n日志目录: %s\n日志级别: %s\n",
		s.Config.System.Addr, s.Config.System.DBPath, s.Config.System.TasksDir,
		orDash(runlog.Dir()), runlog.Current().String())

	budget := s.effectiveBudget(ctx)
	// 0 = 不限(P4-01 默认),摘要按操作者语言呈现。
	turnsLimit, secsLimit := "不限", "不限"
	if budget.MaxTurns > 0 {
		turnsLimit = strconv.Itoa(budget.MaxTurns)
	}
	if budget.RunSeconds > 0 {
		secsLimit = strconv.Itoa(budget.RunSeconds) + " 秒"
	}
	fmt.Fprintf(&b, "\n== 执行预算 ==\n轮次上限: %s\n时长上限: %s\n收尾轮次: %d\n",
		turnsLimit, secsLimit, budget.SettleTurns)
	params := s.effectiveAgentParams(ctx)
	fmt.Fprintf(&b, "\n== Agent 参数 ==\n虫群并发: %d\n重锚定: %d 步\n心跳间隔: %d 秒\n",
		params.Units.Value, params.Reanchor.Value, params.Heartbeat.Value)

	search := s.effectiveSearch(ctx)
	fmt.Fprintf(&b, "\n== 搜索 ==\n后端: %s\nbrave key: %s\ntavily key: %s\n代理: %s\n",
		search.Backend, orDash(search.BraveKeyMask), orDash(search.TavilyKeyMask), orDash(search.ProxyMask))

	if cfgs, err := failover.ListModelConfigs(ctx, s.Store); err == nil {
		b.WriteString("\n== 模型库 ==\n")
		primary, _, _ := failover.ActiveName(ctx, s.Store, "primary")
		backup, _, _ := failover.ActiveName(ctx, s.Store, "backup")
		fmt.Fprintf(&b, "主用: %s\n备用: %s\n", orDash(primary), orDash(backup))
		for _, c := range cfgs {
			fmt.Fprintf(&b, "- %s [%s] %s %s key=%s 窗口=%d 代理=%s 优先级=%d%s\n",
				c.Name, c.Format, c.Model, c.BaseURL, orDash(failover.MaskKey(c.APIKey)),
				c.ContextWindow, orDash(failover.MaskProxy(c.Proxy)), c.PollPriority,
				boolTag(c.PollDisabled, " 不参与轮询"))
		}
	} else {
		fmt.Fprintf(&b, "\n== 模型库 ==\n读取失败: %v\n", err)
	}

	if rules, err := swarm.ListHardRules(ctx, s.Store); err == nil {
		enabled := 0
		for _, rule := range rules {
			if rule.Enabled {
				enabled++
			}
		}
		fmt.Fprintf(&b, "\n== 拦截规则 ==\n共 %d 条(启用 %d 条)\n", len(rules), enabled)
	}

	if views, err := vault.Views(ctx, s.Store); err == nil {
		b.WriteString("\n== 目标凭据 ==\n")
		if len(views) == 0 {
			b.WriteString("(无)\n")
		}
		for _, v := range views {
			fmt.Fprintf(&b, "- %s(%s)%s\n", v.Name, orDash(v.Username), boolTag(!v.HasSecret, " 未存秘密"))
		}
	}
	return b.String()
}

// diagnosticsTaskSummary 单任务账本摘要:节点计数 / 单位数 / token
// 合计 / 近期系统事件(黑匣子与生命周期;不含流量正文与工具明细——
// 红线同运行日志)。
func (s *Server) diagnosticsTaskSummary(ctx context.Context, taskID string, row rings.TaskRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "任务: %s (%s)\n状态: %s\n", row.ID, orDash(row.Title), row.Status)

	countByType := func(nt rings.NodeType) (int, map[string]int) {
		nodes, err := s.Store.NodesByType(ctx, taskID, nt, "")
		if err != nil {
			return 0, nil
		}
		byStatus := map[string]int{}
		for _, n := range nodes {
			byStatus[n.Status]++
		}
		return len(nodes), byStatus
	}
	fmt.Fprintf(&b, "\n== 节点计数 ==\n")
	for _, nt := range []rings.NodeType{rings.NodeGoal, rings.NodeStep, rings.NodeFact, rings.NodeTestClaim, rings.NodeHypothesis, rings.NodeAsset} {
		n, byStatus := countByType(nt)
		fmt.Fprintf(&b, "%s: %d", nt, n)
		if len(byStatus) > 0 {
			statuses := make([]string, 0, len(byStatus))
			for st, c := range byStatus {
				statuses = append(statuses, fmt.Sprintf("%s=%d", st, c))
			}
			sort.Strings(statuses)
			fmt.Fprintf(&b, " (%s)", strings.Join(statuses, " "))
		}
		b.WriteString("\n")
	}

	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err == nil {
		units := map[string]bool{}
		for _, ev := range evs {
			if ev.Kind != rings.EventSwarmRecord {
				continue
			}
			var rec rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.SwarmRecSessionStarted {
				units[rec.UnitID+"\x00"+rec.TranscriptID] = true
			}
		}
		fmt.Fprintf(&b, "\n== 虫群 ==\n单位会话: %d 个\n", len(units))
	}

	if rows, err := s.Store.TokenUsageRows(ctx, taskID); err == nil {
		var in, out, cache int
		for _, x := range rows {
			in += x.InputTokens
			out += x.OutputTokens
			cache += x.CacheReadTokens + x.CacheWriteTokens
		}
		fmt.Fprintf(&b, "\n== Token ==\n输入: %d\n输出: %d\n缓存: %d\n调用: %d 次\n", in, out, cache, len(rows))
	}

	// 近期系统事件(尾部 30 条:黑匣子 + 生命周期;只记类型与摘要行)。
	if evs != nil {
		type line struct{ at, text string }
		var lines []line
		for _, ev := range evs {
			switch ev.Kind {
			case rings.EventBlackboxRecord:
				var rec rings.BlackboxRecord
				if json.Unmarshal(ev.Payload, &rec) != nil {
					continue
				}
				note := ""
				for _, k := range []string{"note", "reason"} {
					if v, ok := rec.Detail[k].(string); ok && v != "" {
						note = v
						break
					}
				}
				lines = append(lines, line{ev.OccurredAt.Local().Format("01-02 15:04:05"),
					fmt.Sprintf("黑匣子 %s %s %s %s", rec.Type, rec.StepID, rec.UnitID, note)})
			case rings.EventTaskLifecycle:
				var p rings.TaskLifecycle
				if json.Unmarshal(ev.Payload, &p) != nil {
					continue
				}
				reason := ""
				if v, ok := p.Detail["reason"].(string); ok {
					reason = v
				}
				lines = append(lines, line{ev.OccurredAt.Local().Format("01-02 15:04:05"),
					fmt.Sprintf("生命周期 %s %s", p.Action, reason)})
			}
		}
		fmt.Fprintf(&b, "\n== 近期系统事件(尾 %d 条) ==\n", 30)
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		for _, l := range lines {
			fmt.Fprintf(&b, "%s %s\n", l.at, l.text)
		}
	}
	return b.String()
}

// ---- zip 小件 ----

func writeZipText(zw *zip.Writer, name, body string) {
	writeZipBytes(zw, name, []byte(body))
}

func writeZipBytes(zw *zip.Writer, name string, body []byte) {
	fw, err := zw.Create(name)
	if err != nil {
		return
	}
	_, _ = fw.Write(body)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func boolTag(cond bool, tag string) string {
	if cond {
		return tag
	}
	return ""
}

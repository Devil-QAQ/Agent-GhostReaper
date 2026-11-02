// Package server 的诊断包导出(P3-13b ④ + P4-32 增强):一键打包
// 日志 + 版本 + 平台 + 配置摘要(脱敏)为 zip,可按任务筛选或全选
// (勾选任务的账本摘要进包)——朋友点一下发给造物主即可远程排查。
// P4-32:内容可勾选(运行日志默认勾,账本库快照 key 表脱敏、会话
// 转录按需加勾)、日志可按类型过滤(按日志行既有类型字样,只影响
// 导出、写入侧不动)、文件名带范围标注;日志级别设置已删(固定
// 全量,磁盘由滚动上限控制)。
//
//	GET  /api/diagnostics?tasks=t1,t2&kinds=error,blocked&content=logs,db,transcripts
//	     tasks   勾选任务(缺省 = 全部任务)
//	     kinds   日志类型过滤(缺省 = 全部日志)
//	     content 导出内容(缺省 = logs;可加 db、transcripts)
package server

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
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
	"ghostreaper/internal/secrets"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/vault"
)

// logKind 日志类型(P4-32 ②):按日志行既有类型字样在导出时过滤,
// 不动写入侧。id 进 URL 参数与文件名标注;word 为行内匹配词
// (任一命中即属该型)。
var logKinds = []struct {
	id    string
	label string
	words []string
}{
	{"error", "错误", []string{"[ERROR]"}},
	{"blocked", "受阻与恢复", []string{"受阻", "恢复", failover.LogMsgBreakerTripped}},
	{"approval", "审批与拦截", []string{"审批", "拦截", "暂挂"}},
	{"units", "单位起落", []string{"单位起飞", "单位干完"}},
}

// logKindByID 类型查找;空 id 返回 nil。
func logKindByID(id string) *struct {
	id    string
	label string
	words []string
} {
	for i := range logKinds {
		if logKinds[i].id == id {
			return &logKinds[i]
		}
	}
	return nil
}

// parseLogKinds 解析 kinds 参数为类型清单(保持既定序);空串返回
// nil = 不过滤(全部日志);未知 id 返回错误。
func parseLogKinds(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if logKindByID(id) == nil {
			return nil, fmt.Errorf("未知日志类型 %q", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// logLineMatches 行是否命中任一所选类型(类型字样子串匹配)。
func logLineMatches(line string, kinds []string) bool {
	for _, id := range kinds {
		if k := logKindByID(id); k != nil {
			for _, w := range k.words {
				if strings.Contains(line, w) {
					return true
				}
			}
		}
	}
	return false
}

// kindLabels 类型 id 清单的界面词(文件名标注与诊断说明用)。
func kindLabels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if k := logKindByID(id); k != nil {
			out = append(out, k.label)
		}
	}
	return out
}

// handleDiagnosticsExport 诊断包导出(GET /api/diagnostics):zip 流
// (Content-Disposition 文件名规约,前端 downloadExport 直接落盘)。
// 包结构:诊断说明 / 配置摘要 / logs/(运行日志,可按类型过滤)/
// tasks/<任务>.txt(勾选任务或全部任务的账本摘要)/ db/(可选,
// 账本库快照,key 表脱敏)/ transcripts/<任务>/(可选,已勾任务的
// 会话转录)。
func (s *Server) handleDiagnosticsExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kinds, err := parseLogKinds(r.URL.Query().Get("kinds"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 导出内容:缺省 = 仅运行日志(P4-32 ③ 默认最小包)。
	withLogs, withDB, withTranscripts := true, false, false
	if raw := strings.TrimSpace(r.URL.Query().Get("content")); raw != "" {
		withLogs, withDB, withTranscripts = false, false, false
		for _, part := range strings.Split(raw, ",") {
			switch strings.TrimSpace(part) {
			case "logs":
				withLogs = true
			case "db":
				withDB = true
			case "transcripts":
				withTranscripts = true
			}
		}
	}

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
	explicit := false
	if raw := strings.TrimSpace(r.URL.Query().Get("tasks")); raw != "" {
		explicit = true
		selected = map[string]rings.TaskRow{}
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if t, ok := byID[id]; ok {
				selected[id] = t
			}
		}
	}

	// 文件名:时间戳 + 日志类型范围标注(P4-32 ②)。
	name := fmt.Sprintf("ghostreaper-diagnostics-%s", time.Now().Format("20060102-150405"))
	if len(kinds) > 0 {
		name += "-" + strings.Join(kindLabels(kinds), "+")
	}
	name += ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	zw := zip.NewWriter(w)
	defer zw.Close()

	// 1) 诊断说明(版本 / 平台 / 范围 / 内容清单)。
	scope := "全部任务"
	if explicit {
		ids := make([]string, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		scope = fmt.Sprintf("勾选任务 %d 个(%s)", len(ids), strings.Join(ids, ", "))
	}
	logScope := "全部日志"
	if len(kinds) > 0 {
		logScope = strings.Join(kindLabels(kinds), "+")
	}
	contents := "诊断说明.txt 与 配置摘要.txt"
	if withLogs {
		contents += ";logs/ 为运行日志(" + logScope + ")"
	}
	contents += ";tasks/ 为各任务的账本摘要(节点计数、token 合计、近期系统事件)"
	if withDB {
		contents += ";db/ 为账本库快照(键值表敏感值已脱敏)"
	}
	if withTranscripts {
		contents += ";transcripts/ 为已勾任务的会话转录(JSONL 原文)"
	}
	writeZipText(zw, "诊断说明.txt", fmt.Sprintf(
		"GhostReaper 诊断包\n版本: %s\n平台: %s/%s\nGo: %s\n生成时间: %s\n范围: %s\n日志类型: %s\n\n"+
			"内容:%s。\n"+
			"配置摘要.txt 为启动配置与生效参数(敏感值已打码)。\n",
		buildinfo.Version, runtime.GOOS, runtime.GOARCH, runtime.Version(),
		time.Now().Format("2006-01-02 15:04:05"), scope, logScope, contents))

	// 2) 配置摘要(脱敏:key 只呈现打码形态,凭据只呈现名称)。
	writeZipText(zw, "配置摘要.txt", s.diagnosticsConfigSummary(ctx))

	// 3) 运行日志(logs/ 目录全量拷入;按类型过滤时仅命中行进包,
	//    零命中的档不进;未开日志时如实说明)。
	if withLogs {
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
					if len(kinds) > 0 {
						filtered := filterLogLines(string(body), kinds)
						if filtered == "" {
							continue
						}
						writeZipText(zw, "logs/"+e.Name(), filtered)
						continue
					}
					writeZipBytes(zw, "logs/"+e.Name(), body)
				}
			}
		} else {
			writeZipText(zw, "logs/说明.txt", "运行日志未开启(本进程启动时日志目录不可用)。\n")
		}
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

	// 5) 账本库快照(可选;一致性快照 + 键值表敏感值脱敏)。
	if withDB {
		if path, err := s.diagnosticsDBSnapshot(ctx); err != nil {
			writeZipText(zw, "db/说明.txt", fmt.Sprintf("账本库快照导出失败: %v\n", err))
		} else if body, err := os.ReadFile(path); err != nil {
			writeZipText(zw, "db/说明.txt", fmt.Sprintf("账本库快照读取失败: %v\n", err))
		} else {
			writeZipBytes(zw, "db/"+filepath.Base(s.Config.System.DBPath), body)
		}
	}

	// 6) 会话转录(可选;已勾任务的 transcripts/ 目录整体进包)。
	if withTranscripts && s.RootDir != "" {
		for _, id := range ids {
			src := filepath.Join(s.RootDir, id, "transcripts")
			_ = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, rerr := filepath.Rel(src, path)
				if rerr != nil {
					return nil
				}
				if body, berr := os.ReadFile(path); berr == nil {
					writeZipBytes(zw, "transcripts/"+id+"/"+filepath.ToSlash(rel), body)
				}
				return nil
			})
		}
	}
	runlog.Infof("诊断包导出: %s(%s;日志 %s;内容 logs=%v db=%v transcripts=%v)",
		name, scope, logScope, withLogs, withDB, withTranscripts)
}

// filterLogLines 日志文本按类型过滤,只留命中行(P4-32 ②;写入侧
// 不动——过滤只发生在导出打包时)。
func filterLogLines(body string, kinds []string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if logLineMatches(line, kinds) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// diagnosticsDBSnapshot 账本库一致性快照(VACUUM INTO)并就地脱敏:
// 键值表敏感值(model.library.* / search.* / cred.* / mirror.ca.key)
// 一律替换为占位——包内库可查结构与数据,拿不到任何密钥(P4-32 ③)。
func (s *Server) diagnosticsDBSnapshot(ctx context.Context) (string, error) {
	tmp, err := os.MkdirTemp("", "gr-diag-db-*")
	if err != nil {
		return "", fmt.Errorf("建临时目录: %w", err)
	}
	path := filepath.Join(tmp, "snapshot.db")
	if err := s.Store.SnapshotTo(ctx, path); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("开快照: %w", err)
	}
	defer db.Close()
	rrows, err := db.QueryContext(ctx, `SELECT key FROM settings`)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("读键值表: %w", err)
	}
	var keys []string
	for rrows.Next() {
		var k string
		if rrows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	rrows.Close()
	for _, k := range keys {
		// 敏感键(secrets 清单)之外,登录凭据行(auth.password 散列与
		// auth.token.* 会话记录)也一并脱敏——诊断包会外发,登录材料
		// 不随包走(P4-32 ③)。
		if !secrets.SensitiveSetting(k) && !strings.HasPrefix(k, "auth.") {
			continue
		}
		if _, err := db.ExecContext(ctx, `UPDATE settings SET value='(脱敏)' WHERE key=?`, k); err != nil {
			_ = os.RemoveAll(tmp)
			return "", fmt.Errorf("脱敏 %s: %w", k, err)
		}
	}
	return path, nil
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

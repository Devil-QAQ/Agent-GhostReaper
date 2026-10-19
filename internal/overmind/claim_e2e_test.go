package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-02 完成标准 ----
// mock 靶站上漏洞走完整状态机(候选 → 竞争解释 → 反证(多轮)→
// Confirmed / 待确认 → 重开再证);两维声明分开、严重级别跟影响面证据走
//(发现者无级别通道);LLM 陈述作为证据被仲裁者拒收;反证过程流量经镜
// 留痕,收割定稿的复现步骤可回放(Burp 可验证)。

// claimStates 取节点状态迁移历史(node.added 的初始状态 + 逐次 state_changed)。
func claimStates(t *testing.T, store *rings.Store, task, nodeID string) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var states []string
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.NodeID == nodeID {
				states = append(states, p.Status)
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.NodeID == nodeID {
				states = append(states, p.Status)
			}
		}
	}
	return states
}

// testclaimRecords 取任务内全部 testclaim.record(按类型过滤,空为全部)。
func testclaimRecords(t *testing.T, store *rings.Store, task, typ string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 testclaim.record: %v", err)
		}
		if typ == "" || r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

// claimByTitle 按标题找 TestClaim 节点。
func claimByTitle(t *testing.T, store *rings.Store, task, substr string) *rings.NodeFull {
	t.Helper()
	claims, err := store.NodesByType(context.Background(), task, rings.NodeTestClaim, "")
	if err != nil {
		t.Fatalf("查声明失败: %v", err)
	}
	for i := range claims {
		if strings.Contains(claims[i].Title, substr) {
			return &claims[i]
		}
	}
	t.Fatalf("未找到标题含 %q 的声明: %+v", substr, claims)
	return nil
}

// TestClaimLifecycleE2E P2-02 完整负向认证 E2E(镜已装配,流量全程留痕):
//
//	备份目录声明(冻结两条竞争解释):
//	  候选 → testing(第 1 轮:排除"归属错误"、缓存污染未除)→
//	  awaiting_round(轮末机器评估:解释未全灭)→ testing(第 2 轮:缓存
//	  穿透重放排除"缓存污染")→ Confirmed(定级 high,收割定稿含复现步骤)
//	  → 重开(造物主反驳)→ testing(重开轮:全部解释复活并重新排除)→
//	  再 Confirmed(新裁决取代旧裁决,历史不删)。
//	版本暴露声明(单条解释,一轮即终态):
//	  候选 → testing(第 1 轮:解释已除但影响面证据不足;Note 里 LLM
//	  自称"影响面证据充分建议确认")→ 待确认(LLM 陈述被仲裁者拒收,R4
//	  机器搁置理由)。
func TestClaimLifecycleE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "claim-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 脚本模型分支:
	//   拆解(无工具):审计意图已派过才派,否则空列表;
	//   发现包(任务包含 web1 资产):robots → 备份目录 → 版本接口 →
	//     两条 submit_test_claim(各带显式竞争解释)→ 收口;
	//   反证包(任务包含"八、反证任务"):按声明分支 + 轮次分支——
	//     备份声明:第 1 轮只排除"归属错误"(alt2),第 2 轮缓存穿透重放
	//       排除"缓存污染"(alt1)→ 确认;重开第 3 轮两条全排 → 再确认;
	//     版本声明:第 1 轮排除唯一解释但影响面不成立(Note 自称充分,
	//       演示 LLM 陈述拒收)→ 待确认。
	claimIDRe := regexp.MustCompile(`声明 (tc-[0-9a-f]+)`)
	roundRe := regexp.MustCompile(`第 (\d+) 轮`)
	stmtBackupRe := regexp.MustCompile(`(?m)^\s*存在性声明: .*备份目录`)
	packURLRe := regexp.MustCompile(`-\s+web1 \[service\] (http://\S+)`)
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "审计主站敏感路径") {
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[{"intent":"审计主站敏感路径暴露(robots/备份目录/版本接口)","expected_observation":"敏感路径清单与各自内容证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
		}
		if strings.Contains(rr.LastUser, "八、反证任务") {
			m := claimIDRe.FindStringSubmatch(rr.LastUser)
			if m == nil {
				return scriptedReply{Text: "反证任务包缺声明 ID。", Finish: "stop"}
			}
			claimID := m[1]
			base := mainURL
			if u := packURLRe.FindStringSubmatch(rr.LastUser); u != nil {
				base = u[1]
			}
			rm := roundRe.FindStringSubmatch(rr.LastUser)
			round := 0
			if rm != nil {
				fmt.Sscanf(rm[1], "%d", &round)
			}
			if stmtBackupRe.MatchString(rr.LastUser) {
				switch round {
				case 1: // 第 1 轮:排除归属错误,缓存污染留活 → 待下轮。
					switch {
					case !seenToolResult(rr, "bash", "mock backup archive listing"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fb1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, base),
						}}, Finish: "tool_calls"}
					case !seenToolResult(rr, "submit_falsify_result", "已入账"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fr1", Name: "submit_falsify_result",
							Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":true,"impact_level":"high","alternatives":[{"id":"alt1","refuted":false},{"id":"alt2","refuted":true}],"evidence":"独立重放 /old-backup/ 仍返回备份列举(db-2026-09-01.sql.gz、config.bak),响应头无缓存命中标记,归属为本站;缓存来源未排除,须穿透复核","note":"归属已确认,缓存污染待下轮"}`, claimID),
						}}, Finish: "tool_calls"}
					default:
						return scriptedReply{Text: "第 1 轮反证完成:归属错误已排除,缓存污染待下轮。", Finish: "stop"}
					}
				case 2: // 第 2 轮:缓存穿透重放 → 全灭 → 确认。
					switch {
					case !seenToolResult(rr, "bash", "mock backup archive listing"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fb2", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/?nocache=880011"}`, base),
						}}, Finish: "tool_calls"}
					case !seenToolResult(rr, "submit_falsify_result", "已入账"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fr2", Name: "submit_falsify_result",
							Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":true,"impact_level":"high","alternatives":[{"id":"alt1","refuted":true}],"evidence":"带随机穿透参数重放 /old-backup/?nocache=880011 仍返回同一备份列举,内容来自源站而非缓存;匿名可读成立,影响面为敏感备份可被未授权下载","note":"缓存污染已排除"}`, claimID),
						}}, Finish: "tool_calls"}
					default:
						return scriptedReply{Text: "第 2 轮反证完成:全部解释已排除,结论已写回。", Finish: "stop"}
					}
				default: // 重开轮:全部解释复活,逐条重新排除 → 再确认。
					switch {
					case !seenToolResult(rr, "bash", "mock backup archive listing"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fb3", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/?recheck=1"}`, base),
						}}, Finish: "tool_calls"}
					case !seenToolResult(rr, "submit_falsify_result", "已入账"):
						return scriptedReply{ToolCalls: []scriptedToolCall{{
							ID: "fr3", Name: "submit_falsify_result",
							Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":true,"impact_level":"high","alternatives":[{"id":"alt1","refuted":true},{"id":"alt2","refuted":true}],"evidence":"重开复核:穿透参数重放与直连重放均返回同一备份列举,归属与缓存两项解释均排除;匿名可读仍成立","note":"重开轮复核一致"}`, claimID),
						}}, Finish: "tool_calls"}
					default:
						return scriptedReply{Text: "重开轮反证完成:全部解释重新排除,结论已写回。", Finish: "stop"}
					}
				}
			}
			// 版本声明:第 1 轮即终态——解释排除,影响面不成立。
			switch {
			case !seenToolResult(rr, "bash", `"version":"2.4.1"`):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "fv", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, base),
				}}, Finish: "tool_calls"}
			case !seenToolResult(rr, "submit_falsify_result", "已入账"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "frv", Name: "submit_falsify_result",
					Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":false,"alternatives":[{"id":"alt1","refuted":true}],"evidence":"独立重放 /api/version 确实返回 2.4.1,内容来自源站;但仅版本号本身,未取得可支撑定向利用的实际影响证据","note":"LLM 自评:影响面证据充分,建议直接确认"}`, claimID),
				}}, Finish: "tool_calls"}
			default:
				return scriptedReply{Text: "反证完成:存在性未推翻,影响面证据不足,结论已写回。", Finish: "stop"}
			}
		}
		// 发现任务分支。
		switch {
		case !seenToolResult(rr, "bash", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/robots.txt"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "mock backup archive listing"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d2", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", `"version":"2.4.1"`):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d3", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_test_claim", "备份目录可公开匿名列举"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d4", Name: "submit_test_claim",
				Args: fmt.Sprintf(`{"statement":"/old-backup/ 备份目录可公开匿名列举","impact":"备份文件 db-2026-09-01.sql.gz 与 config.bak 可被未授权下载,存在敏感数据泄露","evidence":"curl 实测 /old-backup/ 返回列举: mock backup archive listing: db-2026-09-01.sql.gz, config.bak","asset_id":"web1","vulnclass":"信息暴露","alternatives":["缓存污染:列举内容可能来自中间缓存而非目标本体","归属错误:列举可能来自其他资产或共用组件"]}`),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_test_claim", "公开暴露服务版本号"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d5", Name: "submit_test_claim",
				Args: fmt.Sprintf(`{"statement":"/api/version 公开暴露服务版本号","impact":"版本号可支撑针对已知漏洞的定向利用","evidence":"curl 实测 /api/version 返回 {\"service\":\"gr-mock-main\",\"version\":\"2.4.1\"}","asset_id":"web1","vulnclass":"信息暴露","alternatives":["缓存污染:版本响应可能来自中间缓存"]}`),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "审计完成:两条可证伪声明已提交,待主宰派独立单位反证。", Finish: "stop"}
		}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := dir
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir()) // P2-02:反证重放全程经镜留痕(复现步骤来源)

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}
	m, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, _ string, wake <-chan struct{}) { loop.Run(tctx, wake) },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(task)
		m.Close()
	})
	if _, err := m.CreateTask(ctx, task, "TestClaim 完整负向认证 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// 种子:无谓词目标(开放任务,LLM 拆解治理何时算完)+ 主站资产。
	if err := SeedGoal(ctx, store, task, "g1", "审计 mock 靶站主站暴露面", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1"}, map[string]string{"web1": mainURL})
	tk := m.Task(task)
	tk.Wake()

	// ---- 阶段一:两条声明走完各自状态机(备份 Confirmed / 版本待确认) ----
	waitFor(t, 60*time.Second, "两条声明都到首个终态", func() bool {
		backup := claimByTitleSoft(store, task, "备份目录")
		version := claimByTitleSoft(store, task, "版本号")
		return backup != nil && version != nil &&
			backup.Status == rings.TestClaimConfirmed &&
			version.Status == rings.TestClaimPending
	})
	backup := claimByTitle(t, store, task, "备份目录")
	version := claimByTitle(t, store, task, "版本号")

	// 状态机轨迹:备份 候选 → 反证中 → 待下轮 → 反证中 → Confirmed。
	if got := claimStates(t, store, task, backup.ID); len(got) != 5 ||
		got[0] != rings.TestClaimCandidate || got[1] != rings.TestClaimTesting ||
		got[2] != rings.TestClaimAwaitingRound || got[3] != rings.TestClaimTesting ||
		got[4] != rings.TestClaimConfirmed {
		t.Fatalf("备份声明完整状态机轨迹不符: %v", got)
	}
	// 版本:候选 → 反证中 → 待确认。
	if got := claimStates(t, store, task, version.ID); len(got) != 3 ||
		got[0] != rings.TestClaimCandidate || got[1] != rings.TestClaimTesting || got[2] != rings.TestClaimPending {
		t.Fatalf("版本声明状态机轨迹不符: %v", got)
	}

	// 冻结竞争解释:备份 2 条(alt1 缓存污染 / alt2 归属错误)、版本 1 条。
	if alts, _ := backup.Detail["alternatives"].([]any); len(alts) != 2 {
		t.Fatalf("备份声明冻结解释应恰 2 条: %+v", backup.Detail["alternatives"])
	}
	if alts, _ := version.Detail["alternatives"].([]any); len(alts) != 1 {
		t.Fatalf("版本声明冻结解释应恰 1 条: %+v", version.Detail["alternatives"])
	}

	// ---- 裁决链:轮末机器评估逐轮落账,决策语义正确 ----
	verdicts := testclaimRecords(t, store, task, rings.TestClaimRecVerdict)
	var backupVerdicts []rings.TestClaimRecord
	for _, v := range verdicts {
		if v.ClaimID == backup.ID {
			backupVerdicts = append(backupVerdicts, v)
		}
	}
	if len(backupVerdicts) != 2 {
		t.Fatalf("重开前备份声明应恰 2 条裁决(R1 advance + R2 confirm): %+v", backupVerdicts)
	}
	if backupVerdicts[0].Decision != rings.VerdictAdvanceRound || backupVerdicts[0].Satisfied {
		t.Fatalf("R1 裁决应为待下轮反证: %+v", backupVerdicts[0])
	}
	if !strings.Contains(backupVerdicts[0].PendingReason, "缓存污染未排除") &&
		!strings.Contains(backupVerdicts[0].PendingReason, "alt1") &&
		!strings.Contains(backupVerdicts[0].PendingReason, "1 条竞争性替代解释未排除") {
		t.Fatalf("advance 裁决理由应言明未排除解释: %q", backupVerdicts[0].PendingReason)
	}
	if backupVerdicts[1].Decision != rings.VerdictConfirm || !backupVerdicts[1].Satisfied {
		t.Fatalf("R2 裁决应为确认: %+v", backupVerdicts[1])
	}
	for _, c := range backupVerdicts[1].Conditions {
		if !c.Satisfied {
			t.Fatalf("Confirmed 裁决条件须逐条满足: %+v", c)
		}
	}

	// LLM 陈述拒收:版本声明的反证 Note 自称"影响面证据充分,建议直接
	// 确认",结构化结论却 impact_supported=false → 仲裁者判待确认,
	// 理由言明影响面(文本不进求值,契约④)。
	var versionVerdict *rings.TestClaimRecord
	for i := range verdicts {
		if verdicts[i].ClaimID == version.ID {
			versionVerdict = &verdicts[i]
		}
	}
	if versionVerdict == nil || versionVerdict.Satisfied ||
		!strings.Contains(versionVerdict.PendingReason, "影响面") {
		t.Fatalf("版本声明应判待确认且理由言明影响面(LLM 陈述被拒收): %+v", versionVerdict)
	}
	falsifies := testclaimRecords(t, store, task, rings.TestClaimRecFalsify)
	var versionNote string
	for _, f := range falsifies {
		if f.ClaimID == version.ID {
			versionNote = f.Note
		}
	}
	if !strings.Contains(versionNote, "建议直接确认") {
		t.Fatalf("版本反证 Note 应携带 LLM 自评文本(拒收演示的对照组): %q", versionNote)
	}

	// ---- 两维声明与定级:级别只跟影响面证据走 ----
	// 发现者提交侧无级别字段(工具 schema 即无);确认后的收割定稿
	// Severity 必须等于末轮反证影响面定级 high。
	repros := testclaimRecords(t, store, task, rings.TestClaimRecRepro)
	var backupRepro *rings.TestClaimRecord
	for i := range repros {
		if repros[i].ClaimID == backup.ID {
			backupRepro = &repros[i]
		}
	}
	if backupRepro == nil || backupRepro.Severity != "high" {
		t.Fatalf("备份声明收割定稿应携带 high 定级(来自末轮影响面证据): %+v", backupRepro)
	}
	if len(backupRepro.Steps) == 0 {
		t.Fatalf("收割定稿必须携带复现步骤")
	}
	for _, st := range backupRepro.Steps {
		if st.Op == "" || st.Method != "GET" || !strings.Contains(st.URL, "/old-backup/") ||
			st.Status != 200 || st.BlobRef == "" || st.UnitID == "" {
			t.Fatalf("复现步骤须含操作方法+HTTP 交换摘要+blob 引用: %+v", st)
		}
		// blob 可回放:Burp 可验证数据包原文(镜留痕)。
		doc, err := ex.Blobs.Read(st.BlobRef)
		if err != nil {
			t.Fatalf("复现步骤 blob 不可回放: %v", err)
		}
		var exch mirror.Exchange
		if err := json.Unmarshal(doc, &exch); err != nil || exch.Request == nil ||
			!strings.Contains(exch.Request.URL, "/old-backup/") {
			t.Fatalf("复现步骤 blob 不是该声明的镜交换原文: %v %+v", err, exch.Request)
		}
	}
	// 复现步骤 = 当前战役(第 1+2 轮)反证单位的真实重放。
	if backupRepro.Steps[0].Round < 1 {
		t.Fatalf("复现步骤应携带轮次: %+v", backupRepro.Steps[0])
	}

	// ---- R12:每一轮验证者 ≠ 发现者 ----
	discoverer, _ := backup.Detail["unit_id"].(string)
	if discoverer == "" {
		t.Fatalf("备份声明缺发现单位记录")
	}
	backupRounds := 0
	for _, f := range falsifies {
		if f.ClaimID != backup.ID {
			continue
		}
		backupRounds++
		if f.UnitID == "" || f.UnitID == discoverer {
			t.Fatalf("R12 违例:声明 %s 第 %d 轮验证者 %s 与发现者 %s", backup.ID, f.Round, f.UnitID, discoverer)
		}
	}
	if backupRounds != 2 {
		t.Fatalf("重开前备份声明应恰 2 轮反证写回: %d", backupRounds)
	}

	// ---- 真实性:靶站被多轮真实命中(发现 + 两轮反证),非账本自说自话 ----
	if got := target.Hits("main", "/old-backup/"); got < 3 {
		t.Fatalf("/old-backup/ 应被发现与两轮反证真实命中,实际 %d", got)
	}
	if got := target.Hits("main", "/api/version"); got < 2 {
		t.Fatalf("/api/version 应被发现与反证两轮真实命中,实际 %d", got)
	}

	// ---- 阶段二:重开(结论可反驳)→ 重开轮再证 ----
	if err := ReopenClaim(ctx, store, task, backup.ID, rings.ReopenManual,
		"靶站疑似调整缓存策略,备份目录结论须复核", "creator"); err != nil {
		t.Fatalf("重开备份声明失败: %v", err)
	}
	tk.Wake() // 重开是外部触发:唤醒主宰跑重开轮
	waitFor(t, 60*time.Second, "重开轮走完再确认", func() bool {
		b := claimByTitleSoft(store, task, "备份目录")
		return b != nil && b.Status == rings.TestClaimConfirmed
	})

	// 完整轨迹:候选 → testing → awaiting → testing → confirmed →
	// testing(重开轮)→ confirmed(新裁决取代旧裁决)。
	if got := claimStates(t, store, task, backup.ID); len(got) != 7 || got[5] != rings.TestClaimTesting ||
		got[6] != rings.TestClaimConfirmed {
		t.Fatalf("重开后完整状态机轨迹不符: %v", got)
	}
	// 重开记录:触发词 + 理由落账,历史不删(旧裁决仍在)。
	reopens := testclaimRecords(t, store, task, rings.TestClaimRecReopen)
	if len(reopens) != 1 || reopens[0].Trigger != rings.ReopenManual ||
		!strings.Contains(reopens[0].Reason, "复核") {
		t.Fatalf("重开记录不符: %+v", reopens)
	}
	verdicts2 := testclaimRecords(t, store, task, rings.TestClaimRecVerdict)
	backupVerdicts2 := 0
	for _, v := range verdicts2 {
		if v.ClaimID == backup.ID {
			backupVerdicts2++
		}
	}
	if backupVerdicts2 != 3 {
		t.Fatalf("重开后再确认应共 3 条裁决(advance/confirm/confirm): %d", backupVerdicts2)
	}
	// 新收割定稿:重开战役(仅第 3 轮)的复现步骤 + 同源定级。
	repros2 := testclaimRecords(t, store, task, rings.TestClaimRecRepro)
	var latestRepro *rings.TestClaimRecord
	for i := range repros2 {
		if repros2[i].ClaimID == backup.ID {
			latestRepro = &repros2[i]
		}
	}
	if latestRepro == nil || latestRepro.Severity != "high" || len(latestRepro.Steps) == 0 {
		t.Fatalf("重开后再确认应有新收割定稿: %+v", latestRepro)
	}
	for _, st := range latestRepro.Steps {
		if st.Round != 3 { // 战役边界:重开后只收重开轮交换
			t.Fatalf("重开后定稿只应收重开战役交换: %+v", st)
		}
	}
	// 旧定稿记录仍在事件流(取代语义:历史不删,R1)。
	if len(repros2) != 2 {
		t.Fatalf("新旧两份定稿应并存于账本: %d", len(repros2))
	}

	// ---- 静默:终态后主宰收尾回静默,拆解计数稳定不再增长 ----
	last, stable := -1, 0
	waitFor(t, 30*time.Second, "终态后主宰回静默(拆解计数稳定)", func() bool {
		n := len(model.decomposeRequests())
		if n == last {
			stable++
		} else {
			last, stable = n, 0
		}
		return stable >= 20 // ≈300ms 无新拆解
	})
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// claimByTitleSoft claimByTitle 的非致命版(未找到返回 nil,轮询用)。
func claimByTitleSoft(store *rings.Store, task, substr string) *rings.NodeFull {
	claims, err := store.NodesByType(context.Background(), task, rings.NodeTestClaim, "")
	if err != nil {
		return nil
	}
	for i := range claims {
		if strings.Contains(claims[i].Title, substr) {
			return &claims[i]
		}
	}
	return nil
}

// TestHarvestClaimsIntegrity 收割侧完整性门槛:无冻结标准/无发现单位的
// 声明不进反证(留原因);完备声明派第 1 轮反证(绑定轮次与冻结解释,
// 声明迁移 testing);待下轮声明派下一轮(轮次续号、只带存活解释);
// 反证 Step 继承发现步骤的意图分级。
func TestHarvestClaimsIntegrity(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "claims-integrity"

	// 铺垫:资产 + 发现步骤(L2 主动测试)+ 完备声明(两条冻结解释)、
	// 无标准声明、待下轮声明(带一轮历史:alt1 已排除)。
	altList := []any{
		map[string]any{"id": "alt1", "label": "缓存污染"},
		map[string]any{"id": "alt2", "label": "归属错误"},
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "主动验证", Status: "done",
			Detail: map[string]any{"goal_id": "g1", "action_level": "主动测试"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-ok", NodeType: rings.NodeTestClaim, Title: "完备声明", Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-ok", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-ok", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-nostd", NodeType: rings.NodeTestClaim, Title: "无标准声明", Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1"}}},
		// 待下轮声明:第 1 轮已排除 alt1,alt2 存活。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-next", NodeType: rings.NodeTestClaim, Title: "待下轮声明", Status: rings.TestClaimAwaitingRound,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-next", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-next", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-next", "round": 1}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-next", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecFalsify, Round: 1,
			Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}}, EvidenceHead: "第一轮"}},
	); err != nil {
		t.Fatalf("铺垫失败: %v", err)
	}

	res, err := HarvestClaims(ctx, store, task)
	if err != nil {
		t.Fatalf("收割声明失败: %v", err)
	}
	if len(res.Dispatched) != 2 {
		t.Fatalf("应恰派发完备候选 + 待下轮声明: %+v", res.Dispatched)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].FactID != "tc-nostd" {
		t.Fatalf("无标准声明应被跳过留原因: %+v", res.Skipped)
	}

	// tc-ok:s3 反证 Step(铺垫已占 s1/s2),继承 L2 分级,round=1,冻结解释全量。
	step, err := store.Node(ctx, task, "s3")
	if err != nil {
		t.Fatalf("查反证步骤失败: %v", err)
	}
	if step.Detail["testclaim_id"] != "tc-ok" || step.Detail["kind"] != "falsify" {
		t.Fatalf("反证步骤绑定不符: %+v", step.Detail)
	}
	if step.Detail["round"] != float64(1) {
		t.Fatalf("首轮反证步骤应带 round=1: %+v", step.Detail)
	}
	if alts, _ := step.Detail["alternatives"].([]any); len(alts) != 2 {
		t.Fatalf("首轮反证应携带全量冻结解释: %+v", step.Detail["alternatives"])
	}
	if step.Detail["action_level"] != "主动测试" {
		t.Fatalf("反证步骤应继承发现步骤意图分级: %+v", step.Detail)
	}
	if got := claimStates(t, store, task, "tc-ok"); len(got) != 2 || got[1] != rings.TestClaimTesting {
		t.Fatalf("完备声明应迁移 testing: %v", got)
	}
	// covers 继承资产锚点。
	covers, _ := store.Edges(ctx, task, rings.EdgeCovers)
	found := false
	for _, e := range covers {
		if e.From == "s3" && e.To == "web1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("反证步骤应 covers 声明锚定资产: %+v", covers)
	}

	// tc-next:s4 第 2 轮,只带存活解释(alt2),轮次续号。
	step3, err := store.Node(ctx, task, "s4")
	if err != nil {
		t.Fatalf("查第 2 轮反证步骤失败: %v", err)
	}
	if step3.Detail["testclaim_id"] != "tc-next" || step3.Detail["round"] != float64(2) {
		t.Fatalf("第 2 轮反证步骤绑定/轮次不符: %+v", step3.Detail)
	}
	if alts, _ := step3.Detail["alternatives"].([]any); len(alts) != 1 {
		t.Fatalf("第 2 轮应只带存活解释: %+v", step3.Detail["alternatives"])
	} else if m, _ := alts[0].(map[string]any); m["id"] != "alt2" {
		t.Fatalf("存活解释应为 alt2: %+v", alts[0])
	}
	if got := claimStates(t, store, task, "tc-next"); got[len(got)-1] != rings.TestClaimTesting {
		t.Fatalf("待下轮声明应回 testing: %v", got)
	}

	// 幂等:已 testing 的声明不再派发。
	res2, err := HarvestClaims(ctx, store, task)
	if err != nil || len(res2.Dispatched) != 0 {
		t.Fatalf("二次收割应零派发: %v %+v", err, res2.Dispatched)
	}
}

// TestReopenClaimGuards 重开入口守卫:非终态/幽灵声明/空理由拒绝;
// 合法重开落 reopen 记录 + 反证步骤 + testing 迁移一批事务。
func TestReopenClaimGuards(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "reopen-guards"

	altList := []any{map[string]any{"id": "alt1", "label": "缓存污染"}}
	mustAppendReopen := func(events ...rings.Event) {
		t.Helper()
		if _, err := store.Append(ctx, task, events...); err != nil {
			t.Fatalf("铺垫失败: %v", err)
		}
	}
	mustAppendReopen(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "发现", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-c", NodeType: rings.NodeTestClaim, Title: "已确认声明", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-c", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-c", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-t", NodeType: rings.NodeTestClaim, Title: "反证中声明", Status: rings.TestClaimTesting,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-t", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-t", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf1", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-c", "round": 1}}},
	)

	// 反证中声明不可重开;幽灵声明拒绝;空理由拒绝。
	if err := ReopenClaim(ctx, store, task, "tc-t", rings.ReopenManual, "理由", "creator"); err == nil {
		t.Fatalf("反证中声明不可重开")
	}
	if err := ReopenClaim(ctx, store, task, "tc-404", rings.ReopenManual, "理由", "creator"); err == nil {
		t.Fatalf("幽灵声明不可重开")
	}
	if err := ReopenClaim(ctx, store, task, "tc-c", rings.ReopenManual, "  ", "creator"); err == nil {
		t.Fatalf("空理由不可重开")
	}

	// 合法重开:轮次续号(历史已有 round=1 → 新轮 round=2),解释全量
	// 复活,reopen 记录与状态迁移落账。
	if err := ReopenClaim(ctx, store, task, "tc-c", rings.ReopenExpired, "结论时效过期", "creator"); err != nil {
		t.Fatalf("合法重开失败: %v", err)
	}
	if got := claimStates(t, store, task, "tc-c"); len(got) != 2 || got[1] != rings.TestClaimTesting {
		t.Fatalf("重开应迁移 testing: %v", got)
	}
	sf2, err := store.Node(ctx, task, "s2")
	if err != nil {
		t.Fatalf("查重开反证步骤失败: %v", err)
	}
	if sf2.Detail["testclaim_id"] != "tc-c" || sf2.Detail["round"] != float64(2) {
		t.Fatalf("重开轮步骤应续号 round=2: %+v", sf2.Detail)
	}
	if alts, _ := sf2.Detail["alternatives"].([]any); len(alts) != 1 {
		t.Fatalf("重开轮应携带全量冻结解释: %+v", sf2.Detail["alternatives"])
	}
	reopens := testclaimRecords(t, store, task, rings.TestClaimRecReopen)
	if len(reopens) != 1 || reopens[0].Trigger != rings.ReopenExpired || reopens[0].Reason != "结论时效过期" {
		t.Fatalf("重开记录不符: %+v", reopens)
	}
}

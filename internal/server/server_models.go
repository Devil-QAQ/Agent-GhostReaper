// Package server 的模型库面板 API(P2-06e 起,第七章 §5「LLM 模型库
// 与主备」+「故障与恢复语义」恢复两层;P2-08c 升 v2:五段式配置全量
// 存取、测试连接、从 API 拉模型列表、主 + 备链逐成员状态、立即恢复)。
// 设置页存多个模型配置、激活主用、Key 永不回显明文(GET 打码);配置
// 写后链重载并自动重开 blocked 的 Step;任务列表头部「恢复全部」为
// 人工兜底。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
)

// ---- 读视图 ----

// modelsView GET /api/models 的回复:模型库(打码)+ 激活项 + 备链成员
// 状态(逐成员熔断)+ 环境变量兜底。key 明文永不出现(第七章 §5)。
type modelsView struct {
	Library []failover.MaskedConfig `json:"library"`
	Active  struct {
		Primary string `json:"primary"`          // 空 = 未激活(回退环境变量)
		Backup  string `json:"backup,omitempty"` // 06e 遗留字段(v2 备链由轮询配置定义)
	} `json:"active"`
	Env struct {
		Configured bool   `json:"configured"`
		Model      string `json:"model,omitempty"`
		Format     string `json:"format,omitempty"`
	} `json:"env"`
	// Members 备链成员快照(主用在前;「主备轮询」抽屉数据源)。
	Members []memberView `json:"members"`
	// Presets 平台 × 格式默认端点(P4-03 ④:建模型选平台后预填,可改)。
	Presets map[string]map[string]string `json:"presets"`
	// OpenCodeModes opencode 接入方式双形态端点(P4-10 ①:Go 订阅默认 /
	// API 按量;presets 内的 opencode 即默认形态 Go 订阅)。
	OpenCodeModes map[string]map[string]string `json:"opencode_modes"`
	// Platforms 平台下拉清单(含"自定义")。
	Platforms []failover.PlatformOption `json:"platforms"`
	// Breaker 主用腿单熔断口径(06e 兼容)。
	Breaker struct {
		Active    string `json:"active"` // primary|backup|none(按熔断状态推导)
		State     string `json:"state"`  // closed|open|probing
		Reason    string `json:"reason"`
		OpenUntil string `json:"open_until,omitempty"`
		UpdatedAt string `json:"updated_at"`
	} `json:"breaker"`
	ChainReady bool `json:"chain_ready"`
}

// memberView 一个链成员的面板视图(状态词翻译在面板侧)。
type memberView struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Priority  int    `json:"priority"` // 0=主用(恒第 1)
	State     string `json:"state"`    // closed|open|probing
	OpenUntil string `json:"open_until,omitempty"`
	Opens     int    `json:"opens"`
	Reason    string `json:"reason,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// maskedLibrary 库清单的服务端视图(api_key 打码、代理去凭据,经
// ModelConfig.Masked)。
func (s *Server) maskedLibrary(ctx context.Context) ([]failover.MaskedConfig, error) {
	cfgs, err := failover.ListModelConfigs(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	out := make([]failover.MaskedConfig, 0, len(cfgs))
	for _, c := range cfgs {
		v := c.Masked()
		if e, _, _ := s.Store.Setting(ctx, "model.library."+c.Name); !e.UpdatedAt.IsZero() {
			v.UpdatedBy, v.UpdatedAt = e.UpdatedBy, e.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		}
		out = append(out, v)
	}
	return out, nil
}

// handleModelsGet 模型库读 API(LLM 配置页):打码库清单 + 激活项 +
// 备链成员状态 + 环境变量兜底(持久化键值表,面板可见)。
func (s *Server) handleModelsGet(w http.ResponseWriter, r *http.Request) {
	v := modelsView{ChainReady: s.Chain != nil && s.Chain.Ready(),
		Presets: failover.PlatformPresetURLs(), Platforms: failover.PlatformOptions(),
		OpenCodeModes: failover.OpenCodeModePresetURLs()}
	lib, err := s.maskedLibrary(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	v.Library = lib
	if p, ok, _ := failover.ActiveName(r.Context(), s.Store, failover.RolePrimary); ok {
		v.Active.Primary = p
	}
	if b, ok, _ := failover.ActiveName(r.Context(), s.Store, failover.RoleBackup); ok {
		v.Active.Backup = b
	}
	v.Env.Configured = s.Config.LLM.Configured
	v.Env.Model = s.Config.LLM.Model
	v.Env.Format = s.Config.LLM.Format
	if s.Chain != nil {
		for _, m := range s.Chain.Members() {
			mv := memberView{Name: m.Name, Model: m.Model, Priority: m.Priority,
				State: m.State, Opens: m.Opens, Reason: m.Reason}
			if !m.OpenUntil.IsZero() {
				mv.OpenUntil = m.OpenUntil.Local().Format("2006-01-02 15:04:05")
			}
			if !m.UpdatedAt.IsZero() {
				mv.UpdatedAt = m.UpdatedAt.Local().Format("2006-01-02 15:04:05")
			}
			v.Members = append(v.Members, mv)
		}
		st := s.Chain.State()
		v.Breaker.State = st.State
		v.Breaker.Reason = st.Reason
		if st.State == "open" {
			v.Breaker.Active = "backup"
			if st.Backup == "" {
				v.Breaker.Active = "none"
			}
			v.Breaker.OpenUntil = st.OpenUntil.Local().Format("2006-01-02 15:04:05")
		} else {
			v.Breaker.Active = "primary"
		}
		if !st.UpdatedAt.IsZero() {
			v.Breaker.UpdatedAt = st.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- 写路径(全部经键值表单写者;写后链重载 + 自动重开) ----

// modelBody POST /api/models 的写体。api_key 为空且为编辑时保留旧 key
// (面板不回显明文,编辑无需重填);新建必填。P2-08c 起含五段式全量
// 字段(基础/限速/轮询/输出与思考/重试覆盖)。Create 表新增意图
// (P3-05c ② 重名拦截:true 且库内已有同名配置即拒"该名称已存在";
// 编辑自身 Create=false 不受影响)。
type modelBody struct {
	Name           string  `json:"name"`
	Create         bool    `json:"create"`
	Format         string  `json:"format"`
	BaseURL        string  `json:"base_url"`
	APIKey         string  `json:"api_key"`
	Model          string  `json:"model"`
	ContextWindow  int     `json:"context_window"`
	Proxy          string  `json:"proxy"`
	RatePerSecond  float64 `json:"rate_per_second"`
	RatePerMinute  float64 `json:"rate_per_minute"`
	PollPriority   int     `json:"poll_priority"`
	PollDisabled   bool    `json:"poll_disabled"`
	MaxTokens      int     `json:"max_tokens"`
	MaxTokensField string  `json:"max_tokens_field"`
	StreamDisabled bool    `json:"stream_disabled"`
	Thinking       string  `json:"thinking"`
	ThinkingEffort string  `json:"thinking_effort"`
	RetryConnect   int     `json:"retry_connect"`
	RetryEmpty     int     `json:"retry_empty"`
	RetryReplay    int     `json:"retry_replay"`
	RetryBreaker   int     `json:"retry_breaker"`
	RetryRerun     int     `json:"retry_rerun"`
}

func (b modelBody) toConfig() failover.ModelConfig {
	return failover.ModelConfig{
		Name: b.Name, Format: b.Format, BaseURL: b.BaseURL,
		APIKey: strings.TrimSpace(b.APIKey), Model: b.Model,
		ContextWindow: b.ContextWindow,
		Proxy:         strings.TrimSpace(b.Proxy),
		RatePerSecond: b.RatePerSecond, RatePerMinute: b.RatePerMinute,
		PollPriority: b.PollPriority, PollDisabled: b.PollDisabled,
		MaxTokens: b.MaxTokens, MaxTokensField: b.MaxTokensField,
		StreamDisabled: b.StreamDisabled,
		Thinking:       b.Thinking, ThinkingEffort: b.ThinkingEffort,
		RetryConnect: b.RetryConnect, RetryEmpty: b.RetryEmpty,
		RetryReplay: b.RetryReplay, RetryBreaker: b.RetryBreaker,
		RetryRerun: b.RetryRerun,
	}
}

// handleModelSave 保存(新增/覆盖)一条模型配置(五段式全量字段)。
// 新增意图(Create=true)且重名即拒(编辑自身不受影响);响应只回
// 打码视图,不回显明文 key。
func (s *Server) handleModelSave(w http.ResponseWriter, r *http.Request) {
	var body modelBody
	if !readJSON(w, r, &body) {
		return
	}
	cfg := body.toConfig()
	if body.Create {
		if _, exists, err := failover.ModelConfigByName(r.Context(), s.Store,
			strings.TrimSpace(cfg.Name)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		} else if exists {
			writeErr(w, http.StatusConflict, errors.New("该名称已存在"))
			return
		}
	}
	if err := failover.SaveModelConfig(r.Context(), s.Store, cfg, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reloadChain(r.Context())
	s.reopenBlockedSteps(r.Context(), "failover", "模型配置保存", "")
	lib, err := s.maskedLibrary(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"library": lib})
}

// handleModelDelete 删除一条模型配置(被激活引用时激活项一并清除)。
func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := failover.DeleteModelConfig(r.Context(), s.Store, name, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reloadChain(r.Context())
	s.reopenBlockedSteps(r.Context(), "failover", "模型配置删除", "")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}

// handleModelActivate 激活主用(空名清除;P2-08c 起备链由各配置的轮询
// 段定义,backup 字段仅 06e 兼容)。写后链重载(新配置全新探测)+
// 按原因自动重开 blocked 的 Step(第七章恢复两层之自动层)。
func (s *Server) handleModelActivate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Primary string `json:"primary"`
		Backup  string `json:"backup"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Primary) == "" && strings.TrimSpace(body.Backup) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("未提交任何激活项(主用 / 备用至少一项)"))
		return
	}
	if body.Primary == body.Backup && body.Primary != "" {
		writeErr(w, http.StatusBadRequest, errors.New("主用与备用不能是同一个模型"))
		return
	}
	if err := failover.SetActive(r.Context(), s.Store, failover.RolePrimary, body.Primary, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := failover.SetActive(r.Context(), s.Store, failover.RoleBackup, body.Backup, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.reloadChain(r.Context())
	s.reopenBlockedSteps(r.Context(), "failover", "模型配置切换", "")
	v := modelsView{ChainReady: s.Chain != nil && s.Chain.Ready()}
	if p, ok, _ := failover.ActiveName(r.Context(), s.Store, failover.RolePrimary); ok {
		v.Active.Primary = p
	}
	if b, ok, _ := failover.ActiveName(r.Context(), s.Store, failover.RoleBackup); ok {
		v.Active.Backup = b
	}
	writeJSON(w, http.StatusOK, v.Active)
}

// reloadChain 装配了链时重载(模型库变更消费);未装配(未配 LLM 的
// 面板)跳过——库照常可写,链在下次装配时消费。P3-05b ⑤:重载后链变
// 就绪时触发装配层钩子(模型激活免重启——拉起镜/主宰循环/虫群执行器);
// 链未就绪(清除激活/只剩坏配置)不触发,既有装配保持。
func (s *Server) reloadChain(ctx context.Context) {
	if s.Chain == nil {
		return
	}
	s.Chain.Reload(ctx)
	if s.Chain.Ready() && s.RuntimeAssemble != nil {
		s.RuntimeAssemble()
	}
}

// ---- 恢复两层:自动重开 + 恢复全部(P2-06e) ----

// reopenBlockedSteps 把非终态任务里 blocked 的 Step 重开回 dispatched
// 并做执行侧拉起(P3-05c ① 恢复闭环;第七章「自动重开」;人工兜底
// 「恢复全部」与单任务恢复同一条路径):终态(stopped/completed)与
// 收尾中(settling)任务不重开——无人执行的死状态;墓碑任务不重开
// (列表不呈现,历史在账本)。重开后由主宰调度重新领取,R14 复检图
// 版本(过期计划照旧拒绝)。执行侧拉起分两档:任务在跑(started/
// settling/queued/created)唤醒即够(循环活着,排队任务唤醒落缓冲,
// 拉起后消费);任务已暂停(paused,如无模型启动被收养)须 Resume
// 拉起循环——否则重开的步骤躺在图上没人领取。onlyTask 非空时只处理
// 该任务(单任务恢复)。返回重开的任务数与步骤数。
func (s *Server) reopenBlockedSteps(ctx context.Context, actor, reason string, onlyTask string) (int, int) {
	tasks, err := s.Store.Tasks(ctx)
	if err != nil {
		return 0, 0
	}
	taskCnt, stepCnt := 0, 0
	for _, t := range tasks {
		if t.Deleted || (onlyTask != "" && t.ID != onlyTask) {
			continue
		}
		switch t.Status {
		case "stopped", "completed", "settling":
			continue // 终态不重开;收尾中任务正走向终态
		}
		steps, err := s.Store.NodesByType(ctx, t.ID, rings.NodeStep, "blocked")
		if err != nil || len(steps) == 0 {
			continue
		}
		events := make([]rings.Event, 0, len(steps))
		for _, st := range steps {
			events = append(events, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: actor,
				Payload: rings.NodeStateChanged{NodeID: st.ID, Status: "dispatched"},
			})
		}
		if _, err := s.Store.Append(ctx, t.ID, events...); err != nil {
			continue // 单任务失败不拖垮其余(如实跳过)
		}
		taskCnt++
		stepCnt += len(steps)
		s.wake(t.ID) // 重新事件唤醒(排队任务唤醒落缓冲,拉起后消费)
		// 执行侧拉起(P3-05c ①):暂停任务没有活循环,唤醒落在无人消费
		// 的通道里——Resume 拉起循环,拉起即消费缓冲唤醒、领取步骤。
		if mt := s.Manager.Task(t.ID); mt != nil && mt.Status() == "paused" {
			if err := s.Manager.Resume(t.ID); err != nil {
				// 拉起失败如实跳过(步骤已重开,任务仍是暂停,可再试);
				// 账本侧无副作用需要回滚(Resume 自身记账,失败即未拉起)。
				continue
			}
		}
	}
	return taskCnt, stepCnt
}

// handleRecoverAll 任务列表头部「恢复全部」(人工兜底):一键重开全部
// 受阻任务的 blocked 步骤并唤醒(重新事件唤醒 + R14 复检)。
func (s *Server) handleRecoverAll(w http.ResponseWriter, r *http.Request) {
	tasks, steps := s.reopenBlockedSteps(r.Context(), "creator", "恢复全部(面板)", "")
	writeJSON(w, http.StatusOK, map[string]int{"tasks": tasks, "steps": steps})
}

// handleTaskRecover 工作台单任务恢复(P3-05c ⑥ 恢复按钮双入口):重开
// 该任务受阻的步骤并执行侧拉起;任务无受阻步骤时如实报 0(按钮只在
// 存在受阻步骤时可用,此处兜住直调)。
func (s *Server) handleTaskRecover(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	tasks, steps := s.reopenBlockedSteps(r.Context(), "creator", "恢复(工作台)", taskID)
	writeJSON(w, http.StatusOK, map[string]int{"tasks": tasks, "steps": steps})
}

// ---- 任务列表受阻徽标(P2-06e) ----

// blockedBriefOf 任务受阻原因:优先黑匣子 task_blocked 的原因(任务级
// 阈值触发时记),退化为图上 blocked 步骤的 session_finished 终因。
// blocked=true 的判定本身由"图上存在 blocked 步骤"派生(持久真值,
// 与进程内计数无关)。
func (s *Server) blockedBriefOf(ctx context.Context, taskID string) (bool, string) {
	steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "blocked")
	if err != nil || len(steps) == 0 {
		return false, ""
	}
	reason := ""
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err == nil {
		for _, ev := range evs {
			switch ev.Kind {
			case rings.EventBlackboxRecord:
				var rec rings.BlackboxRecord
				if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.BlackboxTaskBlocked {
					if r, ok := rec.Detail["reason"].(string); ok && r != "" {
						reason = r
					}
				}
			case rings.EventSwarmRecord:
				var rec rings.SwarmRecord
				if reason == "" && json.Unmarshal(ev.Payload, &rec) == nil &&
					rec.Type == rings.SwarmRecSessionEnd && strings.Contains(rec.Note, "status=blocked") {
					if i := strings.Index(rec.Note, "reason="); i >= 0 {
						reason = rec.Note[i+len("reason="):]
						if j := strings.Index(reason, " turns="); j >= 0 {
							reason = reason[:j]
						}
					}
				}
			}
		}
	}
	if reason == "" {
		reason = fmt.Sprintf("模型故障,%d 个步骤受阻", len(steps))
	}
	return true, reason
}

// ---- P2-08c:测试连接 / 从 API 拉模型列表 / 立即恢复 ----

// draftBody 测试与拉列表的请求体:name 命中库内配置时以其为底(草稿
// key 为空沿用库内 key);config 为编辑中未保存的草稿(五段式字段)。
type draftBody struct {
	Name   string     `json:"name"`
	Config *modelBody `json:"config"`
	Prompt string     `json:"prompt"`
}

// resolveDraft 解析待测配置:库内底 + 草稿覆盖(未填 key 沿用库内)。
func (s *Server) resolveDraft(ctx context.Context, b draftBody) (failover.ModelConfig, error) {
	if b.Config != nil {
		cfg := b.Config.toConfig()
		if cfg.Name == "" {
			cfg.Name = strings.TrimSpace(b.Name)
		}
		if cfg.APIKey == "" && cfg.Name != "" {
			if old, ok, _ := failover.ModelConfigByName(ctx, s.Store, cfg.Name); ok {
				cfg.APIKey = old.APIKey
			}
		}
		return cfg, nil
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return failover.ModelConfig{}, errors.New("未提交配置(name 或 config 至少一项)")
	}
	cfg, ok, err := failover.ModelConfigByName(ctx, s.Store, name)
	if err != nil {
		return failover.ModelConfig{}, err
	}
	if !ok {
		return failover.ModelConfig{}, fmt.Errorf("模型 %q 不在库内", name)
	}
	return cfg, nil
}

// handleModelTest 测试连接(第七章 §5:看得见模型说话才算通):按配置
// 实际的思考/上限/代理参数发一次真请求,展示回复原文与用量。key 只进
// 请求,不进响应与日志。
func (s *Server) handleModelTest(w http.ResponseWriter, r *http.Request) {
	var body draftBody
	if !readJSON(w, r, &body) {
		return
	}
	cfg, err := s.resolveDraft(r.Context(), body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := failover.ValidateEndpoint(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(cfg.Model) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("模型名不能为空(可先从 API 加载列表)"))
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = "用一句话回复,证明你收到了这条消息。"
	}
	prov, err := failover.BuildProvider(cfg, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req := llm.CompletionRequest{
		System:    []string{"你是连通性测试对象,如实作答。"},
		Messages:  []llm.Message{llm.UserText(prompt)},
		MaxTokens: 512,
	}
	if cfg.MaxTokens > 0 {
		req.MaxTokens = cfg.MaxTokens
	}
	cctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	msg, stop, usage, err := prov.Complete(cctx, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("测试失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "model": cfg.Model, "format": cfg.Format,
		"latency_ms":  time.Since(start).Milliseconds(),
		"stop_reason": stop,
		"usage": map[string]int{
			"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
			"cache_read_tokens": usage.CacheReadTokens, "cache_write_tokens": usage.CacheWriteTokens,
		},
		"text": msg.Text(), "thinking": thinkingText(msg),
	})
}

// thinkingText 思考块原文合并(有则呈现,无则空)。
func thinkingText(msg llm.Message) string {
	var b strings.Builder
	for _, blk := range msg.Content {
		if blk.Type == llm.BlockThinking && blk.Thinking != "" {
			b.WriteString(blk.Thinking)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// handleModelFetchList 从 API 加载可用模型列表(基础段):草稿或库内
// 配置皆可;代理照配置走(仅 LLM 出站)。
func (s *Server) handleModelFetchList(w http.ResponseWriter, r *http.Request) {
	var body draftBody
	if !readJSON(w, r, &body) {
		return
	}
	cfg, err := s.resolveDraft(r.Context(), body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := failover.ValidateEndpoint(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ids, err := failover.FetchModelList(r.Context(), cfg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ids, "count": len(ids)})
}

// handleModelRecover 立即恢复一个链成员:熔断关闭、梯度归零(操作者
// 裁决);同配置恢复路径自动重开受阻步骤(第七章恢复两层)。
func (s *Server) handleModelRecover(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.Chain == nil || !s.Chain.ResetBreaker(name) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("模型 %q 不在链内(或链未装配)", name))
		return
	}
	tasks, steps := s.reopenBlockedSteps(r.Context(), "creator", "立即恢复 "+name, "")
	writeJSON(w, http.StatusOK, map[string]any{"recovered": name, "tasks": tasks, "steps": steps})
}

// ---- P3-05c ②:已存 key 明文查看(受控端点 + 审计) ----

// keyViewAudit key 查看审计的持久化形状(设置键 model.keyview.<name> 的
// 值;键值表自带 by/updated_at 轻量审计,此处再记累计次数与末次时刻)。
// 审计只记动作不记 key 内容(第七章 §5 Key 安全:key 不写日志/黑匣子/
// transcript/报告)。
type keyViewAudit struct {
	Count  int    `json:"count"`
	LastAt string `json:"last_at"`
}

// handleModelRevealKey 已存 key 明文查看(第七章 §5:显式查看走受控
// 端点,查看动作记审计):按名称取配置原文返回 api_key,同时在设置表
// 记一笔查看审计(次数累计;不含 key 内容)。明文只出现在本响应里,
// GET /api/models 等读路径仍恒打码。
func (s *Server) handleModelRevealKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	cfg, ok, err := failover.ModelConfigByName(r.Context(), s.Store, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("模型 %q 不在库内", name))
		return
	}
	// 查看审计(尽力而为:审计写失败不挡查看,错误如实透出)。
	if auditErr := s.recordKeyView(r.Context(), name); auditErr != nil {
		writeErr(w, http.StatusInternalServerError, auditErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "api_key": cfg.APIKey})
}

// recordKeyView 记一笔 key 查看审计:读旧值累加次数,写回设置键
// model.keyview.<name>(by=creator,键值表自带 updated_at)。
func (s *Server) recordKeyView(ctx context.Context, name string) error {
	key := "model.keyview." + name
	audit := keyViewAudit{}
	if e, ok, err := s.Store.Setting(ctx, key); err != nil {
		return fmt.Errorf("读 key 查看审计: %w", err)
	} else if ok {
		_ = json.Unmarshal([]byte(e.Value), &audit)
	}
	audit.Count++
	audit.LastAt = time.Now().Format("2006-01-02 15:04:05")
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("编码 key 查看审计: %w", err)
	}
	return s.Store.SetSetting(ctx, key, string(raw), "creator")
}

"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
import { InterjectBlock, InterjectWindow } from "@/components/interject-window";
import { LongText } from "@/components/long-text";
import { SessionInfoBar } from "@/components/session-info-bar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { denyStatusLabel, postJSON, usePolling } from "@/lib/api";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 会话域(8.3 任务空间·会话 + 8.5 Agent 会话页组件,P2-07 布局):
// 每任务自家四组——牧者(总对话入口)/ 主宰 / 虫群 / 系统事件。
// P2-08b:主宰组与虫群组升级为完整会话页——主宰每轮研判轨迹(五问/
// 收割/派发决策)+ 直连对话;虫群每单位执行轨迹(会话原文)+ 直连
// 对话(问 = 基于历史即时答,改 = 舵杆注入带回执)。四按钮(插话/
// 冬眠/唤醒/熔断)挂输入框右上;顶部信息条复用 P2-07 组件。牧者对话
// 页挂任务控制(冬眠/唤醒/熔断,P2-08)。
//
// 布局:左栏会话分组 + 右区当前会话(顶部信息条 → 对话流 → 输入框)。

const GROUPS = [
  { id: "shepherd", title: "牧者", icon: "messageCircle", desc: "总对话", enabled: true, phase: "" },
  { id: "overmind", title: "主宰", icon: "brain", desc: "研判轨迹与直连对话", enabled: true, phase: "" },
  { id: "swarm", title: "虫群", icon: "bug", desc: "单位轨迹与控制", enabled: true, phase: "" },
  { id: "events", title: "系统事件", icon: "activity", desc: "黑匣子", enabled: true, phase: "" },
];

// 黑匣子事件类型 → 操作者语言。
const BLACKBOX_LABELS = {
  budget_half: "预算过半",
  unit_exhausted: "单位预算耗尽",
  unit_budget_renew: "预算到线续跑",
  task_settling: "任务进入收尾",
  grace_hard_stop: "收尾宽限硬停",
  task_settled: "收尾定终态",
  heartbeat: "心跳巡检",
  hb_stall: "进度停滞标记",
  hb_repeat: "重复动作标记",
  hb_burn_anomaly: "预算消耗异常",
  unit_approval_deny: "审批拒绝熔断单位",
  unit_blocked: "单位受阻",
  task_blocked: "任务受阻",
  task_resumed: "任务恢复",
  restart_adoption: "进程重启收养",
};

// 收养动作 → 操作者语言(restart_adoption 事件的 detail.action,P4-06 ④
// 机器词不上界面;表外词原样)。
const ADOPT_ACTIONS = {
  keep: "保持原状",
  relaunch: "重新拉起",
  adopt_paused: "收养为已暂停",
  requeue: "重新排队",
};

const DELIVERY = {
  queued: { label: "已排队", variant: "paused" },
  delivered: { label: "已送达", variant: "running" },
  replied: { label: "已回复", variant: "success" },
  failed: { label: "失败", variant: "danger" },
};

export function SessionsPage({ currentTask, unitFocus }) {
  const [group, setGroup] = React.useState("shepherd");
  const [interject, setInterject] = React.useState(null); // 插话目标(主宰/单位)
  const task = currentTask;
  // 单位深链(P2-10 工作台执行流跳入):?unit=<会话ID> 直达虫群组对应
  // 单位会话页;ref 防同参重入。
  const jumped = React.useRef("");
  React.useEffect(() => {
    if (unitFocus && jumped.current !== unitFocus) {
      jumped.current = unitFocus;
      setGroup("swarm");
    }
  }, [unitFocus]);
  return (
    <div className="flex h-[calc(100vh-168px)] min-h-[480px] flex-col gap-3">
      <div className="flex items-baseline gap-3 border-b pb-2">
        <h1 className="text-base font-semibold tracking-tight">会话</h1>
        <span className="truncate text-xs text-muted-foreground">
          对话与执行轨迹按任务隔离
        </span>
      </div>
      <div className="grid min-h-0 flex-1 grid-cols-[176px_minmax(0,1fr)] items-stretch gap-3">
        {/* 左栏:会话分组(每任务自家四组) */}
        <nav className="flex min-h-0 flex-col gap-1 overflow-y-auto rounded-xl border bg-card p-2">
          {GROUPS.map((g) => {
            const Icon = iconOf(g.icon);
            const active = group === g.id;
            return (
              <button
                key={g.id}
                disabled={!g.enabled}
                className={cn(
                  "flex flex-col items-start gap-0.5 rounded-md px-3 py-2 text-left text-[13px] transition-colors",
                  g.enabled && "cursor-pointer",
                  active && "bg-primary/15 text-primary",
                  !active && g.enabled && "text-foreground hover:bg-hover-lift",
                  !g.enabled && "cursor-not-allowed text-muted-foreground/60"
                )}
                onClick={() => g.enabled && setGroup(g.id)}
              >
                <span className="flex items-center gap-2">
                  <Icon className="size-4 shrink-0" />
                  <span className="font-medium">{g.title}</span>
                </span>
                <span className="pl-6 text-xs text-muted-foreground">
                  {g.phase || g.desc}
                </span>
              </button>
            );
          })}
        </nav>
        {/* 右区:当前会话 */}
        {group === "events" ? (
          <SystemEventsPane taskID={task?.id} />
        ) : group === "overmind" ? (
          <OvermindPane task={task} onInterject={() => setInterject({ kind: "overmind" })} />
        ) : group === "swarm" ? (
          <SwarmPane task={task} unitFocus={unitFocus} onInterject={(t) => setInterject(t)} />
        ) : (
          <ShepherdPane task={task} />
        )}
      </div>
      <InterjectWindow
        open={Boolean(interject)}
        target={interject}
        task={task}
        onClose={() => setInterject(null)}
      />
    </div>
  );
}

// ---- 主宰会话页(P2-08b:每轮研判轨迹 + 直连对话) ----

// 循环状态徽标(服务端为准):冬眠中(循环随任务冻结)> 无在跑循环(任务
// 终态/未拉起)> 循环冬眠中 > 循环运行中。
function loopBadgeOf(st) {
  if (!st) return { label: "读取中…", variant: "info", frozen: true };
  if (st.hibernated)
    return { label: "任务冬眠中", variant: "paused", frozen: true };
  if (!st.running)
    return { label: "循环未运行", variant: "info", frozen: true };
  if (st.paused)
    return { label: "循环冬眠中", variant: "paused", frozen: false };
  return { label: "循环运行中", variant: "running", frozen: false };
}

// 五问的呈现标签(研判记录字段 → 操作者语言,按第四章五问原文)。
const FIVE_QUESTIONS = [
  ["q_access", "新身份能访问哪些已有资产"],
  ["q_privilege", "新权限是否改变其他漏洞的影响"],
  ["q_combination", "两条线索是否组合成新的边界问题"],
  ["q_unlock", "新的可达关系是否解锁受阻步骤"],
  ["q_overturn", "新证据是否推翻旧判断"],
];

function OvermindPane({ task, onInterject }) {
  const taskID = task?.id;
  const url = taskID ? `/api/tasks/${taskID}/overmind` : null;
  const { data, error } = usePolling(url || "/api/healthz", 2000, {
    immediate: Boolean(url),
  });
  const chatURL = taskID ? `/api/tasks/${taskID}/interject?target=overmind` : null;
  const { data: chat, setData: setChat } = usePolling(chatURL || "/api/healthz", 2000, {
    immediate: Boolean(chatURL),
  });
  const [node, setNode] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const bottomRef = React.useRef(null);

  const badge = loopBadgeOf(data);
  const paused = Boolean(data?.paused) && Boolean(data?.running) && !data?.hibernated;
  const frozen = Boolean(data?.hibernated);
  const rounds = data?.rounds || [];
  const messages = chat?.messages || [];
  const steers = chat?.steers || [];

  React.useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [rounds.length, messages.length, messages[messages.length - 1]?.reply]);

  const steerStateOf = (id) =>
    (steers.find((s) => s.steer_id === id) || {}).state || "queued";

  const ctl = async (action) => {
    if (!taskID) return;
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/overmind/${action}`, {});
    setBusy(false);
    if (!ok) {
      toast.error(body?.error || "操作失败");
      return;
    }
    toast.success(action === "pause" ? "主宰循环已冬眠" : "主宰循环已唤醒");
  };

  // 研判轨迹与直连对话按时间归并成一条流。
  const stream = [];
  rounds.forEach((r, i) => stream.push({ at: r.at, key: `r${i}`, kind: "round", round: r }));
  messages.forEach((m) => stream.push({ at: m.at, key: m.id, kind: "chat", msg: m }));
  stream.sort((a, b) => (a.at || "").localeCompare(b.at || ""));

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2">
      <SessionInfoBar
        info={data?.info}
        startedAt={rounds[0]?.at}
        deductSeconds={data?.hibernate_seconds}
        frozen={frozen}
      />
      {error && (
        <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-1 text-xs text-status-danger">
          读取失败:{error}
        </div>
      )}
      <div className="flex items-center gap-2 text-[13px]">
        {React.createElement(iconOf("brain"), { className: "size-4 text-primary" })}
        <SessionMarkBar
          taskID={taskID}
          sessionID="overmind"
          defaultName="主宰"
          meta={data?.meta}
          className="min-w-40"
        />
        <Badge variant={badge.variant}>{badge.label}</Badge>
        <span className="text-xs text-muted-foreground">
          每轮研判:五问 · 收割 · 派发决策;研判轮次之间可直连问话与提建议。
        </span>
      </div>
      {/* 流:研判轨迹 + 直连对话 */}
      <div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto rounded-xl border bg-card px-3 py-3">
        {stream.length === 0 && (
          <div className="py-10 text-center text-[13px] text-muted-foreground">
            尚无研判记录。主宰被唤醒研判后,每轮五问与派发在此呈现。
          </div>
        )}
        {stream.map((item) =>
          item.kind === "round" ? (
            <RoundCard key={item.key} round={item.round} taskID={taskID} onNode={setNode} />
          ) : (
            <InterjectBlock key={item.key} msg={item.msg} steerStateOf={steerStateOf} />
          )
        )}
        <div ref={bottomRef} />
      </div>
      {/* 直连输入:问(默认)/改(建议)+ 循环控制 + 插话窗 */}
      <DirectInput
        taskID={taskID}
        chat={chat}
        setChat={setChat}
        target={{ kind: "overmind" }}
        frozen={frozen}
        busy={busy}
        onInterject={onInterject}
        buttons={
          paused ? (
            <Button size="sm" variant="outline" disabled={busy || badge.frozen} onClick={() => ctl("resume")}>
              {React.createElement(iconOf("sun"), { className: "size-3.5" })}
              唤醒
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              disabled={busy || badge.frozen}
              title={badge.frozen ? (data?.hibernated ? "任务冬眠中,唤醒后操作" : "任务未在运行") : "主宰循环冬眠,本轮结束后挂起"}
              onClick={() => ctl("pause")}
            >
              {React.createElement(iconOf("moon"), { className: "size-3.5" })}
              冬眠
            </Button>
          )
        }
      />
      <NodeSheetHost tid={taskID} value={node} onChange={setNode} />
    </div>
  );
}

// RoundCard 一轮研判:五问结论(逐问一行,无 = 本轮该问无可研判项)+
// 五问外研判 + 派发决策(本轮窗口内新增步骤,状态取当前)+ 收割
// (事实转正 / 声明裁决 / 假设裁决)+ 会话原文下钻(态势快照与研判
// 回复的原文)。
function RoundCard({ round, taskID, onNode }) {
  const [openRaw, setOpenRaw] = React.useState(false);
  const hasHarvest =
    (round.facts_confirmed || []).length > 0 ||
    (round.claims_judged || []).length > 0 ||
    (round.hypotheses || []).length > 0;
  return (
    <div className="rounded-lg border bg-background px-3 py-3 text-[12px]">
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        <Badge variant="outline">研判</Badge>
        <span className="font-mono">{round.at?.slice(11)}</span>
        {round.dispatched > 0 && <span>派发 {round.dispatched} 条意图</span>}
        {(round.combined || []).length > 0 && <span>组合声明 {(round.combined || []).length} 条</span>}
        {round.session_id && (
          <button
            className="ml-auto cursor-pointer text-primary underline decoration-dotted underline-offset-2"
            onClick={() => setOpenRaw((o) => !o)}
          >
            {openRaw ? "收起原文" : "会话原文"}
          </button>
        )}
      </div>
      <div className="mt-2 space-y-1">
        {FIVE_QUESTIONS.map(([key, label]) => (
          <div key={key} className="flex gap-2">
            <span className="w-[190px] shrink-0 text-muted-foreground">{label}</span>
            <span className="min-w-0 [overflow-wrap:anywhere]">{round[key] || "无"}</span>
          </div>
        ))}
        {round.note && (
          <div className="flex gap-2">
            <span className="w-[190px] shrink-0 text-muted-foreground">五问之外</span>
            <span className="min-w-0 [overflow-wrap:anywhere]">{round.note}</span>
          </div>
        )}
      </div>
      {(round.steps || []).length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="text-muted-foreground">派发:</span>
          {(round.steps || []).map((s) => (
            <NodeChip key={s.id} id={s.id} label={s.title} status={s.status} taskID={taskID} onNode={onNode} />
          ))}
        </div>
      )}
      {hasHarvest && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="text-muted-foreground">收割:</span>
          {(round.facts_confirmed || []).map((f) => (
            <NodeChip key={f} id={f} label="事实转正" status="confirmed" taskID={taskID} onNode={onNode} />
          ))}
          {(round.claims_judged || []).map((c) => (
            <NodeChip key={c.id} id={c.id} label="声明裁决" status={c.status} taskID={taskID} onNode={onNode} />
          ))}
          {(round.hypotheses || []).map((h) => (
            <NodeChip key={h.id} id={h.id} label="假设" status={h.status} taskID={taskID} onNode={onNode} />
          ))}
        </div>
      )}
      {openRaw && round.session_id && (
        <div className="mt-2 border-t pt-2">
          <RawTranscript taskID={taskID} sessionID={round.session_id} />
        </div>
      )}
    </div>
  );
}

// NodeChip 研判轮内的图上节点(步骤/事实/声明/假设):点击开节点核对面
//(P4-06 ④ 并入全站节点抽屉宿主,与进度图/发现页同一呈现)。
function NodeChip({ id, label, status, taskID, onNode }) {
  const st = UNIT_STATUS[status] || CLAIM_STATUS_OF[status] || { label: status, variant: "info" };
  return (
    <button
      className="flex max-w-full items-center gap-1 rounded-md border bg-card px-2 py-0.5 cursor-pointer hover:bg-hover-lift"
      title={`${id}${label ? " · " + label : ""}`}
      onClick={async () => {
        const r = await fetch(`/api/tasks/${taskID}/nodes/${id}`);
        if (!r.ok) {
          toast.error("节点不在账本中");
          return;
        }
        const n = await r.json();
        onNode({ type: n.type, id: n.id });
      }}
    >
      <span className="truncate">
        <span className="font-mono">{id}</span>
        {label && <span className="ml-1 text-muted-foreground">{clip(label, 18)}</span>}
      </span>
      <Badge variant={st.variant}>{st.label}</Badge>
    </button>
  );
}

const CLAIM_STATUS_OF = {
  confirmed: { label: "已确认", variant: "success" },
  pending: { label: "待确认", variant: "warning" },
  awaiting_round: { label: "待下轮反证", variant: "info" },
  testing: { label: "反证中", variant: "running" },
  candidate: { label: "候选", variant: "paused" },
};

function clip(s, n) {
  return s && s.length > n ? s.slice(0, n) + "…" : s || "";
}

// RawTranscript 会话原文下钻(按需拉取,不复用轮询):态势快照(user)
// 与研判回复(assistant)的原文。
function RawTranscript({ taskID, sessionID }) {
  const [data, setData] = React.useState(null);
  React.useEffect(() => {
    let dead = false;
    fetch(`/api/tasks/${taskID}/transcripts/${sessionID}`)
      .then((r) => r.json())
      .then((d) => !dead && setData(d))
      .catch(() => !dead && setData({ exists: false, entries: [] }));
    return () => {
      dead = true;
    };
  }, [taskID, sessionID]);
  if (!data) return <div className="text-[11px] text-muted-foreground">读取原文…</div>;
  if (!data.exists)
    return <div className="text-[11px] text-muted-foreground">会话原文未保存(该轮无记录文件)。</div>;
  return (
    <div className="space-y-1.5">
      {(data.entries || []).map((e, i) => (
        <TranscriptEntryView key={i} entry={e} compact />
      ))}
    </div>
  );
}

// ---- 虫群会话页(P2-08b:单位列表 + 每单位执行轨迹 + 直连对话) ----

const UNIT_STATUS = {
  running: { label: "执行中", variant: "running" },
  dispatched: { label: "已派发", variant: "info" },
  paused: { label: "冬眠中", variant: "paused" },
  done: { label: "已完成", variant: "success" },
  exhausted: { label: "预算耗尽", variant: "warning" },
  failed: { label: "失败", variant: "danger" },
  aborted: { label: "已熔断", variant: "info" },
  approval_denied: { label: "审批拒绝终止", variant: "danger" },
  blocked: { label: "受阻", variant: "warning" },
};

// unitStatusMeta 单位行状态(P4-02:排队中条目 + 拒绝来源区分——
// approval_denied 按拒绝线呈现"内置拦截规则拒绝(规则名)/危害声明
// 拒绝/审批拒绝终止"等,见 lib/api.js denyStatusLabel)。
function unitStatusMeta(u) {
  if (u.queued) return { label: "排队中", variant: "info" };
  if (u.paused) return UNIT_STATUS.paused;
  if (u.status === "approval_denied")
    return { label: denyStatusLabel(u), variant: "danger", deny: true };
  return UNIT_STATUS[u.status] || { label: u.status, variant: "info" };
}

// 收场终因 → 人话(P4-06 ② 状态行;表外词按状态词兜底,不编造解释)。
// 注:exhausted 不再有"转接力"语义——P4-01 起配了上限的单位到线在原
// 会话内续跑,exhausted 落地仅剩任务总闸场景,故按终因呈"任务总闸到点"。
// 受阻终态的终因本就是人话语句(模型连续失败简报),不入词表,由状态
// 词"受阻"收口。
const FINISH_WORDS = {
  completed: "正常完成",
  timeout: "任务总闸到点",
  max_turns: "轮次到线",
  checkpointed: "已存档",
  "pause_stale(R14)": "挂起复检",
  aborted: "已熔断",
  aborted_streaming: "已熔断",
  aborted_tools: "已熔断",
  model_error: "失败(模型错误)",
  stream_error: "失败(流式错误)",
  mirror_error: "失败(镜会话失败)",
  resume_error: "失败(续跑失败)",
  no_terminal: "失败(无终态)",
  prompt_too_long: "失败(上下文超限)",
  blocking_limit: "失败(上下文超限)",
  image_error: "失败(不支持的媒体)",
  stop_hook_prevented: "失败(停止被拦)",
  hook_stopped: "失败(被拦停)",
};

// finishLineOf 收场人话状态行(P4-06 ②:时间账 note 原文不进界面,此为
// 唯一呈现):"18 轮 · 正常完成 · 计费 3 分 12 秒"。拒绝终态按拒绝线
// 呈现(与徽标同源)。
function finishLineOf(unit) {
  if (!unit.finish) return null;
  const word =
    unit.status === "approval_denied"
      ? denyStatusLabel(unit)
      : FINISH_WORDS[unit.finish.reason] ||
        (UNIT_STATUS[unit.status] || {}).label ||
        unit.status;
  return `${unit.finish.turns} 轮 · ${word} · 计费 ${fmtBill(unit.billable_seconds)}`;
}

// fmtBill 计费时长(<90 秒精确到 0.1 秒,更长为分秒;与观测页同口径)。
function fmtBill(seconds) {
  const s = Math.max(0, Number(seconds || 0));
  if (s < 90) return `${Math.round(s * 10) / 10} 秒`;
  const m = Math.floor(s / 60);
  return `${m} 分 ${Math.round(s - m * 60)} 秒`;
}

function SwarmPane({ task, unitFocus, onInterject }) {
  const taskID = task?.id;
  const url = taskID ? `/api/tasks/${taskID}/units` : null;
  const { data, error } = usePolling(url || "/api/healthz", 2000, {
    immediate: Boolean(url),
  });
  const [selected, setSelected] = React.useState(null); // session_id
  const [node, setNode] = React.useState(null); // 节点详情抽屉({type,id},点击下钻)
  // 最新在先;置顶会话(P3-13b ②)再提至最前(置顶内部保持原相对次序)。
  // P4-02 ① 排队中条目(dispatched 步骤)排在会话行之后。
  const rows = data?.units || [];
  const sessions = rows.filter((u) => !u.queued).reverse();
  const units = [
    ...sessions.filter((u) => u.meta?.pinned),
    ...sessions.filter((u) => !u.meta?.pinned),
    ...rows.filter((u) => u.queued),
  ];
  const unit = units.find((u) => u.session_id === selected) || null;

  // 深链单位(工作台执行流跳入):列表到达后选中对应行,一次即止。
  const jumped = React.useRef(false);
  React.useEffect(() => {
    if (!jumped.current && unitFocus && units.length > 0) {
      jumped.current = true;
      setSelected(unitFocus);
    }
  }, [unitFocus, units.length]);

  // 排队步骤详情:点击行拉节点核对面(与研判轮节点 chip 同一入口)。
  const openStep = async (stepID) => {
    const r = await fetch(`/api/tasks/${taskID}/nodes/${stepID}`);
    if (!r.ok) {
      toast.error("节点不在账本中");
      return;
    }
    const n = await r.json();
    setNode({ type: n.type, id: n.id });
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2">
      {error && (
        <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-1 text-xs text-status-danger">
          读取失败:{error}
        </div>
      )}
      <div className="flex min-h-0 flex-1 gap-2">
        {/* 单位列表(会话锚去重,跨进程稳定)+ 排队中条目(派发即可见) */}
        <div className="w-[232px] shrink-0 overflow-y-auto rounded-xl border bg-card">
          {units.length === 0 && (
            <div className="px-3 py-8 text-center text-[13px] text-muted-foreground">
              尚无虫群单位。主宰派发意图后,排队与执行在此列示。
            </div>
          )}
          {units.map((u) => {
            const st = unitStatusMeta(u);
            if (u.queued) {
              return (
                <button
                  key={`q-${u.step_id}`}
                  className="flex w-full flex-col gap-0.5 border-b border-dashed px-3 py-1 text-left text-[12px] cursor-pointer last:border-b-0 hover:bg-hover-lift"
                  title={`等待虫群领取 · ${u.step_title || u.step_id}`}
                  onClick={() => openStep(u.step_id)}
                >
                  <span className="flex items-center gap-2">
                    <Badge variant={st.variant}>{st.label}</Badge>
                  </span>
                  <span className="truncate text-[11px] text-muted-foreground" title={u.step_title}>
                    {u.step_title || ""}
                  </span>
                </button>
              );
            }
            return (
              <div
                key={u.session_id}
                role="button"
                tabIndex={0}
                className={cn(
                  "flex w-full flex-col gap-0.5 border-b px-3 py-1 text-left text-[12px] cursor-pointer last:border-b-0 hover:bg-hover-lift",
                  selected === u.session_id && "bg-primary/15"
                )}
                onClick={() => setSelected(u.session_id)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") setSelected(u.session_id);
                }}
              >
                <span className="flex items-center gap-2">
                  {st.deny ? (
                    <Badge variant={st.variant} className="max-w-40 shrink-0" title={st.label}>
                      <span className="truncate">{st.label}</span>
                    </Badge>
                  ) : (
                    <Badge variant={st.variant}>{st.label}</Badge>
                  )}
                  {u.meta?.pinned &&
                    React.createElement(iconOf("pin"), { className: "size-3 shrink-0 text-primary" })}
                  {/* P4-06 审核迭代:单位·步骤号只在会话头部展示(精确
                      落点);列表行只呈状态 + 该单位在做什么(意图/名称)。 */}
                  {u.meta?.title && (
                    <span className="max-w-[9.5rem] truncate font-medium" title={u.meta.title}>
                      {u.meta.title}
                    </span>
                  )}
                  <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                    {u.started_at?.slice(11)}
                  </span>
                  <SessionMarkMenu
                    taskID={taskID}
                    sessionID={u.session_id}
                    defaultName={u.step_title || u.step_id}
                    meta={u.meta}
                    pinnable
                    iconOnly
                  />
                </span>
                <span className="truncate text-[11px] text-muted-foreground" title={u.meta?.note ? `${u.step_title}\n${u.meta.note}` : u.step_title}>
                  {u.meta?.note ? `${u.step_title || ""} · ${u.meta.note}` : u.step_title || ""}
                </span>
              </div>
            );
          })}
        </div>
        {/* 单位会话页 */}
        {unit ? (
          <UnitSessionPane task={task} unit={unit} onInterject={onInterject} />
        ) : (
          <div className="flex min-h-0 flex-1 items-center justify-center rounded-xl border bg-card text-[13px] text-muted-foreground">
            在左侧选择单位,查看其执行轨迹与直连对话。
          </div>
        )}
      </div>
      <NodeSheetHost tid={taskID} value={node} onChange={setNode} />
    </div>
  );
}

// nowStamp 本地时间戳(与账本事件侧 at 同格式,本地即显的消息用)。
const nowStamp = () => new Date().toLocaleString("sv-SE").slice(0, 19);

// ---- 会话标记(P3-13b ②:重命名 + 备注 + 置顶,入账留痕; ----
// P4-02 ⑤ 入口改「⋯」下拉菜单:重命名…/备注…各弹小窗,置顶菜单项
// 带勾直切——发现性对齐标准菜单形态)

// saveSessionMeta 会话标记写(部分提交:只带变更字段)。
async function saveSessionMeta(taskID, sessionID, body, okWord) {
  const { ok, body: resp } = await postJSON(
    `/api/tasks/${taskID}/sessions/${encodeURIComponent(sessionID)}/meta`,
    body
  );
  if (!ok) {
    toast.error(resp?.error || "保存失败");
    return false;
  }
  toast.success(okWord);
  return true;
}

// SessionMarkMenu 会话标记「⋯」菜单:重命名…(弹小窗)/ 备注…(弹小窗)/
// 置顶(勾选直切,仅虫群单位会话提供——牧者与主宰是单会话面板,无排序
// 语境不设)。iconOnly 供列表行内嵌(窄位只出图标)。
function SessionMarkMenu({ taskID, sessionID, defaultName, meta, pinnable, iconOnly }) {
  const [renaming, setRenaming] = React.useState(false);
  const [noting, setNoting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  const togglePin = async (checked) => {
    await saveSessionMeta(taskID, sessionID, { pinned: checked }, checked ? "已置顶" : "已取消置顶");
  };

  return (
    <span className={cn("flex shrink-0 items-center", !iconOnly && "gap-2")} onClick={(e) => e.stopPropagation()}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="sm"
            variant="ghost"
            className={cn("h-7 px-1.5 text-xs", iconOnly && "size-6 p-0")}
            title="会话标记"
          >
            {React.createElement(iconOf("moreHorizontal"), { className: "size-4" })}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setRenaming(true)}>重命名…</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setNoting(true)}>备注…</DropdownMenuItem>
          {pinnable && (
            <DropdownMenuCheckboxItem
              checked={Boolean(meta?.pinned)}
              onCheckedChange={togglePin}
            >
              置顶
            </DropdownMenuCheckboxItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <MarkRenameDialog
        open={renaming}
        onOpenChange={setRenaming}
        taskID={taskID}
        sessionID={sessionID}
        defaultName={defaultName}
        meta={meta}
        busy={busy}
        setBusy={setBusy}
      />
      <MarkNoteDialog
        open={noting}
        onOpenChange={setNoting}
        taskID={taskID}
        sessionID={sessionID}
        meta={meta}
        busy={busy}
        setBusy={setBusy}
      />
    </span>
  );
}

// MarkRenameDialog 重命名小窗(只改名称;空 = 清除回默认名)。
function MarkRenameDialog({ open, onOpenChange, taskID, sessionID, defaultName, meta, busy, setBusy }) {
  const [title, setTitle] = React.useState("");
  React.useEffect(() => {
    if (open) setTitle(meta?.title || "");
  }, [open]);
  const save = async () => {
    setBusy(true);
    const ok = await saveSessionMeta(taskID, sessionID, { title: title.trim() }, "名称已保存");
    setBusy(false);
    if (ok) onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>重命名</DialogTitle>
          <DialogDescription>默认名:{defaultName};留空恢复默认名</DialogDescription>
        </DialogHeader>
        <Input
          value={title}
          maxLength={80}
          placeholder={defaultName}
          autoFocus
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") save();
          }}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={save} disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// MarkNoteDialog 备注小窗(只改备注;空 = 清除)。
function MarkNoteDialog({ open, onOpenChange, taskID, sessionID, meta, busy, setBusy }) {
  const [note, setNote] = React.useState("");
  React.useEffect(() => {
    if (open) setNote(meta?.note || "");
  }, [open]);
  const save = async () => {
    setBusy(true);
    const ok = await saveSessionMeta(taskID, sessionID, { note: note.trim() }, "备注已保存");
    setBusy(false);
    if (ok) onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>备注</DialogTitle>
          <DialogDescription>随会话留痕;留空清除</DialogDescription>
        </DialogHeader>
        <Textarea
          value={note}
          maxLength={500}
          className="min-h-[72px] resize-none text-[13px]"
          placeholder="选填,如:SQL 注入验证那条"
          autoFocus
          onChange={(e) => setNote(e.target.value)}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={save} disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// SessionMarkBar 会话头部标记条:名称(自定义或默认,P4-02 ③ 长意图
// 标题可查全文)+ 备注一行 + 「⋯」标记菜单。
function SessionMarkBar({ taskID, sessionID, defaultName, meta, pinnable, className }) {
  return (
    <div className={cn("flex min-w-0 items-center gap-2 text-[13px]", className)}>
      {Boolean(meta?.pinned) &&
        React.createElement(iconOf("pin"), { className: "size-3.5 shrink-0 text-primary" })}
      <LongText
        text={meta?.title || defaultName}
        max={48}
        variant="dialog"
        label={meta?.title ? "名称" : "意图标题"}
        className="min-w-0 font-medium"
      />
      {meta?.note && (
        <span className="min-w-0 max-w-56 flex-1 truncate text-xs text-muted-foreground" title={meta.note}>
          {meta.note}
        </span>
      )}
      <SessionMarkMenu
        taskID={taskID}
        sessionID={sessionID}
        defaultName={defaultName}
        meta={meta}
        pinnable={pinnable}
      />
    </div>
  );
}

// UnitSessionPane 单位会话页:顶部信息条 → 执行轨迹流(会话原文 +
// 直连对话归并)→ 直连输入(问/改 + 单位控制三按钮 + 插话)。
// P3-05c ⑤ 中间态反馈(轻量版):执行中轮询加快(700ms),思考与
// 工具调用卡随转录增长实时出现——发消息后不再"死静后突然出结果";
// 完整流式归 P3-08。
function UnitSessionPane({ task, unit, onInterject }) {
  const taskID = task?.id;
  const running = unit.running && !unit.paused;
  const { data: raw, error } = usePolling(
    `/api/tasks/${taskID}/transcripts/${unit.session_id}`,
    running ? 700 : 2000
  );
  const chatURL = `/api/tasks/${taskID}/interject?target=unit&session=${encodeURIComponent(unit.session_id)}`;
  const { data: chat, setData: setChat } = usePolling(chatURL, running ? 700 : 2000);
  const [busy, setBusy] = React.useState(false);
  const [confirmStop, setConfirmStop] = React.useState(false);
  const bottomRef = React.useRef(null);

  const st = unitStatusMeta(unit);
  const finishLine = finishLineOf(unit);
  const messages = chat?.messages || [];
  const steers = chat?.steers || [];
  const frozen = Boolean(chat?.frozen);
  const controllable = unit.running && !frozen;

  React.useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [raw?.entries?.length, messages.length, messages[messages.length - 1]?.reply]);

  const steerStateOf = (id) =>
    (steers.find((s) => s.steer_id === id) || {}).state || "queued";

  const ctl = async (action) => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/units/${unit.step_id}/${action}`, {});
    setBusy(false);
    if (!ok) {
      toast.error(body?.error || "操作失败");
      return;
    }
    toast.success(action === "pause" ? "已冬眠" : "已唤醒");
  };

  const stopUnit = async () => {
    setConfirmStop(false);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/kill-work`, {
      step_id: unit.step_id,
      reason: "单位会话页熔断:操作者终止该单位",
    });
    if (!ok) {
      toast.error(body?.error || "熔断失败");
      return;
    }
    toast.success("单位已熔断");
  };

  // 执行轨迹(会话原文)与直连对话按时间归并。
  const stream = [];
  (raw?.entries || []).forEach((e, i) => stream.push({ at: e.at, key: `t${i}`, kind: "turn", entry: e }));
  messages.forEach((m) => stream.push({ at: m.at, key: m.id, kind: "chat", msg: m }));
  stream.sort((a, b) => (a.at || "").localeCompare(b.at || ""));

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2">
      <SessionInfoBar info={unit.info} startedAt={unit.started_at} billableSeconds={unit.billable_seconds} />
      {error && (
        <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-1 text-xs text-status-danger">
          读取失败:{error}
        </div>
      )}
      {/* 会话头部(P4-06 ③):徽标 + 单位·步骤号 + 意图摘要(① 一两行
          截断,悬停全文 / 点击展开)+ 人话状态行(② note 原文不进界面) */}
      <div className="flex flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2 text-[13px]">
          <Badge variant={st.variant} className="shrink-0" title={st.deny ? st.label : undefined}>
            {st.deny ? <span className="max-w-64 truncate">{st.label}</span> : st.label}
          </Badge>
          <span className="shrink-0 font-mono text-xs text-muted-foreground" title="单位 · 步骤">
            {unit.unit_id} · {unit.step_id}
          </span>
          {Boolean(unit.meta?.pinned) &&
            React.createElement(iconOf("pin"), { className: "size-3.5 shrink-0 text-primary" })}
          <LongText
            text={unit.meta?.title || unit.step_title || unit.step_id}
            lines={2}
            label={unit.meta?.title ? "名称" : "意图标题"}
            className="min-w-0 flex-1 font-medium"
          />
          <SessionMarkMenu
            taskID={taskID}
            sessionID={unit.session_id}
            defaultName={unit.step_title || unit.step_id}
            meta={unit.meta}
            pinnable
            iconOnly
          />
        </div>
        {unit.meta?.note && (
          <div className="truncate text-[11px] text-muted-foreground" title={unit.meta.note}>
            备注:{unit.meta.note}
          </div>
        )}
        {finishLine && <div className="text-[11px] text-muted-foreground">{finishLine}</div>}
      </div>
      {/* 流:执行轨迹 + 直连对话 */}
      <div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto rounded-xl border bg-card px-3 py-3">
        {stream.length === 0 && (
          <div className="py-10 text-center text-[13px] text-muted-foreground">
            会话原文尚未保存。单位动工后,执行轨迹(输入/思考/工具调用/结果)在此呈现。
          </div>
        )}
        {stream.map((item) =>
          item.kind === "turn" ? (
            <TranscriptEntryView key={item.key} entry={item.entry} />
          ) : (
            <InterjectBlock key={item.key} msg={item.msg} steerStateOf={steerStateOf} />
          )
        )}
        <div ref={bottomRef} />
      </div>
      {/* 直连输入:问(默认)/改(舵杆注入)+ 单位控制 + 插话窗 */}
      <DirectInput
        taskID={taskID}
        chat={chat}
        setChat={setChat}
        target={{
          kind: "unit",
          unitID: unit.unit_id,
          stepID: unit.step_id,
          sessionID: unit.session_id,
          running: unit.running,
          paused: unit.paused,
        }}
        frozen={frozen}
        finished={!unit.running}
        busy={busy}
        onInterject={() => onInterject({
          kind: "unit",
          unitID: unit.unit_id,
          stepID: unit.step_id,
          sessionID: unit.session_id,
          stepTitle: unit.step_title,
          running: unit.running,
          paused: unit.paused,
        })}
        confirmStop={confirmStop}
        setConfirmStop={setConfirmStop}
        onStop={stopUnit}
        buttons={
          controllable ? (
            <>
              {unit.paused ? (
                <Button size="sm" variant="outline" title="原地接着干" disabled={busy} onClick={() => ctl("resume")}>
                  {React.createElement(iconOf("sun"), { className: "size-3.5" })}
                  唤醒
                </Button>
              ) : (
                <Button size="sm" variant="outline" title="当前工具调用完成后挂起" disabled={busy} onClick={() => ctl("pause")}>
                  {React.createElement(iconOf("moon"), { className: "size-3.5" })}
                  冬眠
                </Button>
              )}
              <Button size="sm" variant="outline" title="熔断该单位(不可恢复)" onClick={() => setConfirmStop(true)}>
                熔断
              </Button>
            </>
          ) : null
        }
      />
    </div>
  );
}

// ---- 直连输入(主宰/单位会话页共用) ----

// DirectInput 会话页主输入框:问(不改执行,即时答;已结束单位基于
// 历史回答)与改(对虫群 = 舵杆注入带回执;对主宰 = 建议进正式规划)
// 两模式;按钮组挂输入框右上(插话窗 + 冬眠/唤醒/熔断)。发言走插话
// 通道(interject.record 审计流,全记账)。
function DirectInput({ taskID, chat, setChat, target, frozen, finished, busy, onInterject, buttons, confirmStop, setConfirmStop, onStop }) {
  const [text, setText] = React.useState("");
  const [mode, setMode] = React.useState("ask");
  const [sending, setSending] = React.useState(false);
  const isUnit = target.kind === "unit";

  const send = async () => {
    const body = text.trim();
    if (!body || !taskID) return;
    setSending(true);
    const payload = { target: target.kind, mode, text: body };
    if (isUnit) {
      payload.unit_id = target.unitID;
      payload.step_id = target.stepID;
      payload.session_id = target.sessionID;
    }
    const { ok, body: resp } = await postJSON(
      `/api/tasks/${taskID}/interject/messages`,
      payload
    );
    setSending(false);
    if (!ok) {
      toast.error(resp?.error || "发送失败");
      return;
    }
    setText("");
    // 本地即显(P3-05c ⑤):问与改都立刻上屏——改向带回执状态机,
    // 问按已排队呈现,回复经轮询到达(回答期间轮询加快)。
    if (chat) {
      setChat({
        ...chat,
        messages: [
          ...chat.messages,
          {
            id: resp.message_id,
            mode,
            text: body,
            state: mode === "steer" ? "issued" : "queued",
            ref_id: resp.ref_id,
            ref_kind: isUnit ? "steer" : "advice",
            advice_state: isUnit ? undefined : "pending",
            at: nowStamp(),
          },
        ],
      });
    }
  };

  return (
    <div className="rounded-xl border bg-card p-3">
      <div className="mb-2 flex items-center gap-1">
        {[
          ["ask", "问", isUnit ? "基于该单位会话历史回答,不改执行" : "问主宰当前研判相关问题"],
          ["steer", "改", isUnit ? "舵杆注入,下一次工具调用前生效,带回执" : "建议进正式规划,裁决后改"],
        ].map(([v, label, hint]) => (
          <button
            key={v}
            disabled={frozen}
            title={hint}
            className={cn(
              "rounded-md border px-2 py-0.5 text-xs transition-colors cursor-pointer",
              mode === v
                ? "border-primary/50 bg-primary/15 text-primary"
                : "text-muted-foreground hover:bg-hover-lift",
              frozen && "cursor-not-allowed opacity-50"
            )}
            onClick={() => setMode(v)}
          >
            {label}
          </button>
        ))}
        <span className="flex-1" />
        <Button size="sm" variant="outline" title="旁路子会话窗(独立留存)" disabled={frozen} onClick={onInterject}>
          {React.createElement(iconOf("megaphone"), { className: "size-3.5" })}
          插话
        </Button>
        {buttons}
      </div>
      <div className="flex items-end gap-2">
        <Textarea
          value={text}
          disabled={frozen}
          placeholder={
            frozen
              ? "任务冬眠中,唤醒后继续对话"
              : mode === "ask"
                ? isUnit
                  ? finished
                    ? "该单位已结束,问话基于其历史记录回答"
                    : "问这只单位相关问题(基于其会话历史回答)"
                  : "问主宰当前研判相关问题"
                : isUnit
                  ? finished
                    ? "该单位已结束,改向不可用"
                    : "给这只单位改方向(下一次工具调用前生效)"
                  : "给主宰提方向建议(裁决后改变规划)"
          }
          className="min-h-[48px] text-[13px] resize-none"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              send();
            }
          }}
        />
        <Button
          onClick={send}
          disabled={
            sending || !text.trim() || frozen || !taskID ||
            (finished && mode === "steer")
          }
        >
          发送
        </Button>
      </div>
      {confirmStop && (
        <div className="mt-2 rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-2 text-xs">
          <div className="text-status-danger">
            熔断将终止该单位(不可恢复),主宰收割可见原因。确认熔断?
          </div>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="danger" onClick={onStop}>
              确认熔断
            </Button>
            <Button size="sm" variant="outline" onClick={() => setConfirmStop(false)}>
              取消
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

// ---- 会话原文呈现(转录 JSONL 的条目) ----

// TranscriptEntryView 一条转录单元:输入(任务包/续跑/舵杆注入)、思考
// 折叠卡、工具调用卡、工具结果卡、正文(长文折叠卡)、压缩边界分隔线。
// 会话页整页固定(零滚动),全部长内容折叠默认、展开限高框内滚。
// P3-06 起导出:发现详情的复测会话原文复用同一呈现(不另建平行实现)。
export function TranscriptEntryView({ entry, compact }) {
  if (entry.kind === "boundary") {
    return (
      <div className="flex items-center gap-2 py-0.5 text-[11px] text-muted-foreground">
        <span className="h-px flex-1 bg-border" />
        上下文已压缩{entry.pre_tokens ? `(压缩前 ${entry.pre_tokens.toLocaleString("en-US")} token` +
          (entry.summarized ? `,${entry.summarized} 条消息` : "") + ")" : ""}
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }
  if (entry.kind === "user") {
    return (
      <div className="flex flex-col items-start gap-1">
        {entry.text && <InputCard text={entry.text} compact={compact} />}
        {(entry.results || []).map((res, i) => (
          <ResultCard key={i} res={res} />
        ))}
      </div>
    );
  }
  // assistant
  return (
    <div className="flex flex-col items-start gap-1">
      {entry.thinking && <ThinkingCard text={entry.thinking} />}
      {(entry.tools || []).map((t, i) => (
        <CallCard key={i} tool={t} />
      ))}
      {entry.text && <BodyText text={entry.text} compact={compact} />}
    </div>
  );
}

// bodyFoldThreshold 正文折叠阈值(字符):以内直接气泡常显,以上折叠成卡。
const bodyFoldThreshold = 160;

// BodyText 单位正文:短文气泡常显;长文(执行总结/分段汇报)折叠卡,
// 展开后限高框内滚——一条长总结不再垄断整页。
function BodyText({ text, compact }) {
  const [open, setOpen] = React.useState(false);
  if (text.length <= bodyFoldThreshold) {
    return (
      <div className="max-w-[90%] rounded-lg border bg-background px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
        {text}
      </div>
    );
  }
  return (
    <div className="max-w-[85%] rounded-lg border bg-background">
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">正文</Badge>
        <span className="truncate">{clip(text.split("\n")[0] || "", 60)}</span>
        <span className="ml-auto shrink-0 text-muted-foreground">{text.length.toLocaleString("en-US")} 字</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <pre
          className={cn(
            "overflow-y-auto rounded-b-lg border-t bg-input-surface px-3 py-2 text-xs whitespace-pre-wrap break-all",
            compact ? "max-h-56" : "max-h-64"
          )}
        >
          {text}
        </pre>
      )}
    </div>
  );
}

// InputCard 单位侧输入(任务包 / 续跑指令 / 舵杆注入等引擎注入的
// user 消息):默认折叠,点击展开原文。
function InputCard({ text, compact }) {
  const [open, setOpen] = React.useState(false);
  const head = clip(text.split("\n")[0] || "", 80);
  return (
    <div className="max-w-[85%] rounded-lg border bg-background">
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] text-muted-foreground cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">输入</Badge>
        <span className="truncate">{head}</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "ml-auto size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <pre
          className={cn(
            "overflow-y-auto rounded-b-lg border-t bg-input-surface px-3 py-2 font-mono text-xs whitespace-pre-wrap break-all",
            compact ? "max-h-64" : "max-h-72"
          )}
        >
          {text}
        </pre>
      )}
    </div>
  );
}

// CallCard 工具调用卡(转录侧:名称 + 入参)。
function CallCard({ tool }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="max-w-[85%] rounded-lg border bg-background">
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">工具调用</Badge>
        <span className="truncate font-mono">{tool.name}</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "ml-auto size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <pre className="max-h-64 overflow-y-auto overflow-x-auto border-t bg-input-surface px-3 py-2 font-mono text-xs whitespace-pre-wrap break-all">
          {tool.input}
        </pre>
      )}
    </div>
  );
}

// ResultCard 工具结果卡(状态色边:出错红边)。
function ResultCard({ res }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div
      className={cn(
        "max-w-[85%] rounded-lg border bg-background",
        res.is_error && "border-status-danger/60"
      )}
    >
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">{res.is_error ? "工具出错" : "工具结果"}</Badge>
        <span className="truncate font-mono">{res.id}</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "ml-auto size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <pre className="max-h-56 overflow-y-auto border-t px-3 py-2 font-mono text-xs whitespace-pre-wrap break-all bg-input-surface">
          {res.text}
        </pre>
      )}
    </div>
  );
}

// ---- 牧者会话 ----

function ShepherdPane({ task }) {
  const taskID = task?.id;
  const url = taskID ? `/api/tasks/${taskID}/shepherd` : null;
  // P3-05c ⑤ 中间态:牧者回答期间轮询加快(思考/工具卡与回复随到随现)。
  // interval 为函数式(入参最近响应),避免在本声明的解构里引用 data。
  const { data, error, setData } = usePolling(
    url || "/api/healthz",
    (d) => (d?.busy ? 700 : 2000),
    { immediate: Boolean(url) }
  );
  const [text, setText] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [node, setNode] = React.useState(null); // 节点详情抽屉({type,id},fact# 等引用点击下钻)
  const [confirmFuse, setConfirmFuse] = React.useState(false);
  const [confirmHiber, setConfirmHiber] = React.useState(false);
  const bottomRef = React.useRef(null);

  const messages = data?.messages || [];
  const advices = data?.advices || []; // 无锚定的旧建议(尾部呈现)
  const busy = Boolean(data?.busy);
  const providerOn = data?.provider_on;
  const hibernated = Boolean(data?.hibernated);
  const pendingApprovals = data?.pending_approvals || 0;

  // 新消息/回复到达时滚到底。
  React.useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [messages.length, messages[messages.length - 1]?.reply]);

  const send = async () => {
    const body = text.trim();
    if (!body || !taskID) return;
    setSending(true);
    const { ok, body: resp } = await postJSON(
      `/api/tasks/${taskID}/shepherd/messages`,
      { text: body }
    );
    setSending(false);
    if (!ok) {
      toast.error(`发送失败:${resp?.error || "网络错误"}`);
      return;
    }
    setText("");
    // 本地即显(P3-05c ⑤):已入队立刻上屏(已排队),回复经轮询到达
    //(回答期间 700ms 快轮)——发消息不再死静。
    if (data && resp?.message_id && !messages.some((m) => m.id === resp.message_id)) {
      setData({
        ...data,
        messages: [...messages, { id: resp.message_id, text: body, state: "queued", at: nowStamp() }],
      });
    }
  };

  const hibernate = async () => {
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/hibernate`, {});
    setConfirmHiber(false);
    if (!ok) {
      toast.error(body?.error || "冬眠失败");
      return;
    }
    toast.success(
      body?.pending_approvals
        ? `任务已冬眠;待裁决审批 ${body.pending_approvals} 笔已冻结`
        : "任务已冬眠"
    );
  };

  const wake = async () => {
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/hibernate-wake`, {});
    if (!ok) {
      toast.error(body?.error || "唤醒失败");
      return;
    }
    toast.success("任务已唤醒");
  };

  const fuse = async () => {
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/stop`, {
      reason: "会话页熔断:操作者终止任务",
    });
    setConfirmFuse(false);
    if (!ok) {
      toast.error(body?.error || "熔断失败");
      return;
    }
    toast.success("任务已熔断");
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2">
      <SessionInfoBar
        info={data?.info}
        startedAt={data?.started_at}
        deductSeconds={data?.hibernate_seconds}
        frozen={hibernated}
      />
      <SessionMarkBar
        taskID={taskID}
        sessionID={`shepherd-${taskID}`}
        defaultName="牧者"
        meta={data?.meta}
      />
      {error && (
        <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-1 text-xs text-status-danger">
          读取失败:{error}
        </div>
      )}
      {!providerOn && data && !hibernated && (
        <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-1 text-xs text-status-warning">
          模型未配置:消息会入账并记为失败。在 LLM配置 页激活模型后重发。
        </div>
      )}
      {/* 对话流 */}
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto rounded-xl border bg-card px-3 py-3">
        {messages.length === 0 && (
          <div className="py-10 text-center text-[13px] text-muted-foreground">
            会话为空。问进度、纠方向、插急活,牧者代你查图与传话。
          </div>
        )}
        {messages.map((m) => (
          <MessageBlock key={m.id} msg={m} taskID={taskID} onNode={setNode} />
        ))}
        {advices.map((a) => (
          <InlineAdvice key={a.id} advice={a} />
        ))}
        <div ref={bottomRef} />
      </div>
      {/* 输入框 */}
      <div className="rounded-xl border bg-card p-3">
        <div className="mb-1 flex items-center justify-between text-[11px] text-muted-foreground">
          <span>
            {hibernated
              ? "任务冬眠中"
              : busy
                ? "牧者在回答"
                : messages.length > 0
                  ? "继续对话"
                  : "发起对话"}
          </span>
          {/* 任务控制组(P2-08):冬眠/唤醒 + 熔断(终止不可恢复) */}
          <span className="flex items-center gap-1">
            {hibernated ? (
              <Button size="sm" variant="outline" title="唯一恢复入口:同一单位同一会话原地继续" onClick={wake}>
                {React.createElement(iconOf("sun"), { className: "size-3.5" })}
                唤醒
              </Button>
            ) : (
              <Button
                size="sm"
                variant="outline"
                title="主宰与虫群挂起、会话冻结、计时器停走(可唤醒)"
                onClick={() => setConfirmHiber(true)}
              >
                {React.createElement(iconOf("moon"), { className: "size-3.5" })}
                冬眠
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              title="终止任务(不可恢复)"
              onClick={() => setConfirmFuse(true)}
            >
              {React.createElement(iconOf("unplug"), { className: "size-3.5" })}
              熔断
            </Button>
          </span>
        </div>
        <div className="flex items-end gap-2">
          <Textarea
            value={text}
            disabled={hibernated}
            placeholder={hibernated ? "任务冬眠中,唤醒后继续对话" : "问进度 / 纠方向 / 插急活"}
            className="min-h-[48px] text-[13px] resize-none"
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
          />
          <Button onClick={send} disabled={sending || !text.trim() || !taskID || hibernated}>
            发送
          </Button>
        </div>
        {confirmHiber && (
          <div className="mt-2 rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-2 text-xs">
            <div>
              冬眠:主宰与虫群挂起、本会话冻结、计时器停走;唤醒后原地继续。
              {pendingApprovals > 0 && ` 当前待裁决审批 ${pendingApprovals} 笔将冻结为只读。`}
            </div>
            <div className="mt-2 flex gap-2">
              <Button size="sm" onClick={hibernate}>
                确认冬眠
              </Button>
              <Button size="sm" variant="outline" onClick={() => setConfirmHiber(false)}>
                取消
              </Button>
            </div>
          </div>
        )}
        {confirmFuse && (
          <div className="mt-2 rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-2 text-xs">
            <div className="text-status-danger">熔断终止任务,不可恢复。确认?</div>
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="danger" onClick={fuse}>
                确认熔断
              </Button>
              <Button size="sm" variant="outline" onClick={() => setConfirmFuse(false)}>
                取消
              </Button>
            </div>
          </div>
        )}
      </div>
      <NodeSheetHost tid={taskID} value={node} onChange={setNode} />
    </div>
  );
}

// MessageBlock 一条问答:造物主消息(带送达状态)→ 思考段 → 工具折叠卡
// 流 → 牧者回复(引用可点击)。
function MessageBlock({ msg, taskID, onNode }) {
  const st = DELIVERY[msg.state] || DELIVERY.queued;
  // 系统收尾提示(任务终态,P2-08f):非造物主发言,居中窄条呈现;
  // 牧者的一句话进展总结照常挂在其下。
  if (msg.system) {
    return (
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 py-0.5">
          <span className="h-px flex-1 bg-border" />
          <span className="text-[11px] text-muted-foreground">{msg.text.split("。")[0]}</span>
          <span className="h-px flex-1 bg-border" />
        </div>
        {(msg.thinking || (msg.tool_calls && msg.tool_calls.length) > 0 || msg.reply) && (
          <div className="flex flex-col items-start gap-2">
            {msg.thinking && <ThinkingCard text={msg.thinking} />}
            {(msg.tool_calls || []).map((c, i) => (
              <ToolCallCard key={i} call={c} />
            ))}
            {msg.reply && (
              <div className="max-w-[90%] rounded-lg border bg-background px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
                <RefText text={msg.reply} taskID={taskID} onNode={onNode} />
                <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                  <Badge variant="outline">进展总结</Badge>
                  <span className="font-mono">{msg.reply_at?.slice(11)}</span>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      {/* 造物主 */}
      <div className="flex flex-col items-end gap-0.5">
        <div className="max-w-[85%] rounded-lg bg-primary/15 px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
          {msg.text}
        </div>
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span className="font-mono">{msg.at?.slice(11)}</span>
          <Badge variant={st.variant}>{st.label}</Badge>
        </div>
        {msg.state === "failed" && msg.note && (
          <div className="max-w-[85%] text-[11px] text-status-danger">{msg.note}</div>
        )}
      </div>
      {/* 牧者 */}
      {(msg.thinking || (msg.tool_calls && msg.tool_calls.length) > 0 || msg.reply) && (
        <div className="flex flex-col items-start gap-2">
          {msg.thinking && <ThinkingCard text={msg.thinking} />}
          {(msg.tool_calls || []).map((c, i) => (
            <ToolCallCard key={i} call={c} />
          ))}
          {(msg.advices || []).map((a) => (
            <InlineAdvice key={a.id} advice={a} />
          ))}
          {msg.reply && (
            <div className="max-w-[90%] rounded-lg border bg-background px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
              <RefText text={msg.reply} taskID={taskID} onNode={onNode} />
              <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                <Badge variant="outline">回复</Badge>
                <span className="font-mono">{msg.reply_at?.slice(11)}</span>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ThinkingCard 思考段(消息类型徽标:thinking)。
function ThinkingCard({ text }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="max-w-[85%] rounded-lg border bg-background">
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] text-muted-foreground cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">思考</Badge>
        <span className="truncate">{text.slice(0, 60)}…</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "ml-auto size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <div className="border-t px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
          {text}
        </div>
      )}
    </div>
  );
}

// ToolCallCard 工具调用折叠卡(8.5:聚合一次回合的工具调用,展开为
// 步骤卡片流——状态色边 + 工具名;此处一条一卡,参数与结果头部展开)。
function ToolCallCard({ call }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div
      className={cn(
        "max-w-[85%] rounded-lg border bg-background",
        call.is_error && "border-status-danger/60"
      )}
    >
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">{call.is_error ? "工具出错" : "工具调用"}</Badge>
        <span className="truncate font-mono">{call.name}</span>
        <span className="ml-auto shrink-0 text-muted-foreground">
          {call.at?.slice(11)}
        </span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <div className="space-y-2 border-t px-3 py-2 text-xs">
          {call.args && (
            <div>
              <div className="mb-0.5 text-muted-foreground">参数</div>
              <pre className="overflow-x-auto rounded-md bg-input-surface p-2 font-mono whitespace-pre-wrap break-all">
                {call.args}
              </pre>
            </div>
          )}
          {call.result_head && (
            <div>
              <div className="mb-0.5 text-muted-foreground">结果</div>
              <pre className="max-h-56 overflow-y-auto rounded-md bg-input-surface p-2 font-mono whitespace-pre-wrap break-all">
                {call.result_head}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// InlineAdvice 对主宰的建议(纳谏)内联卡:牧者在某条消息里提交的建议,
// 卡片落在对话流的原位;裁决状态原地更新(待裁决/已采纳/未采纳+理由)。
function InlineAdvice({ advice }) {
  const meta = (st) =>
    st === "pending"
      ? { label: "待裁决", variant: "paused" }
      : st === "adopted"
        ? { label: "已采纳", variant: "success" }
        : { label: "未采纳", variant: "danger" };
  const m = meta(advice.state);
  return (
    <div className="w-[85%] rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-[12px]">
      <div className="flex items-center gap-2">
        {React.createElement(iconOf("sparkles"), { className: "size-3 shrink-0 text-primary" })}
        <span className="text-muted-foreground">对主宰的建议</span>
        <Badge variant={m.variant}>{m.label}</Badge>
        <span className="font-mono text-[11px] text-muted-foreground">{advice.id}</span>
      </div>
      <div className="mt-1 whitespace-pre-wrap [overflow-wrap:anywhere]">{advice.text}</div>
      {advice.reason && (
        <div className="mt-1 text-[11px] text-muted-foreground">裁决:{advice.reason}</div>
      )}
    </div>
  );
}

// ---- fact# 等引用可点击 ----

// 引用词形两种:带前缀编号(fact:f-xx / claim:c-xx / step:s1 / asset:a-xx,
// 牧者提示词约定)与图上 ID 的列表写法(f-xxxx: / tc-chain-xxxx: /
// h-xxxx: / a-xxxx: / g-xxxx:,后跟冒号的定义式——模型的常见列表格式)。
const REF_RE =
  /(fact|claim|step|asset|goal|hypo)[:#]([A-Za-z0-9_-]+)|\b((?:tc-chain-|f-|h-|a-|g-)[a-z0-9]+)(?=\**:)/g;

function RefText({ text, taskID, onNode }) {
  const parts = [];
  let last = 0;
  for (const m of text.matchAll(REF_RE)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const nodeID = m[2] || m[3]; // 带前缀形态取编号;列表形态整词即 ID
    parts.push(
      <button
        key={m.index}
        className="cursor-pointer font-mono text-primary underline decoration-dotted underline-offset-2"
        title="查看节点"
        onClick={async () => {
          const r = await fetch(`/api/tasks/${taskID}/nodes/${nodeID}`);
          if (!r.ok) {
            toast.error("节点不在账本中");
            return;
          }
          const n = await r.json();
          onNode({ type: n.type, id: n.id });
        }}
      >
        {m[0]}
      </button>
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

// ---- 系统事件(黑匣子) ----

function SystemEventsPane({ taskID }) {
  const url = taskID ? `/api/tasks/${taskID}/blackbox` : null;
  const { data, error } = usePolling(url || "/api/healthz", 3000, {
    immediate: Boolean(url),
  });
  const events = Array.isArray(data) ? data : [];
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="rounded-lg border bg-card px-3 py-2 text-[11px] text-muted-foreground">
        系统级事件(黑匣子):预算状态变化、心跳巡检、故障与恢复 · 共 {events.length} 条
      </div>
      {error && (
        <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-2 text-xs text-status-danger">
          读取失败:{error}
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto rounded-xl border bg-card">
        {events.length === 0 && (
          <div className="py-10 text-center text-[13px] text-muted-foreground">
            暂无系统事件
          </div>
        )}
        {events.map((e, i) => (
          <div
            key={i}
            className="flex items-start gap-3 border-b px-3 py-2 text-[12px] last:border-b-0"
          >
            <span className="shrink-0 font-mono text-muted-foreground">
              {e.at?.slice(11)}
            </span>
            <Badge variant="outline">{BLACKBOX_LABELS[e.type] || e.type}</Badge>
            <span className="min-w-0 break-words text-muted-foreground">
              {[e.step_id, e.unit_id].filter(Boolean).join(" ")}
              {e.detail?.note || e.detail?.reason ||
                (e.detail?.action
                  ? `收养动作:${ADOPT_ACTIONS[e.detail.action] || e.detail.action}`
                  : "")}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

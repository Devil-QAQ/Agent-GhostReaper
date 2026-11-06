"use client";

import * as React from "react";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { Field, Fold, INPUT } from "@/components/form-bits";
import { LongText } from "@/components/long-text";
import { PageHeader } from "@/components/page-header";
import { iconOf } from "@/components/icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
  APPROVAL_THRESHOLD_OPTIONS,
  DENY_LINE_LABELS,
  GOAL_STATUS,
  HARM_CLASS_LABELS,
  assetClassMeta,
  assetSourceMeta,
  fetchJSON,
  levelVariant,
  postJSON,
  usePolling,
} from "@/lib/api";

// RoE 授权面板(8.3 驾驶域):审批弹门(待决裁决+拒绝线记录的研判后
// 放行)+ 操作约束(快照+变更弹窗,审批门槛)+ 任务级拦截 + 目标管理。
// 增删改全部记账(资产范围摘除即时止损,P4-45;约束变更下一轮生效);
// 对话内容不能代替裁决(R9)。P2-13b
// 各区改 Card 组件、弹窗对齐 LLM配置/Agent 基准形态(Field 左标签 +
// 高级折叠 + 无改动禁用保存)。

// 审批记录类型 → 操作者语言。
const APPROVAL_TYPE_LABELS = {
  requested: "请求",
  approved: "批准",
  denied: "拒绝",
  forced: "规划后放行",
  group_pass: "同组放行",
  step_pass: "同步骤放行",
};

// 审批门槛的简短说明(操作约束区展示)。
const APPROVAL_THRESHOLD_HINT = {
  "修改与持久化": "仅增删改需批准",
  "利用深入": "利用深入及以上需批准",
  "主动测试": "主动测试及以上需批准",
  "只读探测": "全部需批准",
};

// 区块卡头:标题 + 徽标/说明 + 右侧操作(全站统一卡形态)。
function SectionHeader({ title, children, tail }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <h2 className="t-sec">{title}</h2>
      {children}
      {tail && <div className="ml-auto flex items-center gap-2">{tail}</div>}
    </div>
  );
}

export function RoePage({ currentTask }) {
  const tid = currentTask?.id;

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader
          title="RoE · 授权面板"
          subtitle="造物主画边界,虫群在边界内执行 · 增删改全部记账"
        />
        <EmptyState
          icon="scale"
          title="未选择任务"
          hint="RoE(目标与操作约束)随任务切换"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="RoE · 授权面板"
        subtitle="造物主画边界,虫群在边界内执行 · 增删改全部记账"
      />

      <ApprovalSection taskID={tid} />
      <ConstraintSection taskID={tid} />
      <IntentRulesCard taskID={tid} />
      <AssetScopeSection taskID={tid} />
      <GoalSection taskID={tid} />
    </div>
  );
}

// ---- 资产范围(P4-05,问题清单 #10;P4-45 摘除即止损) ----

// sameIDSet 两个 ID 集合是否一致(乐观覆盖交还判定用)。
function sameIDSet(a, b) {
  return a.size === b.size && [...a].every((x) => b.has(x));
}

// AssetScopeSection 授权资产范围:资产默认全部纳入授权范围,不勾即
// 摘除——摘除即止损:覆盖该资产的在途单位立即终止、排队步骤取消,
// 主宰下一轮不再派发覆盖它的意图。摘除先弹确认(告知将终止多少个
// 在途单位),全部记账(roe.updated 事件带变更备注)。
// P4-47 B ① 乐观更新:勾选点下即翻、后台提交、失败回弹+人话提示,
// 界面不等网络往返;本地覆盖在服务端轮询跟上一致后自动交还。
function AssetScopeSection({ taskID }) {
  const { data: roe, error } = usePolling(`/api/tasks/${taskID}/roe`, 2000);
  const { data: assetData } = usePolling(`/api/tasks/${taskID}/assets`, 5000);
  const assets = Array.isArray(assetData?.assets) ? assetData.assets : [];
  const serverExcluded = new Set(roe?.excluded_assets || []);
  const [overlay, setOverlay] = React.useState(null); // Set|null;null=跟随服务端
  // 服务端已与本地说一致(轮询跟上)→ 交还服务端数据源。
  React.useEffect(() => {
    setOverlay((cur) => (cur && sameIDSet(cur, serverExcluded) ? null : cur));
  }, [roe]);
  const excluded = overlay ?? serverExcluded;
  // 摘除确认弹窗(P4-45):pending = {asset, impact} 影响预览;null 关闭。
  const [pending, setPending] = React.useState(null);

  const submit = async (asset, include, next, prevSet) => {
    // 网络/服务端失败统一走回弹(postJSON 在网络不通时抛异常,一并兜住)。
    let ok = false;
    let body = null;
    try {
      ({ ok, body } = await postJSON(`/api/tasks/${taskID}/roe/assets`, {
        excluded: [...next],
      }));
    } catch {
      ok = false;
    }
    if (!ok) {
      // 失败回弹:还原到本次点击前的呈现;授权范围未被改动。
      setOverlay(sameIDSet(prevSet, serverExcluded) ? null : prevSet);
      toast.error(`未生效:${body?.error || "网络未连通"},已还原勾选`);
      return;
    }
    if (include) {
      toast.success(`已纳入授权:${asset.title}`);
      return;
    }
    const units = body?.stopped_units || 0;
    const steps = body?.stopped_steps || 0;
    toast.success(
      units > 0 || steps > 0
        ? `已摘除:${asset.title},终止 ${units} 个在途单位,取消 ${steps} 个排队步骤`
        : `已摘除:${asset.title},无在途单位与排队步骤`
    );
  };

  // 勾上(纳入)点下即翻、直接提交;取消勾(摘除)先取影响预览再弹
  // 确认——确认按下时乐观翻勾并后台提交(确认才是决策时刻)。
  const toggle = async (asset, include) => {
    const prevSet = excluded;
    const next = new Set(prevSet);
    if (include) {
      next.delete(asset.id);
      setOverlay(next);
      await submit(asset, true, next, prevSet);
      return;
    }
    const r = await fetchJSON(
      `/api/tasks/${taskID}/roe/assets/impact?assets=${encodeURIComponent(asset.id)}`
    );
    if (!r.ok) {
      toast.error(`读取影响失败:${r.body?.error || "原因未说明"}`);
      return;
    }
    next.add(asset.id);
    setPending({
      asset,
      next,
      prevSet,
      units: r.body?.units?.length || 0,
      steps: r.body?.queued?.length || 0,
    });
  };

  const excludedCount = assets.filter((a) => excluded.has(a.id)).length;

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-5">
        <SectionHeader title="资产范围">
          <span className="text-xs text-muted-foreground">
            默认全部纳入授权;摘除即终止覆盖它的在途工作,不再派发
          </span>
          <span className="ml-auto num text-xs text-muted-foreground">
            摘除 {excludedCount}/{assets.length}
          </span>
        </SectionHeader>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {assets.length === 0 ? (
          <div className="rounded-lg border border-dashed py-5 text-center text-sm text-muted-foreground">
            无资产 · 资产页可手动补入,或等虫群发现回传
          </div>
        ) : (
          <div className="flex max-h-56 flex-col gap-1 overflow-y-auto pr-1">
            {assets.map((a) => (
              <label
                key={a.id}
                className="flex items-center gap-2 rounded-md px-2 py-1 text-sm transition-colors duration-150 hover:bg-hover-lift cursor-pointer"
              >
                <Checkbox
                  checked={!excluded.has(a.id)}
                  onCheckedChange={(v) => toggle(a, Boolean(v))}
                />
                <span className="min-w-0 flex-1 truncate num text-xs" title={a.title}>
                  {a.title}
                </span>
                <Badge variant="outline">{assetClassMeta(a.class).label}</Badge>
                <span title={`${assetSourceMeta(a.source).label}资产`}>
                  {React.createElement(iconOf(assetSourceMeta(a.source).icon), {
                    className: "size-3.5 text-muted-foreground",
                  })}
                </span>
                {excluded.has(a.id) && <Badge variant="outline" className="text-muted-foreground">范围外</Badge>}
              </label>
            ))}
          </div>
        )}
        <ConfirmRemoveDialog
          pending={pending}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            // 确认即决策时刻:关弹窗、乐观翻勾、后台提交(P4-47 B ①)。
            if (!pending) return;
            setPending(null);
            setOverlay(pending.next);
            submit(pending.asset, false, pending.next, pending.prevSet);
          }}
        />
      </CardContent>
    </Card>
  );
}

// ConfirmRemoveDialog 摘除确认(P4-45):如实告知将终止的在途单位与排队
// 步骤笔数(0 个也如实),确认后才执行止损。确认即翻勾并关窗(P4-47
// B ① 乐观更新,提交转后台)。
function ConfirmRemoveDialog({ pending, onCancel, onConfirm }) {
  const open = !!pending;
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onCancel()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>摘除资产</DialogTitle>
          <DialogDescription>
            {pending
              ? pending.units > 0 || pending.steps > 0
                ? `将摘除「${pending.asset.title}」,终止 ${pending.units} 个在途单位${
                    pending.steps > 0 ? `,取消 ${pending.steps} 个排队步骤` : ""
                  };主宰下一轮不再派发它。`
                : `将摘除「${pending.asset.title}」,无在途单位与排队步骤;主宰下一轮不再派发它。`
              : ""}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            确认摘除
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 审批弹门 ----

function ApprovalSection({ taskID }) {
  // P3-08:审批待决走实时推送驱动刷新(裁决后服务端再推一轮,待决卡
  // 即时消失);推送未连接时按 2s 轮询降级。
  const { data: body, error } = usePolling(`/api/tasks/${taskID}/approvals`, 2000, {
    live: [`approval:${taskID}`],
  });
  const approvals = Array.isArray(body?.pending) ? body.pending : [];
  const records = Array.isArray(body?.records) ? body.records.slice(0, 20) : [];

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-5">
        <SectionHeader title="审批">
          <Badge variant={approvals.length > 0 ? "danger" : "info"}>
            待决 {approvals.length}
          </Badge>
          <span className="ml-auto text-xs text-muted-foreground">
            对话内容不能代替这里的审批
          </span>
        </SectionHeader>
        <p className="text-xs text-muted-foreground">
          达到审批门槛的动作逐次等批准;批准前不执行,等待期计时停走。
          拒绝就终止这个单位,它不换方法重试;主宰会另想办法。
        </p>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {approvals.length === 0 ? (
          <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
            无待审批请求
          </div>
        ) : (
          approvals.map((a) => (
            <ApprovalCard key={a.id} taskID={taskID} approval={a} />
          ))
        )}
        <ApprovalRecords records={records} taskID={taskID} />
      </CardContent>
    </Card>
  );
}

// 审批记录(拒绝线记录可研判后放行,逐次执行、全记账)。
function ApprovalRecords({ records, taskID }) {
  const releasable = records.filter((r) => r.releasable);
  const history = records.filter((r) => !r.releasable);
  return (
    <div className="flex flex-col gap-2 border-t pt-4">
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">审批记录</span>
        {releasable.length > 0 && (
          <Badge variant="danger">可放行 {releasable.length}</Badge>
        )}
      </div>
      {releasable.length === 0 && history.length === 0 ? (
        <div className="text-xs text-muted-foreground">暂无记录</div>
      ) : (
        <>
          {releasable.map((r) => (
            <DeniedCard key={r.request_id} taskID={taskID} rec={r} />
          ))}
          {history.length > 0 && (
            <div className="flex flex-col gap-1">
              {history.slice(0, 8).map((r, i) => (
                <div
                  key={`${r.request_id}-${r.type}-${i}`}
                  className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
                >
                  <span className="num">{r.occurred_at}</span>
                  <span>{APPROVAL_TYPE_LABELS[r.type] || r.type}</span>
                  <span className="num">{r.tool_name}</span>
                  {r.deny_line && <span>({DENY_LINE_LABELS[r.deny_line] || r.deny_line})</span>}
                  {r.wait_millis > 0 && <span>等待 {(r.wait_millis / 1000).toFixed(1)} 秒</span>}
                  {r.reason && (
                    <LongText
                      text={r.reason}
                      max={40}
                      variant="dialog"
                      label={r.type === "denied" ? "拒绝原因" : "备注"}
                      className="max-w-80 text-xs text-muted-foreground"
                    />
                  )}
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// 拒绝线记录卡:直接拒绝的非常严重动作,造物主研判后可逐次放行。
function DeniedCard({ taskID, rec }) {
  const [busy, setBusy] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  const [reason, setReason] = React.useState("");

  const release = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(
      `/api/tasks/${taskID}/approvals/${rec.request_id}/release`,
      { reason: reason.trim() }
    );
    setBusy(false);
    if (!ok) {
      toast.error(`放行失败:${body.error || "原因未说明"}`);
      return;
    }
    setConfirmOpen(false);
    toast.success("已放行并执行一次(全记账)");
  };

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-status-danger/40 p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge variant="danger">{DENY_LINE_LABELS[rec.deny_line] || "拒绝线"}</Badge>
        {rec.harm_class && (
          <Badge variant="outline">{HARM_CLASS_LABELS[rec.harm_class] || rec.harm_class}</Badge>
        )}
        <span className="num">{rec.tool_name}</span>
        <span className="text-xs text-muted-foreground">
          步骤 {rec.step_id} · {rec.occurred_at}
        </span>
      </div>
      {rec.intent && (
        <div className="text-xs text-muted-foreground">
          意图:<LongText text={rec.intent} max={120} label="意图原文" className="align-baseline" />
        </div>
      )}
      <div className="text-xs text-muted-foreground">
        拒绝原因:<LongText text={rec.reason} max={120} label="拒绝原因" className="align-baseline" />
      </div>
      <div className="text-xs">
        <LongText text={rec.tool_args} max={200} mono label="参数原文" className="align-baseline" />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => setConfirmOpen(true)}>
          规划后放行
        </Button>
        <span className="text-xs text-muted-foreground">放行按上方参数执行一次,逐次放行不累计授权</span>
      </div>
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>规划后放行</DialogTitle>
            <DialogDescription>
              将按记录在案的参数执行一次 {rec.tool_name} 调用,执行结果与本次放行全部记账;
              被终止的单位不恢复。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <span className="text-sm text-muted-foreground">放行理由(记账)</span>
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="为何放行这次动作"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              取消
            </Button>
            <Button variant="destructive" onClick={release} disabled={busy}>
              确认放行
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ApprovalCard({ taskID, approval }) {
  const [reason, setReason] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const decide = async (approved) => {
    setBusy(true);
    const { ok, body } = await postJSON(
      `/api/tasks/${taskID}/approvals/${approval.id}`,
      { approved, reason: reason.trim() }
    );
    setBusy(false);
    if (!ok) {
      toast.error(`审批失败:${body.error || "原因未说明"}`);
      return;
    }
    toast.success(approved ? "已批准(已记账)" : "已拒绝(已记账)");
  };

  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3 transition-colors duration-150 hover:bg-hover-lift">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge variant={levelVariant(approval.action_level)}>
          {approval.action_level}
        </Badge>
        <span className="num">{approval.tool_name}</span>
        <span className="text-xs text-muted-foreground">
          步骤 {approval.step_id} · 单位 {approval.unit_id} · {approval.created_at}
        </span>
      </div>
      <div className="text-xs text-muted-foreground">
        意图:<LongText text={approval.intent} max={120} label="意图原文" className="align-baseline" />
      </div>
      {/* 危害说明三段式(P4-27 ⑨):要做什么/目标会怎样/最坏后果,
          确定性模板拼装——批准后果先说清,不用猜 */}
      {approval.harm_note && (
        <div className="text-sm leading-relaxed">{approval.harm_note}</div>
      )}
      {approval.note && (
        <div className="text-xs text-status-warning">
          <LongText text={approval.note} max={160} label="拦截说明" className="align-baseline" />
        </div>
      )}
      <div className="text-xs">
        <LongText text={approval.tool_args} max={200} mono label="参数原文" className="align-baseline" />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="w-64"
          placeholder="审批理由(随审批记录)"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <Button size="sm" variant="outline" disabled={busy} onClick={() => decide(true)}>
          批准
        </Button>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => decide(false)}>
          拒绝
        </Button>
      </div>
    </div>
  );
}

// ---- 操作约束 ----

function ConstraintSection({ taskID }) {
  const { data: roe, error } = usePolling(`/api/tasks/${taskID}/roe`, 2000);
  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-5">
        <SectionHeader
          title={
            <span title={`版本号 ${roe?.updated_rev ?? "—"}`}>操作约束</span>
          }
          tail={<ConstraintDialog taskID={taskID} roe={roe} />}
        />
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {!roe ? (
          <div className="text-sm text-muted-foreground">读取中…</div>
        ) : (
          <div className="flex flex-col gap-2 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-muted-foreground">审批门槛</span>
              <Badge variant={levelVariant(roe.approval_threshold)}>
                {roe.approval_threshold}
              </Badge>
              <span className="text-xs text-muted-foreground">
                {APPROVAL_THRESHOLD_HINT[roe.approval_threshold] || "该级及以上需批准"}
              </span>
            </div>
            <div className="text-xs text-muted-foreground">
              数据销毁与拒绝服务类动作不进审批等待,直接拒绝并留在审批记录,可规划后放行。
            </div>
            <ConstraintRow label="禁止工具" values={roe.denied_tools} empty="无" />
            <ConstraintRow label="仅允许工具" values={roe.allowed_tools} empty="不设白名单" />
            {/* 注意事项(P4-49):对做法的软要求,建后在变更约束弹窗改,
                下一轮生效 */}
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="text-muted-foreground">注意事项</span>
              {roe.creator_notes ? (
                <span className="min-w-0 whitespace-pre-line text-sm">
                  {roe.creator_notes}
                </span>
              ) : (
                <span className="text-xs text-muted-foreground">无</span>
              )}
            </div>
            {roe.note && (
              <div className="text-xs text-muted-foreground">最近变更说明:{roe.note}</div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// IntentRulesCard 任务级拦截规则(独立分区,审核迭代二轮:自操作约束
// 卡拆出——规则与门槛/工具边界是两类约束,分卡呈现)。逐条增删改 +
// 自然语言录入;保存即生效并定向告知受影响的在跑单位。
function IntentRulesCard({ taskID }) {
  const { data: roe, error } = usePolling(`/api/tasks/${taskID}/roe`, 2000);
  const { data: assetData } = usePolling(`/api/tasks/${taskID}/assets`, 10000);
  const assets = Array.isArray(assetData?.assets) ? assetData.assets : [];
  const [editRule, setEditRule] = React.useState(null); // null=关闭;{}=新建;rule=编辑
  const [deleteRule, setDeleteRule] = React.useState(null);
  const list = (roe && roe.intent_rules) || [];

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-5">
        <SectionHeader
          title="任务级拦截"
          tail={
            <Button size="sm" variant="outline" onClick={() => setEditRule({})}>
              新增规则
            </Button>
          }
        >
          <span className="text-xs text-muted-foreground">
            按资产与动作拦,变更即生效并告知受影响的在跑单位
          </span>
        </SectionHeader>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {!roe ? (
          <div className="text-sm text-muted-foreground">读取中…</div>
        ) : list.length === 0 ? (
          <div className="rounded-lg border border-dashed py-5 text-center text-sm text-muted-foreground">
            无规则 · 对指定资产禁止或放行某类动作,次数不填即不限
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {list.map((r) => (
              <div
                key={r.id}
                className="flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors duration-150 hover:bg-hover-lift"
              >
                <Badge variant={r.disposition === "forbid" ? "danger" : "success"}>
                  {r.disposition === "forbid" ? "禁止" : "允许"}
                </Badge>
                <span className="min-w-0 max-w-72 truncate font-medium" title={r.action}>
                  {r.action}
                </span>
                <span className="text-muted-foreground">
                  {r.asset_titles && r.asset_titles.length > 0
                    ? r.asset_titles.join("、")
                    : "全部资产"}
                </span>
                <span className="text-xs text-muted-foreground">
                  {r.disposition === "allow"
                    ? r.quota > 0
                      ? `次数上限 ${r.quota},已用 ${r.used}`
                      : "不限次数"
                    : ""}
                </span>
                {r.note && (
                  <span
                    className="hidden truncate text-xs text-muted-foreground md:inline md:max-w-48"
                    title={r.note}
                  >
                    {r.note}
                  </span>
                )}
                <div className="ml-auto flex items-center gap-1">
                  <Button size="sm" variant="ghost" className="h-8 px-2 text-xs" onClick={() => setEditRule(r)}>
                    编辑
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-8 px-2 text-xs text-status-danger"
                    onClick={() => setDeleteRule(r)}
                  >
                    删除
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
        {editRule !== null && (
          <RoERuleDialog
            taskID={taskID}
            assets={assets}
            rule={editRule.id ? editRule : null}
            onClose={() => setEditRule(null)}
          />
        )}
        <ConfirmRuleDeleteDialog taskID={taskID} rule={deleteRule} onClose={() => setDeleteRule(null)} />
      </CardContent>
    </Card>
  );
}

function ConstraintRow({ label, values, empty }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-muted-foreground">{label}</span>
      {!values || values.length === 0 ? (
        <span className="text-xs text-muted-foreground">{empty}</span>
      ) : (
        values.map((v) => (
          <span
            key={v}
            className="rounded-md border border-border/55 bg-muted px-2 py-0.5 num text-xs"
          >
            {v}
          </span>
        ))
      )}
    </div>
  );
}

// ConstraintDialog 变更操作约束(按钮挂操作约束卡头,08e 挂账落地)。
function ConstraintDialog({ taskID, roe }) {
  const [open, setOpen] = React.useState(false);
  const [form, setForm] = React.useState(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (open && roe && !form) {
      setForm({
        denied: (roe.denied_tools || []).join(", "),
        allowed: (roe.allowed_tools || []).join(", "),
        threshold: roe.approval_threshold || "修改与持久化",
        notes: roe.creator_notes || "",
        note: "",
      });
    }
    if (!open) setForm(null);
  }, [open, roe, form]);

  const dirty = (() => {
    if (!form || !roe) return false;
    return (
      form.denied !== (roe.denied_tools || []).join(", ") ||
      form.allowed !== (roe.allowed_tools || []).join(", ") ||
      form.threshold !== roe.approval_threshold ||
      form.notes.trim() !== (roe.creator_notes || "") ||
      form.note.trim() !== ""
    );
  })();

  const submit = async () => {
    const csv = (s) =>
      s.split(/[,，]/).map((x) => x.trim()).filter(Boolean);
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/roe`, {
      denied_tools: csv(form.denied),
      allowed_tools: csv(form.allowed),
      approval_threshold: form.threshold,
      // 注意事项显式提交(空 = 清空);任务级规则有独立端点(逐条增删改),
      // 快照提交时透传当前值不动它。
      creator_notes: form.notes.trim(),
      intent_rules: (roe.intent_rules || []).map((r) => ({
        id: r.id,
        action: r.action,
        asset_ids: r.asset_ids || [],
        disposition: r.disposition,
        quota: r.quota || 0,
        note: r.note || "",
      })),
      note: form.note.trim(),
    });
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${body.error || "原因未说明"}`);
      return;
    }
    setOpen(false);
    toast.success("约束已记账,下一轮生效(不打断运行中的虫群单位)");
  };

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        变更约束
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[88vh] w-[min(620px,94vw)] max-w-none gap-4 overflow-y-auto p-6">
          <DialogHeader>
            <DialogTitle>变更操作约束</DialogTitle>
            <DialogDescription>
              提交后整体覆盖当前约束,全部记账,下一轮生效;门槛收紧后,
              执行中的单位维持领取时门槛。任务级拦截规则在下方单独增删改。
            </DialogDescription>
          </DialogHeader>
          {!form ? (
            <div className="text-sm text-muted-foreground">读取当前约束…</div>
          ) : (
            <div className="flex flex-col gap-4">
              <Field label="禁止工具">
                <Input
                  className={`${INPUT} num text-xs`}
                  placeholder="逗号分隔,如: bash, port_probe"
                  value={form.denied}
                  onChange={(e) => setForm({ ...form, denied: e.target.value })}
                />
              </Field>
              <Field label="仅允许工具">
                <Input
                  className={`${INPUT} num text-xs`}
                  placeholder="留空 = 不设白名单"
                  value={form.allowed}
                  onChange={(e) => setForm({ ...form, allowed: e.target.value })}
                />
              </Field>
              <Field label="审批门槛">
                <Select
                  value={form.threshold}
                  onValueChange={(v) => setForm({ ...form, threshold: v })}
                >
                  <SelectTrigger className={INPUT}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {APPROVAL_THRESHOLD_OPTIONS.map((o) => (
                      <SelectItem key={o.value} value={o.value}>
                        {o.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="text-xs text-muted-foreground">该级及以上需批准</span>
              </Field>
              <Field label="注意事项" className="items-start" labelClassName="mt-2">
                <div className="flex min-w-0 flex-1 flex-col gap-1">
                  <Textarea
                    rows={2}
                    className="min-w-0 flex-1 resize-none bg-input-surface"
                    value={form.notes}
                    onChange={(e) => setForm({ ...form, notes: e.target.value })}
                    placeholder="如：用小字典，不要用大字典；不要修改任何数据"
                  />
                  <span className="text-xs text-muted-foreground">
                    对做法的要求,每行一条;清空即删除,下一轮生效
                  </span>
                </div>
              </Field>
              <Field label="变更说明">
                <Input
                  className={INPUT}
                  value={form.note}
                  onChange={(e) => setForm({ ...form, note: e.target.value })}
                  placeholder="记账:谁、为何"
                />
              </Field>
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button onClick={submit} disabled={busy || !dirty}>
              提交约束
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// RoERuleDialog 单条任务级规则弹窗(08e 挂账①:处置/范围/动作描述首屏
// 一眼可填,匹配正则与说明收「高级选项」;自然语言与表单合并、动作
// 不限词表)。
function RoERuleDialog({ taskID, assets, rule, onClose }) {
  const editing = !!rule?.id;
  const [form, setForm] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [advanced, setAdvanced] = React.useState(false);

  React.useEffect(() => {
    setForm({
      action: rule?.action || "",
      asset_ids: rule?.asset_ids || [],
      disposition: rule?.disposition || "forbid",
      quota: rule?.quota || 0,
      patterns: (rule?.patterns || []).join("\n"),
      note: rule?.note || "",
    });
    setAdvanced(false);
  }, [rule]);

  const dirty = (() => {
    if (!form) return false;
    if (!rule) return form.action.trim() !== "";
    return (
      form.action !== rule.action ||
      form.disposition !== rule.disposition ||
      form.quota !== (rule.quota || 0) ||
      form.asset_ids.join(",") !== (rule.asset_ids || []).join(",") ||
      form.patterns !== (rule.patterns || []).join("\n") ||
      form.note !== (rule.note || "")
    );
  })();

  const generate = async () => {
    if (!form.action.trim()) {
      toast.error("先写动作描述,再生成匹配模式");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/roe/rules/patterns`, {
      text: form.action.trim(),
    });
    setBusy(false);
    if (!ok) {
      toast.error(body.error || "原因未说明");
      return;
    }
    const got = Array.isArray(body.patterns) ? body.patterns : [];
    if (got.length === 0) {
      toast.success("没生成出可用的模式;可手写,或把描述改成内置动作词(上传/爆破等)");
      return;
    }
    setForm({ ...form, patterns: got.join("\n") });
    toast.success(`生成 ${got.length} 条,可直接改`);
  };

  const submit = async () => {
    setBusy(true);
    const url = editing
      ? `/api/tasks/${taskID}/roe/rules/${rule.id}`
      : `/api/tasks/${taskID}/roe/rules`;
    const patterns = form.patterns
      .split("\n")
      .map((x) => x.trim())
      .filter(Boolean);
    const { ok, body } = await postJSON(url, {
      action: form.action.trim(),
      asset_ids: form.asset_ids,
      disposition: form.disposition,
      quota: form.disposition === "allow" ? form.quota || 0 : 0,
      patterns,
      note: form.note,
    });
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${body.error || "原因未说明"}`);
      return;
    }
    onClose();
    toast.success(editing ? "规则已变更,即刻生效并告知受影响单位" : "规则已新增,即刻生效并告知受影响单位");
  };

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[88vh] w-[min(620px,94vw)] max-w-none gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>
            {editing ? "编辑任务级规则" : "新增任务级规则"}
          </DialogTitle>
          <DialogDescription>
            保存即生效;只对范围内资产的单位起作用,受影响的在跑单位会收到告知
          </DialogDescription>
        </DialogHeader>
        {!form ? (
          <div className="text-sm text-muted-foreground">读取中…</div>
        ) : (
          <div className="flex flex-col gap-4">
            <Field label="处置">
              <Select
                value={form.disposition}
                onValueChange={(v) => setForm({ ...form, disposition: v, quota: v === "forbid" ? 0 : form.quota })}
              >
                <SelectTrigger className="w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="forbid">禁止</SelectItem>
                  <SelectItem value="allow">允许</SelectItem>
                </SelectContent>
              </Select>
              {form.disposition === "allow" && (
                <>
                  <Input
                    className="h-9 w-28 text-right num text-xs"
                    inputMode="numeric"
                    value={form.quota}
                    onChange={(e) => {
                      const n = Number(e.target.value.replace(/[^0-9]/g, ""));
                      setForm({ ...form, quota: Number.isFinite(n) ? n : 0 });
                    }}
                    placeholder="0"
                  />
                  <span className="text-xs text-muted-foreground">次数上限,0 不限</span>
                </>
              )}
            </Field>
            <Field label="范围" className="items-start" labelClassName="mt-2">
              <div className="flex min-w-0 flex-1 flex-wrap gap-2">
                <span className="w-full text-xs text-muted-foreground">不勾 = 全部资产</span>
                {assets.map((a) => (
                  <label
                    key={a.id}
                    className="flex items-center gap-2 rounded-md border px-2 py-1 text-xs transition-colors duration-150 hover:bg-hover-lift"
                  >
                    <Checkbox
                      checked={form.asset_ids.includes(a.id)}
                      onCheckedChange={(v) => {
                        const ids = v
                          ? [...form.asset_ids, a.id]
                          : form.asset_ids.filter((x) => x !== a.id);
                        setForm({ ...form, asset_ids: ids });
                      }}
                    />
                    {a.title}
                  </label>
                ))}
              </div>
            </Field>
            <Field label="动作描述" className="items-start" labelClassName="mt-2">
              <Textarea
                rows={2}
                className="min-w-0 flex-1 resize-none bg-input-surface"
                value={form.action}
                onChange={(e) => setForm({ ...form, action: e.target.value })}
                placeholder="如:上传文件到目标;对后台登录口做暴力破解;修改配置文件"
              />
            </Field>

            {/* 高级选项折叠:横线 — 箭头 — 文字 — 横线 */}
            <Fold open={advanced} onToggle={() => setAdvanced((v) => !v)}>
              高级选项
            </Fold>
            {advanced && (
              <div className="flex animate-rise flex-col gap-4">
                <Field label="匹配模式" className="items-start" labelClassName="mt-2">
                  <div className="flex min-w-0 flex-1 flex-col gap-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-8 px-2 text-xs"
                        onClick={generate}
                        disabled={busy}
                      >
                        从描述生成
                      </Button>
                      <span className="text-xs text-muted-foreground">
                        判定按这些正则;不填且描述是内置动作词(上传/爆破等)时按词判
                      </span>
                    </div>
                    <Textarea
                      rows={4}
                      className="resize-none bg-input-surface num text-xs"
                      value={form.patterns}
                      onChange={(e) => setForm({ ...form, patterns: e.target.value })}
                      placeholder={"每行一条正则,如:\\bcurl\\b[^|;&]*(\\s-T\\b|--upload-file)"}
                    />
                  </div>
                </Field>
                <Field label="说明">
                  <Input
                    className={INPUT}
                    value={form.note}
                    onChange={(e) => setForm({ ...form, note: e.target.value })}
                    placeholder="一句话备注,随规则留痕"
                  />
                </Field>
              </div>
            )}
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !dirty || !form?.action?.trim()}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ConfirmRuleDeleteDialog 删除确认(逐条端点)。
function ConfirmRuleDeleteDialog({ taskID, rule, onClose }) {
  const [busy, setBusy] = React.useState(false);
  const open = !!rule;

  const remove = async () => {
    setBusy(true);
    const r = await fetch(`/api/tasks/${taskID}/roe/rules/${rule.id}`, { method: "DELETE" });
    const body = await r.json().catch(() => ({}));
    setBusy(false);
    if (!r.ok) {
      toast.error(`删除失败:${body.error || "原因未说明"}`);
      return;
    }
    onClose();
    toast.success("规则已解除,即刻生效并告知受影响单位");
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>删除任务级规则</DialogTitle>
          <DialogDescription>
            删除「{rule?.action} · {rule?.asset_titles?.join("、") || "全部资产"}
            {rule?.disposition === "forbid" ? " · 禁止" : rule?.quota ? ` · 允许 ≤${rule.quota} 次` : " · 允许"}
            」后不再拦截,受影响的在跑单位会收到解除告知。
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button variant="destructive" onClick={remove} disabled={busy}>
            确认删除
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 目标管理 ----

function GoalSection({ taskID }) {
  const { data, error } = usePolling(`/api/tasks/${taskID}/goals`, 2000);
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const goals = Array.isArray(data) ? data : [];

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-5">
        <SectionHeader
          title="目标管理"
          tail={
            <Button size="sm" variant="outline" onClick={() => setDialogOpen(true)}>
              新增目标
            </Button>
          }
        >
          <span className="text-xs text-muted-foreground">
            完成条件创建时锁定,达成与否由仲裁者自动判定
          </span>
        </SectionHeader>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {goals.length === 0 ? (
          <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
            尚无目标 · 主宰按进行中目标拆解派发
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {goals.map((g) => (
              <div key={g.id} className="flex flex-wrap items-center gap-2 text-sm">
                <Badge variant={GOAL_STATUS[g.status]?.variant || "info"}>
                  {GOAL_STATUS[g.status]?.label || g.status}
                </Badge>
                <span className="num text-xs text-muted-foreground">{g.id}</span>
                <span>{g.title}</span>
                {g.predicate && (
                  <span className="text-xs text-muted-foreground">条件已锁定</span>
                )}
              </div>
            ))}
          </div>
        )}
        <GoalDialog taskID={taskID} open={dialogOpen} onOpenChange={setDialogOpen} />
      </CardContent>
    </Card>
  );
}

function GoalDialog({ taskID, open, onOpenChange }) {
  const [title, setTitle] = React.useState("");
  const [predicate, setPredicate] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [advanced, setAdvanced] = React.useState(false);

  React.useEffect(() => {
    if (!open) {
      setTitle("");
      setPredicate("");
      setAdvanced(false);
    }
  }, [open]);

  const submit = async () => {
    if (!title.trim()) {
      toast.error("目标标题不能为空");
      return;
    }
    let payload;
    if (predicate.trim()) {
      try {
        payload = JSON.parse(predicate);
      } catch {
        toast.error("完成条件不是合法 JSON");
        return;
      }
    }
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/goals`, {
      title: title.trim(),
      predicate: payload,
    });
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success("目标已入图并唤醒主宰");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] w-[min(620px,94vw)] max-w-none gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>新增目标</DialogTitle>
          <DialogDescription>
            目标是最终可核验对象;完成条件(可选)创建时锁定。
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <Field label="目标标题">
            <Input
              className={INPUT}
              placeholder="最终可核验目标"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </Field>

          {/* 高级选项折叠:横线 — 箭头 — 文字 — 横线 */}
          <Fold open={advanced} onToggle={() => setAdvanced((v) => !v)}>
            高级选项
          </Fold>
          {advanced && (
            <div className="flex animate-rise flex-col gap-4">
              <Field label="完成条件" className="items-start" labelClassName="mt-2">
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  <span className="text-xs text-muted-foreground">
                    JSON 格式,创建时锁定,格式非法直接拒绝
                  </span>
                  <Textarea
                    rows={4}
                    className="resize-none bg-input-surface num text-xs"
                    placeholder={'如 {"op":"all_of","conditions":[{"kind":"fact_confirmed","asset_id":"a1","min":1}]}'}
                    value={predicate}
                    onChange={(e) => setPredicate(e.target.value)}
                  />
                </div>
              </Field>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !title.trim()}>
            新增目标
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";

import { useLive } from "@/lib/live";

// ---- 基础请求 ----

// fetchJSON:统一入口。会话失效(401,P3-09 认证)整页重载——服务端对
// 未登录的页面请求回登录页,重载即回到登录入口;登录 Cookie 由浏览器
// 同源自动携带,此处无需附加凭据。
export async function fetchJSON(url, opts) {
  const r = await fetch(url, opts);
  if (r.status === 401 && !url.startsWith("/api/auth/")) {
    location.reload();
    return { ok: false, status: 401, body: {} };
  }
  const body = await r.json().catch(() => ({}));
  return { ok: r.ok, status: r.status, body };
}

export function postJSON(url, payload) {
  return fetchJSON(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload ?? {}),
  });
}

// usePolling:interval 毫秒轮询一个 GET 端点,immediate 立即拉一次。
// 返回 { data, error, setData }:setData 供写操作后即时回填(不等下一拍)。
// interval 可为函数(入参为最近一次响应,返回毫秒)——P3-05c ⑤ 中间态
// 反馈用:按响应态自适应轮询节奏(如牧者回答期间 700ms、平时 2000ms)。
// live(P3-08 实时推送):主题数组——实时推送已连接时事件驱动刷新
// (150ms 去抖合并突发)并停轮询;未连接/断线时按 interval 轮询降级,
// 重连后状态翻转会自动重拉一次(断线窗口的差额由此补齐)。
export function usePolling(url, interval = 2000, { immediate = true, live = null } = {}) {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const alive = useRef(false);
  const last = useRef(null); // 最近一次响应(函数式 interval 的输入)
  const { status: liveStatus, subscribe } = useLive();
  const topics = Array.isArray(live) ? live.filter(Boolean) : null;
  const topicKey = topics ? topics.join("\n") : "";
  const iv = typeof interval === "function" ? interval(last.current) || 2000 : interval;

  useEffect(() => {
    alive.current = true;
    let timer = null;
    let debounce = null;
    const poll = async () => {
      try {
        const r = await fetch(url);
        if (alive.current && r.status === 401) {
          // 会话失效(P3-09):重载回登录页,轮询自然停止。
          location.reload();
          return;
        }
        const body = await r.json().catch(() => null);
        if (alive.current && r.ok) {
          last.current = body;
          setData(body);
          setError(null);
        } else if (alive.current && !r.ok) {
          setError(body?.error || `HTTP ${r.status}`);
        }
      } catch {
        if (alive.current) setError("网络未连通");
      }
    };
    if (immediate) poll();
    let unsub = () => {};
    if (topics && topics.length && topicKey) {
      unsub = subscribe(topics, () => {
        clearTimeout(debounce);
        debounce = setTimeout(() => {
          if (alive.current) poll();
        }, 150);
      });
    }
    // 轮询降级:未订阅实时主题,或实时推送未连接时按节奏轮询。
    if (!topics || !topics.length || liveStatus !== "open") {
      timer = setInterval(poll, iv);
    }
    return () => {
      alive.current = false;
      clearInterval(timer);
      clearTimeout(debounce);
      unsub();
    };
  }, [url, iv, immediate, topicKey, liveStatus, subscribe]);

  return { data, error, setData };
}

// ---- 任务上下文(全局唯一"当前任务") ----

const TaskContext = createContext(null);

export function TaskContextProvider({ children, initial }) {
  const [taskID, setTaskID] = useState(initial ?? null);
  // 切换持久化:刷新后保持语境(localStorage)。
  useEffect(() => {
    if (taskID) localStorage.setItem("gr.currentTask", taskID);
  }, [taskID]);
  const value = {
    taskID,
    setTaskID,
    // 当前选中任务不存在(被删/来自旧库)时由壳层重置为 null。
    clearIfGone(id) {
      if (taskID && taskID === id) setTaskID(null);
    },
  };
  return <TaskContext.Provider value={value}>{children}</TaskContext.Provider>;
}

export function useTaskContext() {
  return useContext(TaskContext);
}

// ---- 标签词汇(与后端词汇表对应,面板侧仅做展示映射) ----

// 任务状态显示取态(P4-20 起,P4-27 ② 全状态矩阵绝对单显)为纯显示
// 逻辑,实现独立于数据层在 task-status.js(测试脚本直引做 36 格矩阵
// 断言);此处再导出,全站消费点不变。
export {
  TASK_STATUS,
  TASK_BLOCKED_HINT,
  taskActiveStatus,
  taskFrozenStatus,
  taskStatusBadge,
  taskStopWay,
} from "./task-status.mjs";

// 发现(TestClaim)状态 → 徽标变体。P4-28 ⑥ 显示层合并 12→6:过程态
// candidate/testing/awaiting_round 合并显示"锤炼中"(hint 悬停细分当前
// 所处阶段),出结果才显示已确认/待确认;fixed 为 P3-06 复测确认已修复
// (锤炼态之上的修复标记,成果与报告不消失)。live 过程态徽标带呼吸点
// 动画(gr-live),终态静态。与 rings.ClaimStatusWord 同词同口径。
export const CLAIM_STATUS = {
  candidate: {
    label: "锤炼中", variant: "running", live: true,
    hint: "锤炼中(待验证)：已提交，等待安排独立单位反证",
  },
  testing: {
    label: "锤炼中", variant: "running", live: true,
    hint: "锤炼中(反证中)：独立单位正在反证",
  },
  awaiting_round: {
    label: "锤炼中", variant: "running", live: true,
    hint: "锤炼中(待下轮反证)：本轮疑点未排完，等下一轮反证",
  },
  confirmed: {
    label: "已确认", variant: "success",
    hint: "反证未推翻且证据充分，判定成立",
  },
  pending: {
    label: "待确认", variant: "warning",
    hint: "暂不成立挂起，有新证据自动重开，无需人工操作",
  },
  fixed: {
    label: "已修复", variant: "success",
    hint: "复测确认漏洞已不可触发，发现记录保留",
  },
};

// 复测结论(P3-06):修复状态轴三值,如实呈现(P4-28 ② 悬停人话)。
export const RETEST_VERDICT = {
  fixed: { label: "已修复", variant: "success", hint: "复测重放显示漏洞不再可触发" },
  unfixed: { label: "未修复", variant: "danger", hint: "复测显示漏洞仍可触发" },
  inconclusive: { label: "未能定论", variant: "warning", hint: "复测未能判定，如目标不可达" },
};

// 重放校验结论(P3-07):绑定流量证据原样重放后的确定性比对——只陈述
// 响应是否一致,是否已修复的判定归复测。
export const REPLAY_VERDICT = {
  reproduced: { label: "重放一致", variant: "success" },
  diverged: { label: "重放有变化", variant: "warning" },
  unreachable: { label: "重放不可达", variant: "danger" },
};

// 假设(Hypothesis)状态 → 徽标变体(P2-03 直觉回流:候选 → 主宰评估
// 三去向 → 验证结论;挂账 = 存疑留在图上待唤醒,已废弃 = 有理由的废案)。
// P4-28 ⑥:过程态 candidate/testing 合并显示"锤炼中"(hint 细分)。
export const HYP_STATUS = {
  candidate: {
    label: "锤炼中", variant: "running", live: true,
    hint: "锤炼中(待验证)：已提交，等待主宰评估",
  },
  testing: {
    label: "锤炼中", variant: "running", live: true,
    hint: "锤炼中(验证中)：独立单位正在验证",
  },
  held: {
    label: "搁置", variant: "paused",
    hint: "评估存疑暂留，唤醒后重新评估",
  },
  confirmed: {
    label: "成立", variant: "success",
    hint: "验证取得支持证据，猜测成立",
  },
  discarded: {
    label: "已废弃", variant: "danger",
    hint: "已判不成立，理由留档不删除",
  },
};

// 假设评估三去向(主宰检查归纳后的判定)。
export const HYP_DECISION = {
  verify: { label: "转验证", tone: "running" },
  hold: { label: "搁置", tone: "warning" },
  discard: { label: "废弃", tone: "danger" },
};

// 假设验证结论(验证单位结构化写回;variant 供 StatusBadge,P4-28 ②
// 悬停人话)。
export const HYP_VERDICT = {
  supported: { label: "成立", variant: "success", hint: "验证取得支持证据，猜测成立" },
  refuted: { label: "不成立", variant: "danger", hint: "验证推翻了这条猜测" },
  inconclusive: { label: "未能定论", variant: "warning", hint: "验证过但证据不足，可再验" },
};

// 严重级别(critical/high/medium/low)→ 徽标变体。级别只来自反证轮的
// 影响面证据(检查归纳定稿),发现者无级别通道(两维判断定稿,P2-02)。
export const SEVERITY = ["critical", "high", "medium", "low"];

export function severityMeta(level) {
  const labels = { critical: "Critical", high: "High", medium: "Medium", low: "Low" };
  if (!level || !labels[level]) return null;
  return { value: level, label: labels[level], variant: level };
}

// 目标状态 → 徽标变体。
export const GOAL_STATUS = {
  active: { label: "进行中", variant: "running" },
  achieved: { label: "已达成", variant: "success" },
};

// 意图分级(Q8 四级)→ 徽标变体。
export const ACTION_LEVELS = [
  { value: "只读探测", variant: "info" },
  { value: "主动测试", variant: "medium" },
  { value: "利用深入", variant: "high" },
  { value: "修改与持久化", variant: "critical" },
];

export function levelVariant(level) {
  const hit = ACTION_LEVELS.find((l) => l.value === level);
  return hit ? hit.variant : "info";
}

// 审批门槛选项(P2-06b:该级及以上的意图需批准;默认宽=仅增删改需批准)。
export const APPROVAL_THRESHOLD_OPTIONS = [
  { value: "修改与持久化", label: "仅增删改需批准(默认)" },
  { value: "利用深入", label: "利用深入及以上需批准" },
  { value: "主动测试", label: "主动测试及以上需批准" },
  { value: "只读探测", label: "全部需批准" },
];

// 拒绝线类别 → 操作者语言。
export const DENY_LINE_LABELS = {
  harm: "危害类",
  roe_scope: "超授权范围",
  level_invalid: "分级异常",
  no_gate: "无审批门",
  panel: "面板拒绝",
  closeout: "收尾结案",
  hard_rule: "拦截规则",
  intent_rule: "任务级拦截",
};

// 拒绝来源 → 终态文案(P4-02 ②:approval_denied 步骤按拒绝线区分
// 呈现;硬规则带规则名——从拒绝原因"内置硬规则命中:组 · 名称"段
// 解析)。无拒绝线信息时退统一文案(历史账面)。
const DENY_SOURCE_LABELS = {
  panel: "审批拒绝终止",
  harm: "危害声明拒绝",
  hard_rule: "内置拦截规则拒绝",
  intent_rule: "任务级拦截拒绝",
  roe_scope: "超授权范围拒绝",
  level_invalid: "分级异常拒绝",
  no_gate: "无审批门拒绝",
  closeout: "收尾按拒绝结案",
};

export function denyStatusLabel(unit) {
  if (unit?.status !== "approval_denied") return "审批拒绝终止";
  const line = unit?.deny_line;
  if (!line || !DENY_SOURCE_LABELS[line]) return "审批拒绝终止";
  if (line === "hard_rule") {
    const m = /内置硬规则命中:([^(:·]+·[^(:（]+?)(?:\s*[(（]|$)/.exec(unit.deny_reason || "");
    const rule = m ? m[1].trim() : "";
    return rule ? `内置拦截规则拒绝(${rule})` : "内置拦截规则拒绝";
  }
  return DENY_SOURCE_LABELS[line];
}

// 任务级拦截的内置动作词(rings.IntentAction 词表,P2-08e;审核迭代三
// 起动作不限词表,词表仅作"描述恰为该词时按词判"的兼容路径)。
export const INTENT_ACTIONS = ["数据读取", "注入测试", "上传", "爆破", "爬取", "端口扫描"];

// 硬规则组词表(rings 侧 swarm.HardGroup,P2-08e;P3-05b 增代理旁路)。
export const HARD_RULE_GROUPS = [
  "系统破坏命令",
  "数据库破坏",
  "HTTP 破坏请求",
  "批量清空路径",
  "外泄管道",
  "代理旁路",
];

// 危害声明 → 操作者语言。
export const HARM_CLASS_LABELS = {
  data_destruction: "数据销毁",
  denial_of_service: "拒绝服务类",
};

// 资产 12 类(rings.AssetClass 词汇表):键为落账值,值为面板标签与图标 key。
export const ASSET_CLASSES = [
  ["domain", "域名", "globe"],
  ["subdomain", "子域名", "link"],
  ["host", "主机", "server"],
  ["service", "服务", "plug"],
  ["api", "API 接口", "braces"],
  ["organization", "组织", "building2"],
  ["miniprogram", "小程序", "appWindow"],
  ["app", "应用", "appWindow"],
  ["wechat_account", "微信公众号", "megaphone"],
  ["person", "人员", "user"],
  ["leak_info", "泄露信息", "fileWarning"],
  ["other", "其他", "circleHelp"],
];

export function assetClassMeta(cls) {
  const hit = ASSET_CLASSES.find(([v]) => v === cls);
  return hit ? { value: hit[0], label: hit[1], icon: hit[2] } : { value: cls, label: cls, icon: "circleHelp" };
}

// 资产来源三态(P4-05):seed=种子(建任务表单)/ manual=手动(面板
// 新增资产)/ discovered=发现(虫群回流收割入图)。落账值不上面板,
// 统一经此映射为操作者语言(icon 为图标配号)。
export const ASSET_SOURCES = {
  seed: { label: "种子", icon: "sprout" },
  manual: { label: "手动", icon: "hand" },
  discovered: { label: "发现", icon: "sparkles" },
};

export function assetSourceMeta(source) {
  return ASSET_SOURCES[source] || ASSET_SOURCES.seed;
}

// 任务类型(P4-44):渗透=范围外发现资产待批锁定;红队=直接可测;
// 空=存量任务未标(行为等同红队,徽标如实呈现)。落账值不上面板,
// 统一经此映射为操作者语言。
export const TASK_TYPES = {
  pentest: { label: "渗透任务", hint: "只测填写的域名或 IP;虫群发现的相邻资产待批准才可测" },
  redteam: { label: "红队任务", hint: "虫群发现的相邻资产(同服务器其他端口、兄弟域名等)直接可测" },
};

export function taskTypeMeta(type) {
  return TASK_TYPES[type] || { label: "未标", hint: "旧任务未记录类型:发现的相邻资产直接可测" };
}

// 六类图节点(账本 NodeType,P2-09 进度图/跳转协议共用):标签为操作者
// 语言;icon 为节点视觉语言的图标配号;shape 为 CSS 形状徽记类
//(globals.css,P2-11 年轮图卡片沿用同一套形状语义)。
export const NODE_TYPES = {
  goal: { label: "目标", icon: "flag", tone: "running", shape: "gr-shape-goal" },
  step: { label: "步骤", icon: "arrowRight", tone: "high", shape: "gr-shape-step" },
  fact: { label: "事实", icon: "check", tone: "success", shape: "gr-shape-fact" },
  testclaim: { label: "判断", icon: "shieldAlert", tone: "critical", shape: "gr-shape-claim" },
  hypothesis: { label: "假设", icon: "sparkles", tone: "warning", shape: "gr-shape-hypo" },
  asset: { label: "资产", icon: "server", tone: "info", shape: "gr-shape-asset" },
};

export function nodeTypeMeta(type) {
  return NODE_TYPES[type] || { label: type, icon: "circleHelp", tone: "info" };
}

// 步骤状态 → 操作者语言(原 findings-page 局部词汇,P2-09 起多页共用)。
// P4-46 ② 对齐状态总表:dispatched 显示"排队中"(与虫群列表排队条目、
// 工作台水位同词;等前置是行内标注非状态)、aborted 显示"已中止"。
export const STEP_STATUS = {
  dispatched: "排队中",
  running: "执行中",
  pending: "待开始",
  done: "已完成",
  exhausted: "预算耗尽",
  failed: "失败",
  aborted: "已中止",
  cancelled: "已取消",
  blocked: "受阻",
  approval_denied: "审批拒绝终止",
};

// 事实状态(两态:待确认 / 仲裁确认;P4-28 ② 悬停人话)。
export const FACT_STATUS = {
  candidate: { label: "待确认", variant: "info", hint: "虫群提交的事实，待主宰检查归纳" },
  confirmed: { label: "已确认", variant: "success", hint: "已经主宰检查归纳确认" },
};

// 目标证书校验结论(https 流量标注;与后端 mirror.TLSLabel 同一映射,
// P3-01 术语清单「目标证书:可信/自签/过期/主机名不符/未知签发/校验未过」)。
export const TLS_LABELS = {
  trusted: "可信",
  self_signed: "自签",
  expired: "过期",
  hostname_mismatch: "主机名不符",
  untrusted: "未知签发",
  invalid: "校验未过",
};

export function tlsLabel(v) {
  return TLS_LABELS[v] || v || "";
}

// 流量记录类型(镜留痕)→ 操作者语言。
export const TRAFFIC_TYPES = {
  http: "HTTP",
  https: "HTTPS",
  tunnel: "隧道",
};

// 六类节点状态 → 徽标 {label, variant}(P2-11 起自进度页局部映射提为
// 共用词汇;asset 无状态语义时为 null——卡片/节点不渲染状态徽标)。
export function nodeStatusMeta(type, status) {
  switch (type) {
    case "goal":
      return GOAL_STATUS[status] || null;
    case "step":
      return {
        label: STEP_STATUS[status] || status,
        variant: status === "done" ? "success" : status === "running" ? "running" : "paused",
      };
    case "fact": {
      const f = FACT_STATUS[status];
      return f || { label: status, variant: "info" };
    }
    case "testclaim":
      return CLAIM_STATUS[status] || null;
    case "hypothesis":
      return HYP_STATUS[status] || null;
    default:
      return null;
  }
}

// 终止且无产出的节点状态:进度图灰显保留(R10 废案可视化的分支态)。
export const DEAD_STATUSES = new Set(["discarded", "cancelled", "aborted", "approval_denied", "failed"]);

// 类型化边 → 操作者语言(goal / falsify 为图投影派生边,血缘上标
// "派生"/"反证";sub_of 仅资产树视图消费,进度图不画)。
export const EDGE_LABELS = {
  goal: "派生",
  produces: "产出",
  derived_from: "派生自",
  about: "关于",
  covers: "覆盖",
  depends_on: "依赖",
  verifies: "验证",
  falsify: "反证",
  sub_of: "子级",
};

// 跳转协议(P2-09):节点点击 → Sheet 详情;「在图中查看」跳节点对应
// 视图并携带 URL 定位参数(?focus=,可直达)。对应视图:资产 → 资产树,
// 其余五类 → 年轮图(P2-11 起,全图总览是它们的图上家;进度图看过程,
// 从标签直达)。
export function nodeHomeView(type) {
  return type === "asset" ? "assets" : "annulus";
}

export function jumpToNodeView(tid, nodeID, type) {
  const view = nodeHomeView(type);
  location.hash = `/task/${tid}/${view}?focus=${encodeURIComponent(nodeID)}`;
}

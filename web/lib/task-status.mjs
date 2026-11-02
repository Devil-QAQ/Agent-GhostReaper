// 任务状态显示取态——纯显示逻辑(零 React/数据层依赖,P4-27 ② 全状态
// 矩阵的唯一实现;api.js 再导出供全站消费,测试脚本直引本文件做
// 36 格矩阵断言)。.mjs 后缀:Node 测试脚本直引零配置(Next 解析同源)。

// 任务状态(Manager 运行态)→ 徽标变体。completed 为 P1-10 完成终态
//(全部目标经仲裁者判定达成);settling 为 P2-04 任务级总闸的收尾子态
//(整体超时到点:停止派发、在跑单位收尾、终局判定后定终态);
// hibernated 为 P2-08 冬眠态(可唤醒恢复);paused 为收养/异常让位的
// 暂停态;checkpointed 为 P3-07 断点存档态(进程可关,重启自动续跑)。
// P4-25 审核迭代:状态词 11→7(只动显示层,后端枚举不动)——created
// 并入「排队中」(未开跑一律排队中)、paused/checkpointed 并入「冬眠中」
//(悬停注明方式)、stopped 统称「已停止」(悬停注明结束方式,见
// taskStopWay);保留词:排队中/运行中/收尾中/受阻/冬眠中/已停止/已完成。
// 审核迭代 2:每个状态的悬停都写明具体情况——平实书面白话,陈述事实,
// 不带机器词;受阻悬停为造物主逐字拍板文案(TASK_BLOCKED_HINT),不改。
export const TASK_STATUS = {
  created: { label: "排队中", variant: "paused", title: "等待并发名额，前面的任务结束后自动开跑" },
  queued: { label: "排队中", variant: "paused", title: "等待并发名额，前面的任务结束后自动开跑" },
  started: { label: "运行中", variant: "running", title: "运行中：主宰拆解派发，虫群单位执行意图" },
  settling: { label: "收尾中", variant: "running", title: "收尾中：不再派发新意图，在跑单位收尾后结束" },
  paused: { label: "冬眠中", variant: "paused", title: "任务暂停：常见于重启时模型不可用；唤醒后原地继续" },
  hibernated: { label: "冬眠中", variant: "paused", title: "全部暂停，计时停走；唤醒后原地继续" },
  checkpointed: { label: "冬眠中", variant: "paused", title: "断点存档：单位已收拢，进程可关闭，重启后自动续跑" },
  stopped: { label: "已停止", variant: "paused" },
  completed: { label: "已完成", variant: "success", title: "已完成：全部目标达成，任务正常结束" },
};

// 任务受阻悬停文案(P4-20,造物主逐字拍板)。
export const TASK_BLOCKED_HINT = "等待模型，恢复后自动继续";

// taskActiveStatus 任务状态是否活动态(P4-25 ②):运行中(started)与
// 收尾中(settling)——受阻单显只盖活动态,取态与计数两处共用同一
// 判定,不漂移。
export function taskActiveStatus(status) {
  return status === "started" || status === "settling";
}

// taskFrozenStatus 任务是否冻结态(P4-27 ② 状态矩阵):paused(暂停,
// 常见于模型不可用被收养)/ hibernated(冬眠)/ checkpointed(断点存档)
// ——三者显示层统称"冬眠中",是世界暂停类状态;取态优先级最高
//(冬眠 > 受阻 > 收尾中 > 运行中,造物主拍板 2026-10-24:任务状态
// 只能有一个,杜绝一切并排)。
export function taskFrozenStatus(status) {
  return status === "paused" || status === "hibernated" || status === "checkpointed";
}

// taskStatusBadge 任务主状态显示取态——全状态矩阵唯一显示词(P4-20 起,
// P4-27 ② 绝对单显:9 后端态 × 受阻 × 冬眠每格一个词,零并排):
//
//   冻结态(paused/hibernated/checkpointed)→「冬眠中」单显;图上有
//     blocked 步骤时悬停追加"冻结前有 N 条受阻，唤醒后处理"(受阻
//     与否唤醒后再论);
//   活动态(started/settling)+ 受阻 →「受阻」单显(悬停固定文案,
//     受阻=全部模型熔断冷却中的普遍故障,恢复后自动回原活动态);
//   其余 → 状态词本词(排队中/运行中/收尾中/已停止/已完成)。
//
// 后端枚举不动(机器状态机零改动);blocked/blocked_steps 由 /api/tasks
// 透出(普遍故障实时信号 / 图上 blocked 步骤数)。底层生命周期语义不变。
export function taskStatusBadge(task) {
  const status = task?.status;
  if (taskFrozenStatus(status)) {
    const st = TASK_STATUS[status] || { label: status, variant: "info" };
    const n = task?.blocked_steps ?? 0;
    const title =
      n > 0 ? `${st.title}；冻结前有 ${n} 条受阻，唤醒后处理` : st.title;
    return { label: st.label, variant: st.variant, title };
  }
  if (task?.blocked && taskActiveStatus(status)) {
    return { label: "受阻", variant: "warning", title: TASK_BLOCKED_HINT };
  }
  const st = TASK_STATUS[status] || { label: status, variant: "info" };
  const title = st.title ?? taskStopWay(task);
  return title ? { label: st.label, variant: st.variant, title } : { label: st.label, variant: st.variant };
}

// taskStopWay 已停止的结束方式(P4-25 审核迭代:已终止/已收尾合并为
// 已停止,悬停注明具体情况)——settled_by 为账本锚点值,显示层翻译:
// task_gate→整体超时、closure_prompt→收口结束、其余→手动终止。
export function taskStopWay(task) {
  if (task?.status !== "stopped") return null;
  if (task?.settled_by === "task_gate") return "整体超时：跑到设定的时长上限，有序收场";
  if (task?.settled_by === "closure_prompt") return "收口结束：连续多轮无新发现，选择结束，有序收场";
  return "手动终止：在面板选择终止，任务不可恢复";
}

// 任务状态全矩阵测试(P4-27 ②b):9 后端态 × 受阻布尔 × 冬眠布尔,
// 每格断言唯一显示词、零并排。挂 make web / make test 前置,与既有
// 闸门并列——"改一处漏一处"由此根治:任何新状态或取态改动过不了
// 构建即被拦下。
//
// 冬眠布尔的语义:与冻结态正交(矩阵穷举含现实不可能出现的组合,
// 防御性定义——任何组合都必须落且只落一个词)。冻结态本身即"世界
// 暂停",矩阵里冻结×受阻=1 的格子取「冬眠中」(受阻唤醒后再论)。

import { TASK_BLOCKED_HINT, taskStatusBadge, taskBlockedDot, blockedHint } from "../lib/task-status.mjs";

const STATUSES = [
  "created",
  "queued",
  "started",
  "settling",
  "paused",
  "hibernated",
  "checkpointed",
  "stopped",
  "completed",
];

// 期望显示词:冻结 > 受阻 > 收尾中 > 运行中,其余状态本词。
function expectWord(status, blocked) {
  if (status === "paused" || status === "hibernated" || status === "checkpointed") {
    return "冬眠中";
  }
  if (blocked && (status === "started" || status === "settling")) {
    return "受阻";
  }
  return {
    created: "排队中",
    queued: "排队中",
    started: "运行中",
    settling: "收尾中",
    stopped: "已停止",
    completed: "已完成",
  }[status];
}

const WORDS = ["排队中", "运行中", "收尾中", "受阻", "冬眠中", "已停止", "已完成"];
let fails = 0;
let cells = 0;

for (const status of STATUSES) {
  for (const blocked of [false, true]) {
    for (const frozen of [false, true]) {
      cells++;
      // frozen 组合用冻结态字段体现(paused/hibernated/checkpointed
      // 即冻结;非冻结态的 frozen=true 为防御格,行为应与 false 相同)。
      const task = { status, blocked, blocked_steps: frozen ? 2 : 0 };
      const badge = taskStatusBadge(task);
      const want = expectWord(status, blocked);
      const tag = `${status} × blocked=${blocked ? 1 : 0} × frozen=${frozen ? 1 : 0}`;
      if (badge.label !== want) {
        console.error(`矩阵格 ${tag}: 期望「${want}」实得「${badge.label}」`);
        fails++;
        continue;
      }
      if (!WORDS.includes(badge.label)) {
        console.error(`矩阵格 ${tag}: 显示词「${badge.label}」不在七词表`);
        fails++;
      }
      if (typeof badge.title !== "string" || badge.title === "") {
        console.error(`矩阵格 ${tag}: 悬停文案缺失`);
        fails++;
      }
    }
  }
}

// 关键格逐字:受阻悬停(造物主拍板文案)、冬眠×受阻悬停追加、
// 活动态×受阻=1 单显、冻结×受阻不并排。
const hint = taskStatusBadge({ status: "started", blocked: true });
if (hint.title !== TASK_BLOCKED_HINT) {
  console.error(`受阻悬停应为拍板文案: ${hint.title}`);
  fails++;
}
const hiber = taskStatusBadge({ status: "hibernated", blocked: true, blocked_steps: 3 });
if (
  hiber.label !== "冬眠中" ||
  !hiber.title.includes("冻结前有 3 条受阻，唤醒后处理")
) {
  console.error(`冬眠×受阻应单显冬眠中且悬停带受阻数: ${JSON.stringify(hiber)}`);
  fails++;
}
const noSteps = taskStatusBadge({ status: "hibernated", blocked: true, blocked_steps: 0 });
if (noSteps.title.includes("冻结前有")) {
  console.error(`冬眠无受阻步骤不应追加受阻悬停: ${noSteps.title}`);
  fails++;
}
// 已停止悬停按结束方式(P4-25):三值抽查。
if (!taskStatusBadge({ status: "stopped" }).title.includes("手动终止")) {
  console.error("已停止默认悬停应为手动终止");
  fails++;
}

// 受阻红点取态(P4-57 ⑤ 三层递进):活动态亮(计数+归因悬停)、冻结/
// 终态不亮、零步不亮;归因文案逐字——模型故障类/程序异常类/两类并存。
{
  const on = taskBlockedDot({ status: "started", blocked_steps: 4, blocked_fault: 3, blocked_defect: 1 });
  if (!on || on.count !== 4) {
    console.error(`运行中受阻红点应亮且计数=总步数: ${JSON.stringify(on)}`);
    fails++;
  } else if (on.title !== "受阻 3 步，恢复后自动继续\n受阻 1 步，程序异常已记录，更新修复后自动恢复") {
    console.error(`两类并存悬停应逐行分列归因文案: ${JSON.stringify(on.title)}`);
    fails++;
  }
  const faultOnly = blockedHint({ blocked_steps: 2, blocked_fault: 2, blocked_defect: 0 });
  if (faultOnly !== "受阻 2 步，恢复后自动继续") {
    console.error(`模型故障类文案逐字不符: ${faultOnly}`);
    fails++;
  }
  const defectOnly = blockedHint({ blocked_steps: 2, blocked_fault: 0, blocked_defect: 2 });
  if (defectOnly !== "受阻 2 步，程序异常已记录，更新修复后自动恢复") {
    console.error(`程序异常类文案逐字不符: ${defectOnly}`);
    fails++;
  }
  for (const st of ["paused", "hibernated", "checkpointed", "stopped", "completed"]) {
    if (taskBlockedDot({ status: st, blocked_steps: 3 })) {
      console.error(`冻结/终态 ${st} 不应亮受阻红点`);
      fails++;
    }
  }
  if (taskBlockedDot({ status: "started", blocked_steps: 0 })) {
    console.error("零受阻步不应亮红点");
    fails++;
  }
  // 归因拆分缺位(旧负载):回退总数+模型故障文案,不撒谎也不瞎标。
  const legacy = blockedHint({ blocked_steps: 5 });
  if (legacy !== "受阻 5 步，恢复后自动继续") {
    console.error(`归因缺位回退文案不符: ${legacy}`);
    fails++;
  }
}

if (fails > 0) {
  console.error(`任务状态矩阵测试失败: ${fails}/${cells} 格`);
  process.exit(1);
}
console.log(`任务状态矩阵测试通过: ${cells} 格唯一显示词零并排`);

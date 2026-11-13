// 审批记录列表折叠与空参数判定——纯显示逻辑(零 React 依赖,P4-74 ④
// 的唯一实现;roe-page.js 消费,测试脚本直引本文件做类级断言)。
// .mjs 后缀:Node 测试脚本直引零配置(Next 解析同源,与 task-status
// 同惯例)。
//
// 折叠(P4-74 ④,问题清单 #90 ③):同步骤放行逐条留痕 5 分钟 30 条
// 刷满列表——机器放行留痕改为连续段折叠成一行计数(点开看明细),
// 账本照旧全量(服务端全量返回,折叠只在展示层);列表默认只显示操
// 作者裁决与待决记录。同组放行/性质判定是同族机器留痕,同款折叠
// (P4-52 ③ 全量可达不回退——折叠行点开即明细)。

// 机器放行留痕类型(折叠对象):连续段合成一行"×N"计数行。
export const FOLDABLE_RECORD_TYPES = {
  step_pass: "同步骤放行",
  group_pass: "同组放行",
  triage: "性质判定",
};

// 折叠分组键:step_pass 按步骤(同步骤的放行归一行——"同步骤放行"
// 字面),其余按类型(全局一族一行语义,跨步骤连续段自然分段)。
function foldKeyOf(record) {
  if (record.type === "step_pass") return `step_pass:${record.step_id ?? ""}`;
  return record.type;
}

// foldApprovalRecords 审批记录 → 列表条目(倒序输入,倒序输出):
//   { kind: "record", record }                 单条可见记录
//   { kind: "fold", type, label, count, records } 连续机器留痕段折叠行
// 折叠只合连续段(同键)——不同步骤/不同类型的留痕各归各行,与其他
// 记录穿插时自然分段;records 携带段内全量明细(点开渲染)。
export function foldApprovalRecords(records) {
  const items = [];
  const pushFold = (type, label, seg) => {
    // 段内按输入序(倒序最新在先)保留,计数即长度。
    items.push({ kind: "fold", type, label, count: seg.length, records: seg });
  };
  let seg = []; // 当前连续折叠段(同 foldKey)
  let segKey = null;
  let segLabel = "";
  const flush = () => {
    if (seg.length > 0) pushFold(seg[0].type, segLabel, seg);
    seg = [];
    segKey = null;
    segLabel = "";
  };
  for (const r of records ?? []) {
    const label = FOLDABLE_RECORD_TYPES[r?.type];
    if (!label) {
      flush();
      items.push({ kind: "record", record: r });
      continue;
    }
    const key = foldKeyOf(r);
    if (key !== segKey) {
      flush();
      segKey = key;
      segLabel = label;
    }
    seg.push(r);
  }
  flush();
  return items;
}

// argsKnownOnCard 命令/参数是否可上卡(P4-74 ②):空串与 "{}"(无参数
// 调用形态)都无可核对内容——如实标注"参数未记录",禁止空白/裸 {}。
// 其余原文上卡(含 {"command":""} 等畸形原文,照实呈现)。
export function argsKnownOnCard(toolName, toolArgs) {
  if (!toolName) return true; // 无工具的卡(资产类)不渲染命令行
  const t = (toolArgs ?? "").trim();
  return t !== "" && t !== "{}";
}

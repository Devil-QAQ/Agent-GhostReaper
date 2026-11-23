// Previously saved draft constraints are shown as editable operator notes.
// This only preserves draft text; it creates no matcher, quota counter or grant.
export function draftPolicyNotes(notes, constraints) {
  const lines = typeof notes === "string" && notes ? [notes] : [];
  for (const rule of Array.isArray(constraints) ? constraints : []) {
    const action = typeof rule?.action === "string" ? rule.action?.trim() : "";
    if (!action) continue;
    const scope = Array.isArray(rule.scope) && rule.scope?.length ? `；对象：${rule.scope?.join("、")}` : "";
    const quota = Number(rule.quota) > 0 ? `；次数上限：${rule.quota}，实际次数无法核实时需审批` : "";
    const basis = typeof rule.basis === "string" && rule.basis ? `；原文：${rule.basis}` : "";
    lines.push(`${rule.disposition === "allow" ? "允许" : "禁止"}${action}${scope}${quota}${basis}`);
  }
  return [...new Set(lines)].join("\n");
}

// Only transpose the existing focused panorama layout; membership and provenance
// are supplied by pview/links, never inferred from coverage or node types.
export function claimChainLayout(skeleton, links) {
  const ids = new Set(links.ids || []), positions = new Map((links.positions || []).map(p => [p.id, p]));
  const nodes = (skeleton.nodes || []).filter(n => ids.has(n.id)).map(n => {
    const p = positions.get(n.id);
    if (!p) throw new Error("链路位置缺失");
    return { ...n, x: p.y * 2.4, y: p.x / 2 };
  });
  if (nodes.length !== ids.size) throw new Error("链路已更新");
  const byID = new Map(nodes.map(n => [n.id, n]));
  const edges = (links.edges || []).map(e => {
    const a = byID.get(e.from), b = byID.get(e.to);
    if (!a || !b) throw new Error("链路已更新");
    // Ports lie along the short face after transposing the vertical layers.
    const factor = a.x === b.x ? 2.4 : .25;
    return { ...e, out_port: (e.out_port || 0) * factor, in_port: (e.in_port || 0) * factor };
  });
  const aliases = Object.fromEntries(Object.entries(skeleton.aliases || {}).map(([id, targets]) => [id, targets.filter(target => ids.has(target))]).filter(([, targets]) => targets.length));
  const xs = nodes.map(n => n.x), ys = nodes.map(n => n.y);
  const left = nodes.length ? Math.min(...xs) - 108 : 0, top = nodes.length ? Math.min(...ys) - 48 : 0;
  return { rev: skeleton.rev, nodes, edges, aliases, bounds: nodes.length ? [left, top, Math.max(...xs) + 108 - left, Math.max(...ys) + 48 - top] : [0, 0, 1, 1] };
}

// Deterministic solar layout. Runs in a worker; no simulation during drawing.
export function assetNetLayout(assets = [], hierarchy = [], relations = []) {
  const byID = new Map(assets.filter(a => a?.id).map(a => [a.id, a]));
  const ids = [...byID.keys()].sort(), parents = new Map(), children = new Map(), edges = [], seenEdges = new Set();
  for (const e of hierarchy) {
    if (!byID.has(e?.from) || !byID.has(e?.to) || e.from === e.to) continue;
    const key = `${e.to}\0${e.from}`;
    if (seenEdges.has(key)) continue;
    seenEdges.add(key); edges.push({ from: e.to, to: e.from, rel: "sub_of" });
    if (!parents.has(e.from)) parents.set(e.from, []);
    if (!children.has(e.to)) children.set(e.to, []);
    parents.get(e.from).push(e.to); children.get(e.to).push(e.from);
  }
  for (const relation of relations) {
    if (!byID.has(relation?.from) || !byID.has(relation?.to) || relation.from === relation.to) continue;
    const labels = { resolves_to: "解析到", hosted_on: "部署在", related_to: "关联" };
    if (!labels[relation.kind]) continue;
    const key = `${relation.kind}\0${relation.from}\0${relation.to}`;
    if (seenEdges.has(key)) continue;
    seenEdges.add(key); edges.push({ ...relation, rel: relation.kind, label: labels[relation.kind] });

  }
  edges.sort((a, b) => String(a.from).localeCompare(b.from) || String(a.to).localeCompare(b.to) || String(a.rel).localeCompare(b.rel));
  const roots = ids.filter(id => !parents.has(id)), memberships = new Map(), depth = new Map();
  const visit = root => {
    const queue = [[root, 0]], visited = new Set();
    for (let i = 0; i < queue.length; i++) {
      const [id, level] = queue[i]; if (visited.has(id)) continue; visited.add(id);
      if (!memberships.has(id)) memberships.set(id, []);
      memberships.get(id).push(root); depth.set(id, Math.min(depth.get(id) ?? Infinity, level));
      for (const child of children.get(id) || []) queue.push([child, level + 1]);
    }
  };
  for (const root of roots) visit(root);
  // A malformed cycle is still visible, once, without inventing ledger edges.
  for (const id of ids) if (!memberships.has(id)) { roots.push(id); visit(id); }
  const groups = new Map(roots.map(id => [id, []]));
  for (const id of ids) if (!roots.includes(id)) groups.get(memberships.get(id)[0]).push(id);
  const sizes = new Map();
  for (const root of roots) {
    const rings = Math.ceil((Math.sqrt(1 + 4 * groups.get(root).length / 3) - 1) / 2);
    sizes.set(root, rings ? 134 + (rings - 1) * 112 : 0);
  }
  const columns = Math.max(1, Math.ceil(Math.sqrt(roots.length * 1.5))), centers = new Map();
  const rowHeights = [], colWidths = [];
  roots.forEach((id, i) => { const col = i % columns, row = Math.floor(i / columns), width = sizes.get(id) * 2 + 280; colWidths[col] = Math.max(colWidths[col] || 0, width); rowHeights[row] = Math.max(rowHeights[row] || 0, width); });
  const offset = (list, index) => list.slice(0, index).reduce((sum, n) => sum + n, 0) + list[index] / 2;
  roots.forEach((id, i) => centers.set(id, { x: offset(colWidths, i % columns), y: offset(rowHeights, Math.floor(i / columns)) }));
  const positions = new Map(), targets = new Map(), rootSet = new Set(roots);
  for (const root of roots) {
    const center = centers.get(root); positions.set(root, { ...center }); targets.set(root, center);
    const members = groups.get(root).sort((a, b) => depth.get(a) - depth.get(b) || a.localeCompare(b));
    let cursor = 0, ring = 1;
    while (cursor < members.length) {
      const count = Math.min(members.length - cursor, 6 * ring), radius = 134 + (ring - 1) * 112;
      for (let i = 0; i < count; i++) {
        const id = members[cursor++], angle = -Math.PI / 2 + i * Math.PI * 2 / count + (ring % 2 ? 0 : .23);
        const point = { x: center.x + Math.cos(angle) * radius, y: center.y + Math.sin(angle) * radius * .82 };
        positions.set(id, { ...point }); targets.set(id, point);
      }
      ring++;
    }
  }
  // Shared assets occupy one position between their actual systems.
  for (const id of ids) {
    const membership = memberships.get(id); if (rootSet.has(id) || membership.length < 2) continue;
    const point = membership.reduce((p, root) => ({ x: p.x + centers.get(root).x / membership.length, y: p.y + centers.get(root).y / membership.length }), { x: 0, y: 0 });
    positions.set(id, { ...point }); targets.set(id, point);
  }
  // Bounded spring relaxation with local spatial repulsion (no all-pairs loop).
  const movable = ids.filter(id => !rootSet.has(id));
  for (let iteration = 0; movable.length && iteration < 64; iteration++) {
    const grid = new Map(), force = new Map();
    for (const id of ids) {
      const p = positions.get(id), key = `${Math.floor(p.x / 160)},${Math.floor(p.y / 80)}`;
      if (!grid.has(key)) grid.set(key, []); grid.get(key).push(id);
      force.set(id, { x: (targets.get(id).x - p.x) * .025, y: (targets.get(id).y - p.y) * .025 });
    }
    for (const id of movable) {
      const p = positions.get(id), f = force.get(id), gx = Math.floor(p.x / 160), gy = Math.floor(p.y / 80);
      for (let dx = -1; dx <= 1; dx++) for (let dy = -1; dy <= 1; dy++) for (const other of grid.get(`${gx + dx},${gy + dy}`) || []) {
        if (other === id) continue;
        const q = positions.get(other); let x = p.x - q.x, y = (p.y - q.y) * 2;
        if (x === 0 && y === 0) { x = id < other ? -1 : 1; y = 1; }
        const distance = Math.hypot(x, y);
        if (distance < 145) { const strength = (145 - distance) * .18; f.x += x / distance * strength; f.y += y / distance * strength; }
      }
    }
    for (const id of movable) { const p = positions.get(id), f = force.get(id); p.x += Math.max(-12, Math.min(12, f.x)); p.y += Math.max(-12, Math.min(12, f.y)); }
  }
  const nodes = ids.map(id => ({ ...byID.get(id), t: "asset", ti: byID.get(id).display_title || byID.get(id).title || id, ...positions.get(id), primary: rootSet.has(id), systems: memberships.get(id), orbit: sizes.get(id) || 0 }));
  let left = Infinity, top = Infinity, right = -Infinity, bottom = -Infinity;
  for (const n of nodes) { left = Math.min(left, n.x - 100); right = Math.max(right, n.x + 100); top = Math.min(top, n.y - 56); bottom = Math.max(bottom, n.y + 72); }
  return { nodes, edges, roots, bounds: nodes.length ? [left, top, right - left, bottom - top] : [0, 0, 1, 1] };
}

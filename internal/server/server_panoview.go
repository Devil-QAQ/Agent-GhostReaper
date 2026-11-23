package server

// 全景图投影：显示口径以 Process/P5-09展示与交互确认-20261118.md 为准。
// 连线只画真实产生/来源及链路入口的资产派发；执行前置留在详情。事实合入产出
// 意图，子资产保留引用但不另画卡片。与 /panorama 研判接口无关。

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ghostreaper/internal/rings"
)

const panoCardW = 168.0
const panoCardH = 48.0

type panoGoalRow struct {
	ID  string `json:"id"`
	Ti  string `json:"ti"`
	Ach bool   `json:"ach"`
}
type panoFactRow struct {
	ID     string `json:"id"`
	Ti     string `json:"ti"`
	Status string `json:"status,omitempty"`
}
type panoNodeRow struct {
	ID      string        `json:"id"`
	T       string        `json:"t"`
	Ti      string        `json:"ti"`
	X       float64       `json:"x"`
	Y       float64       `json:"y"`
	Root    string        `json:"root,omitempty"`
	Sev     string        `json:"sev,omitempty"`
	Dsp     string        `json:"dsp,omitempty"`
	Results []panoFactRow `json:"results,omitempty"`
	Assets  []panoFactRow `json:"assets,omitempty"`
	Created int64         `json:"created_rev"`
}
type panoEdgeRow struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Rel     string   `json:"rel"`
	Refs    []string `json:"refs,omitempty"`
	Dashed  bool     `json:"dashed,omitempty"`
	Blocked bool     `json:"blocked,omitempty"`
	Out     float64  `json:"out_port"`
	In      float64  `json:"in_port"`
}
type panoSkeletonView struct {
	Rev     int64               `json:"rev"`
	Bounds  [4]float64          `json:"bounds"`
	Goals   []panoGoalRow       `json:"goals"`
	Nodes   []panoNodeRow       `json:"nodes"`
	Edges   []panoEdgeRow       `json:"edges"`
	Aliases map[string][]string `json:"aliases"`
	// Retain the original endpoints for focus selection before display aliases merge facts.
	rawNodes map[string]rings.NodeFull
	rawEdges []rings.EdgeView
}

func panoClaimVisible(n *rings.NodeFull) bool {
	d, _ := rings.ClaimDisplay(n.Status)
	return d == rings.ClaimDisplayConfirmed || d == rings.ClaimDisplayFixed || d == rings.ClaimDisplayInsufficient
}
func panoLinkTypeOf(t rings.NodeType) string {
	switch t {
	case rings.NodeStep:
		return "step"
	case rings.NodeTestClaim:
		return "claim"
	default:
		return string(t)
	}
}

func (s *Server) handlePanoSkeleton(w http.ResponseWriter, r *http.Request) {
	if !s.taskExists(r.PathValue("id")) {
		writeErr(w, 404, fmt.Errorf("任务不存在"))
		return
	}
	v, err := s.panoSkeletonOf(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) panoSkeletonOf(ctx context.Context, task string) (*panoSkeletonView, error) {
	rev, err := s.Store.CurrentRevision(ctx, task)
	if err != nil {
		return nil, err
	}
	v := &panoSkeletonView{Rev: rev, Goals: []panoGoalRow{}, Nodes: []panoNodeRow{}, Edges: []panoEdgeRow{}, Aliases: map[string][]string{}}
	all := map[string]rings.NodeFull{}
	order := []rings.NodeFull{}
	for _, kind := range []rings.NodeType{rings.NodeGoal, rings.NodeAsset, rings.NodeStep, rings.NodeFact, rings.NodeTestClaim} {
		list, e := s.Store.NodesByType(ctx, task, kind, "")
		if e != nil {
			return nil, e
		}
		for _, n := range list {
			all[n.ID] = n
			order = append(order, n)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].CreatedRev != order[j].CreatedRev {
			return order[i].CreatedRev < order[j].CreatedRev
		}
		return order[i].ID < order[j].ID
	})
	edges, err := s.Store.Edges(ctx, task)
	if err != nil {
		return nil, err
	}
	v.rawNodes, v.rawEdges = all, edges
	polish := s.polishedNodeTitleOf(ctx, task)
	digests, err := s.Store.ClaimDigests(ctx, task)
	if err != nil {
		return nil, err
	}
	parent := map[string]string{}
	roots := map[string]string{}
	owners := map[string][]string{}
	covers := map[string][]string{}
	validStep := func(id string) bool {
		n, ok := all[id]
		if !ok || n.Type != rings.NodeStep {
			return false
		}
		kind, _ := n.Detail["kind"].(string)
		return kind != "retake"
	}
	assetNodes := []rings.NodeFull{}
	for _, n := range order {
		if n.Type == rings.NodeAsset {
			assetNodes = append(assetNodes, n)
		}
	}
	roe, err := s.Store.CurrentRoE(ctx, task)
	if err != nil {
		return nil, err
	}
	members := rings.InventoryAssetIDs(assetNodes, roe)
	validAsset := func(id string) bool { return members[id] }
	for _, e := range rings.AssetDisplayHierarchy(assetNodes, edges, members) {
		parent[e.From] = e.To
	}

	for _, e := range edges {
		switch e.Type {
		case rings.EdgeProduces:
			if validStep(e.From) {
				if n, ok := all[e.To]; ok && n.Type == rings.NodeFact && n.Status == "confirmed" {
					owners[e.To] = append(owners[e.To], e.From)
				}
			}
		case rings.EdgeCovers:
			if validStep(e.From) && validAsset(e.To) {
				covers[e.From] = append(covers[e.From], e.To)
			}
		}
	}
	rootOf := func(id string) string {
		if root, ok := roots[id]; ok {
			return root
		}
		cur := id
		seen := map[string]bool{}
		path := []string{}
		for parent[cur] != "" && !seen[cur] {
			seen[cur] = true
			path = append(path, cur)
			cur = parent[cur]
		}
		if seen[cur] {
			path = append(path, cur)
			sort.Strings(path)
			cur = path[0]
		}
		roots[id] = cur
		return cur
	}
	title := func(n rings.NodeFull) string {
		if p := polish[n.ID]; p != "" {
			return p
		}
		return n.Title
	}
	index := map[string]int{}
	for _, n := range order {
		row := panoNodeRow{ID: n.ID, T: panoLinkTypeOf(n.Type), Ti: title(n), Created: n.CreatedRev}
		switch n.Type {
		case rings.NodeGoal:
			v.Goals = append(v.Goals, panoGoalRow{ID: n.ID, Ti: n.Title, Ach: n.Status == "achieved"})
			continue
		case rings.NodeAsset:
			if !validAsset(n.ID) {
				continue
			}
			root := rootOf(n.ID)
			v.Aliases[n.ID] = []string{root}
			if root != n.ID {
				continue
			}
			row.Root = root
			row.Ti = rings.AssetDisplayTitle(n.AssetClass, n.Title)
		case rings.NodeStep:
			if !validStep(n.ID) {
				continue
			}
			v.Aliases[n.ID] = []string{n.ID}
			for _, aid := range covers[n.ID] {
				a := all[aid]
				row.Assets = append(row.Assets, panoFactRow{ID: aid, Ti: a.Title})
				if row.Root == "" {
					row.Root = rootOf(aid)
				}
			}
		case rings.NodeFact:
			if n.Status == "confirmed" && len(owners[n.ID]) > 0 {
				v.Aliases[n.ID] = append([]string{}, owners[n.ID]...)
			}
			continue
		case rings.NodeTestClaim:
			if !panoClaimVisible(&n) {
				continue
			}
			row.T = "claim"
			row.Dsp, _ = rings.ClaimDisplay(n.Status)
			row.Sev = digests[n.ID].Severity
			if row.Sev == "" {
				row.Sev, _ = n.Detail["severity"].(string)
			}
			if d := digests[n.ID]; d.Title != "" {
				row.Ti = d.Title
			} else if d.Statement != "" {
				row.Ti = d.Statement
			}
			v.Aliases[n.ID] = []string{n.ID}
		default:
			continue
		}
		index[row.ID] = len(v.Nodes)
		v.Nodes = append(v.Nodes, row)
	}
	for _, n := range order {
		if n.Type != rings.NodeFact || n.Status != "confirmed" {
			continue
		}
		for _, sid := range owners[n.ID] {
			if i, ok := index[sid]; ok {
				v.Nodes[i].Results = append(v.Nodes[i].Results, panoFactRow{ID: n.ID, Ti: title(n), Status: n.Status})
			}
		}
	}
	// 展示只按来源→去向去重；完整关系仍由详情直接读取账本。
	lineIndex := map[string]int{}
	lineRefs := map[string]map[string]bool{}
	add := func(from, to, rel, ref string) {
		if from == to {
			return
		}
		if _, ok := index[from]; !ok {
			return
		}
		if _, ok := index[to]; !ok {
			return
		}
		key := from + "\x00" + to
		i, ok := lineIndex[key]
		if !ok {
			i = len(v.Edges)
			lineIndex[key] = i
			lineRefs[key] = map[string]bool{}
			v.Edges = append(v.Edges, panoEdgeRow{From: from, To: to, Rel: rel})
		}
		if rel < v.Edges[i].Rel {
			v.Edges[i].Rel = rel
		}
		if ref != "" && !lineRefs[key][ref] {
			lineRefs[key][ref] = true
			v.Edges[i].Refs = append(v.Edges[i].Refs, ref)
		}
	}
	mapRelation := func(from, to, rel, ref string) {
		for _, a := range v.Aliases[from] {
			for _, b := range v.Aliases[to] {
				add(a, b, rel, ref)
			}
		}
	}
	for _, e := range edges {
		switch e.Type {
		case rings.EdgeSourcedFrom:
			mapRelation(e.To, e.From, "sourced_from", e.To)
		case rings.EdgeDerivedFrom:
			mapRelation(e.To, e.From, "derived_from", e.To)
		case rings.EdgeProduces:
			if a, ok := all[e.To]; ok && a.Type == rings.NodeAsset && rootOf(a.ID) == a.ID {
				mapRelation(e.From, e.To, "discovered", e.To)
			}
		}
	}
	for _, n := range order {
		if n.Type == rings.NodeTestClaim && panoClaimVisible(&n) {
			if sid, _ := n.Detail["step_id"].(string); validStep(sid) {
				mapRelation(sid, n.ID, "produces", sid)
			}
		}
		if n.Type == rings.NodeAsset && validAsset(n.ID) && rootOf(n.ID) == n.ID {
			if sid, _ := n.Detail["step_id"].(string); validStep(sid) {
				mapRelation(sid, n.ID, "discovered", n.ID)
			} else if fid, _ := n.Detail["discovered_fact"].(string); fid != "" {
				mapRelation(fid, n.ID, "discovered", fid)
			}
		}
	}
	// Asset assignments are the entry of a path, not a new source for every
	// downstream intention. Compute these once for the overview; focus can only
	// select from this same set, never create a different assignment graph.
	adj, upstream := map[string][]string{}, map[string][]string{}
	for _, edge := range v.Edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
		upstream[edge.To] = append(upstream[edge.To], edge.From)
	}
	reachable := func(from string, links map[string][]string) map[string]bool {
		pending, seen := []string{from}, map[string]bool{}
		for i := 0; i < len(pending); i++ {
			id := pending[i]
			if !seen[id] {
				seen[id] = true
				pending = append(pending, links[id]...)
			}
		}
		return seen
	}
	entries := map[string]map[string]bool{}
	for step, assets := range covers {
		for _, asset := range assets {
			root := rootOf(asset)
			if entries[root] == nil {
				entries[root] = map[string]bool{}
			}
			entries[root][step] = true
		}
	}
	forward, backward := map[string]map[string]bool{}, map[string]map[string]bool{}
	for root := range entries {
		forward[root], backward[root] = reachable(root, adj), reachable(root, upstream)
	}
	for _, n := range order {
		if !validStep(n.ID) {
			continue
		}
		for _, asset := range covers[n.ID] {
			root := rootOf(asset)
			if forward[root][n.ID] || backward[root][n.ID] {
				continue
			}
			redundant := false
			pending, seen := append([]string(nil), upstream[n.ID]...), map[string]bool{n.ID: true}
			for i := 0; i < len(pending); i++ {
				earlier := pending[i]
				if seen[earlier] {
					continue
				}
				seen[earlier] = true
				if entries[root][earlier] && (earlier < n.ID || !reachable(n.ID, adj)[earlier]) {
					redundant = true
					break
				}
				pending = append(pending, upstream[earlier]...)
			}
			if !redundant {
				add(root, n.ID, "covers", asset)
			}
		}
	}
	for i := range v.Edges {
		sort.Strings(v.Edges[i].Refs)
	}
	layoutPanorama(v)
	return v, nil
}

// layoutPanorama computes coordinates and attachment points on the server. The
// client only moves the camera and interpolates appearances; it never lays out nodes.
func layoutPanorama(v *panoSkeletonView) {
	index := map[string]int{}
	for i, n := range v.Nodes {
		index[n.ID] = i
	}
	parents := map[string][]string{}
	children := map[string][]string{}
	degree := map[string]int{}
	rank := map[string]int{}
	for _, n := range v.Nodes {
		if n.T == "step" {
			degree[n.ID] = 0
		}
	}
	for _, e := range v.Edges {
		a, b := v.Nodes[index[e.From]], v.Nodes[index[e.To]]
		if a.T == "step" && b.T == "step" {
			parents[b.ID] = append(parents[b.ID], a.ID)
			children[a.ID] = append(children[a.ID], b.ID)
			degree[b.ID]++
		}
	}
	queue := []string{}
	for _, n := range v.Nodes {
		if n.T == "step" && degree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	for q := 0; q < len(queue); q++ {
		id := queue[q]
		for _, next := range children[id] {
			if rank[next] < rank[id]+1 {
				rank[next] = rank[id] + 1
			}
			degree[next]--
			if degree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	// Invalid historical cycles cannot hang layout; their original relations remain visible.
	groups := map[string]map[int][]string{}
	keys := []string{}
	seen := map[string]bool{}
	maxBand := 0
	for _, n := range v.Nodes {
		if n.T == "asset" {
			keys = append(keys, n.ID)
			seen[n.ID] = true
		}
	}
	for i := range v.Nodes {
		n := &v.Nodes[i]
		if n.T != "step" {
			continue
		}
		band := (rank[n.ID] + 1) / 2
		n.Y = 158 + float64(band)*120
		if band > maxBand {
			maxBand = band
		}
		if !seen[n.Root] {
			keys = append(keys, n.Root)
			seen[n.Root] = true
		}
		if groups[n.Root] == nil {
			groups[n.Root] = map[int][]string{}
		}
		groups[n.Root][band] = append(groups[n.Root][band], n.ID)
	}
	claimGroups := map[string][]string{}
	for i := range v.Nodes {
		n := &v.Nodes[i]
		if n.T != "claim" {
			continue
		}
		for _, e := range v.Edges {
			if e.To == n.ID {
				source := v.Nodes[index[e.From]]
				if source.T == "step" || source.T == "asset" {
					n.Root = source.Root
					break
				}
			}
		}
		if !seen[n.Root] {
			keys = append(keys, n.Root)
			seen[n.Root] = true
		}
		claimGroups[n.Root] = append(claimGroups[n.Root], n.ID)
	}
	cursor := 0.0
	for _, key := range keys {
		cols := 1
		for _, ids := range groups[key] {
			if len(ids) > cols {
				cols = len(ids)
			}
		}
		if len(claimGroups[key]) > cols {
			cols = len(claimGroups[key])
		}
		span := float64(cols) * 200
		if i, ok := index[key]; ok {
			v.Nodes[i].X = cursor + span/2
			v.Nodes[i].Y = 54
		}
		for _, ids := range groups[key] {
			offset := (span - float64(len(ids))*200) / 2
			for j, id := range ids {
				v.Nodes[index[id]].X = cursor + offset + float64(j)*200 + 100
			}
		}
		ids := claimGroups[key]
		offset := (span - float64(len(ids))*200) / 2
		for j, id := range ids {
			n := &v.Nodes[index[id]]
			n.X = cursor + offset + float64(j)*200 + 100
			n.Y = 158 + float64(maxBand+1)*120 + 56
		}
		cursor += span + 112
	}
	finishPanoLayout(v)
}

// finishPanoLayout 共用连线端口、避让和边界计算，适用于总览与局部视野。
func finishPanoLayout(v *panoSkeletonView) {
	index := make(map[string]int, len(v.Nodes))
	for i, n := range v.Nodes {
		index[n.ID] = i
	}
	for i := range v.Edges {
		e := &v.Edges[i]
		e.Blocked, e.Out, e.In = false, 0, 0
		e.Dashed = false
		a, b := v.Nodes[index[e.From]], v.Nodes[index[e.To]]
		if a.Y == b.Y {
			for _, n := range v.Nodes {
				if n.ID != a.ID && n.ID != b.ID && n.Y == a.Y && n.X > math.Min(a.X, b.X) && n.X < math.Max(a.X, b.X) {
					e.Blocked = true
					break
				}
			}
		}
	}
	// 同一卡片同一物理边上的入线、出线共同分配端点，不能分别占中点。
	type faceKey struct {
		node string
		side int
	}
	type portRef struct {
		edge     int
		out      bool
		across   float64
		neighbor string
	}
	ports := map[faceKey][]portRef{}
	face := func(a, b panoNodeRow) int {
		if b.Y < a.Y {
			return 0
		} // 上
		if b.Y > a.Y {
			return 2
		} // 下
		if b.X >= a.X {
			return 1
		} // 右
		return 3 // 左
	}
	for i, e := range v.Edges {
		a, b := v.Nodes[index[e.From]], v.Nodes[index[e.To]]
		side := face(a, b)
		aAcross, bAcross := b.X, a.X
		if side == 1 || side == 3 {
			aAcross, bAcross = b.Y, a.Y
		}
		ports[faceKey{a.ID, side}] = append(ports[faceKey{a.ID, side}], portRef{i, true, aAcross, b.ID})
		opposite := (side + 2) % 4
		ports[faceKey{b.ID, opposite}] = append(ports[faceKey{b.ID, opposite}], portRef{i, false, bAcross, a.ID})
	}
	for key, refs := range ports {
		sort.Slice(refs, func(i, j int) bool {
			a, b := refs[i], refs[j]
			if a.across != b.across {
				return a.across < b.across
			}
			if a.neighbor != b.neighbor {
				return a.neighbor < b.neighbor
			}
			return !a.out && b.out
		})
		span, step := 96.0, 24.0
		if key.side == 1 || key.side == 3 {
			span, step = 32, 12
		}
		for j, ref := range refs {
			port := (float64(j) - float64(len(refs)-1)/2) * math.Min(step, span/math.Max(1, float64(len(refs)-1)))
			if ref.out {
				v.Edges[ref.edge].Out = port
			} else {
				v.Edges[ref.edge].In = port
			}
		}
	}
	if len(v.Nodes) == 0 {
		return
	}
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, n := range v.Nodes {
		left = math.Min(left, n.X-84)
		right = math.Max(right, n.X+84)
		top = math.Min(top, n.Y-24)
		bottom = math.Max(bottom, n.Y+24)
	}
	v.Bounds = [4]float64{left - 24, top - 24, right - left + 48, bottom - top + 48}
}

type panoPositionRow struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

type panoLinksView struct {
	Node      panoNodeRow              `json:"node"`
	IDs       []string                 `json:"ids"`
	Edges     []panoEdgeRow            `json:"edges"`
	Positions []panoPositionRow        `json:"positions"`
	Bounds    [4]float64               `json:"bounds"`
	Sources   []string                 `json:"sources"`
	Targets   []string                 `json:"targets"`
	Results   map[string][]panoFactRow `json:"results"`
}

// layoutPanoFocus orders the selected paths by their actual direction. Overview
// coordinates stay unchanged; shared inputs precede their outputs in focus.
func layoutPanoFocus(nodes []panoNodeRow, edges []panoEdgeRow, width, height int) *panoSkeletonView {
	if width < 320 {
		width = 1152
	}
	if height < 240 {
		height = 720
	}
	width, height = min(width, 4096), min(height, 2160)
	cols := max(1, min(24, int(math.Ceil(math.Sqrt(float64(len(nodes))*80/192*float64(width)/float64(height))))))
	view := &panoSkeletonView{Nodes: append([]panoNodeRow(nil), nodes...), Edges: append([]panoEdgeRow(nil), edges...)}
	degree, rank := map[string]int{}, map[string]int{}
	children := map[string][]string{}
	for _, edge := range edges {
		degree[edge.To]++
		children[edge.From] = append(children[edge.From], edge.To)
	}
	pending := []string{}
	for _, node := range nodes {
		if degree[node.ID] == 0 {
			pending = append(pending, node.ID)
		}
	}
	for i := 0; i < len(pending); i++ {
		id := pending[i]
		for _, next := range children[id] {
			rank[next] = max(rank[next], rank[id]+1)
			degree[next]--
			if degree[next] == 0 {
				pending = append(pending, next)
			}
		}
	}
	rows := map[float64][]int{}
	for i, n := range view.Nodes {
		level := float64(rank[n.ID])
		if degree[n.ID] > 0 {
			// Historical cycles have no topological order. Keep their original
			// relative rows and real arrow directions without inventing a path.
			level = float64(len(nodes)) + n.Y
		}
		rows[level] = append(rows[level], i)
	}
	levels := make([]float64, 0, len(rows))
	for y := range rows {
		levels = append(levels, y)
	}
	sort.Float64s(levels)
	y := 0.0
	for _, level := range levels {
		ids := rows[level]
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := view.Nodes[ids[i]], view.Nodes[ids[j]]
			if a.X != b.X {
				return a.X < b.X
			}
			return a.ID < b.ID
		})
		for start := 0; start < len(ids); start += cols {
			end := min(start+cols, len(ids))
			count := end - start
			for j, i := range ids[start:end] {
				view.Nodes[i].X = (float64(j) - float64(count-1)/2) * 192
				view.Nodes[i].Y = y
			}
			y += 80
		}
		y += 40
	}
	finishPanoLayout(view)
	for i := range view.Edges {
		view.Edges[i].Dashed = edges[i].Dashed
	}
	return view
}

func (s *Server) handlePanoLinks(w http.ResponseWriter, r *http.Request) {
	task, id := r.PathValue("id"), r.PathValue("nid")
	if !s.taskExists(task) {
		writeErr(w, 404, fmt.Errorf("任务不存在"))
		return
	}
	v, err := s.panoSkeletonOf(r.Context(), task)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ids := v.Aliases[id]
	if len(ids) == 0 {
		if _, err := s.Store.Node(r.Context(), task, id); err != nil {
			writeErr(w, 404, fmt.Errorf("节点不存在"))
		} else {
			writeErr(w, 400, fmt.Errorf("该节点不在全景图中"))
		}
		return
	}
	id = ids[0]
	index := map[string]panoNodeRow{}
	for _, n := range v.Nodes {
		index[n.ID] = n
	}
	node, ok := index[id]
	if !ok {
		writeErr(w, 404, fmt.Errorf("节点不存在"))
		return
	}
	source, target := r.URL.Query().Get("source"), r.URL.Query().Get("target")
	result := panoFocusMembers(v, r.PathValue("nid"), source, target)
	contains := func(ids []string, id string) bool {
		for _, item := range ids {
			if item == id {
				return true
			}
		}
		return false
	}
	if source != "" && !contains(result.Sources, source) {
		writeErr(w, 400, fmt.Errorf("来源分支不存在"))
		return
	}
	if target != "" && !contains(result.Targets, target) {
		writeErr(w, 400, fmt.Errorf("去向分支不存在"))
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("width"))
	height, _ := strconv.Atoi(r.URL.Query().Get("height"))
	members := make([]panoNodeRow, 0, len(result.IDs))
	for _, id := range result.IDs {
		members = append(members, index[id])
	}
	compact := layoutPanoFocus(members, result.Edges, width, height)
	// 局部视野围绕所选节点的原位置展开，保留淡出背景的空间上下文。
	compact.Bounds[0] += node.X
	compact.Bounds[1] += node.Y
	for i := range compact.Nodes {
		compact.Nodes[i].X += node.X
		compact.Nodes[i].Y += node.Y
	}
	result.Edges, result.Bounds = compact.Edges, compact.Bounds
	result.Positions = make([]panoPositionRow, 0, len(compact.Nodes))
	for _, n := range compact.Nodes {
		result.Positions = append(result.Positions, panoPositionRow{ID: n.ID, X: n.X, Y: n.Y})
	}
	writeJSON(w, 200, result)
}

type panoSearchRow struct {
	ID    string `json:"id"`
	T     string `json:"t"`
	Ti    string `json:"ti"`
	Match string `json:"match,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

const panoSearchLimit = 20

func panoNormalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("-_.:/#", r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}
func panoMatch(text, q string) int {
	t := panoNormalize(text)
	if t == q {
		return 0
	}
	if strings.HasPrefix(t, q) {
		return 1
	}
	if strings.Contains(t, q) {
		return 2
	}
	runes := []rune(q)
	i := 0
	for _, r := range t {
		if i < len(runes) && r == runes[i] {
			i++
		}
	}
	if i == len(runes) {
		return 3
	}
	return -1
}
func (s *Server) handlePanoSearch(w http.ResponseWriter, r *http.Request) {
	task := r.PathValue("id")
	if !s.taskExists(task) {
		writeErr(w, 404, fmt.Errorf("任务不存在"))
		return
	}
	q := panoNormalize(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeJSON(w, 200, []panoSearchRow{})
		return
	}
	v, err := s.panoSkeletonOf(r.Context(), task)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	type hit struct {
		row  panoSearchRow
		rank int
	}
	hits := []hit{}
	for _, n := range v.Nodes {
		rank := panoMatch(n.ID, q)
		name := panoMatch(n.Ti, q)
		if rank < 0 || (name >= 0 && name+2 < rank) {
			if name >= 0 {
				rank = name + 2
			}
		}
		match, ref := "", ""
		for _, f := range append(append([]panoFactRow{}, n.Results...), n.Assets...) {
			if p := panoMatch(f.ID+" "+f.Ti, q); p >= 0 {
				score := p + 4
				if panoNormalize(f.ID) == q {
					score = 0
				}
				if rank < 0 || score < rank {
					rank = score
					match, ref = f.ID+" "+f.Ti, ""
					for _, result := range n.Results {
						if result.ID == f.ID {
							ref = f.ID
							break
						}
					}
				}
			}
		}
		if rank >= 0 {
			hits = append(hits, hit{panoSearchRow{ID: n.ID, T: n.T, Ti: n.Ti, Match: match, Ref: ref}, rank})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := []panoSearchRow{}
	for _, h := range hits {
		if len(out) == panoSearchLimit {
			break
		}
		out = append(out, h.row)
	}
	writeJSON(w, 200, out)
}

// Intent detail returns all produced facts, including their original evidence and
// status. The drawer labels unsettled results; no model is involved in reading.
func (s *Server) handlePanoDetail(w http.ResponseWriter, r *http.Request) {
	task, id := r.PathValue("id"), r.PathValue("nid")
	if !s.taskExists(task) {
		writeErr(w, 404, fmt.Errorf("任务不存在"))
		return
	}
	n, err := s.Store.Node(r.Context(), task, id)
	if err != nil {
		writeErr(w, 404, fmt.Errorf("节点不存在"))
		return
	}
	if n.Type != rings.NodeStep {
		writeErr(w, 400, fmt.Errorf("该节点不是意图"))
		return
	}
	facts, err := s.Store.NodesByType(r.Context(), task, rings.NodeFact, "")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	byID := map[string]rings.NodeFull{}
	for _, f := range facts {
		byID[f.ID] = f
	}
	edges, err := s.Store.Edges(r.Context(), task, rings.EdgeProduces, rings.EdgeSourcedFrom, rings.EdgeDependsOn, rings.EdgeCovers)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	results := []map[string]any{}
	selectedFacts := map[string]bool{}
	scopedFacts := r.URL.Query().Has("facts")
	for _, fact := range strings.Split(r.URL.Query().Get("facts"), ",") {
		selectedFacts[fact] = true
	}
	sources := []map[string]any{}
	seen := map[string]bool{}
	for _, e := range edges {
		if e.From != id {
			continue
		}
		if e.Type == rings.EdgeProduces {
			if f, ok := byID[e.To]; ok && (!scopedFacts || selectedFacts[f.ID]) {
				results = append(results, panoDetailJSON(f))
			}
			continue
		}
		key := string(e.Type) + e.To
		if seen[key] {
			continue
		}
		seen[key] = true
		if source, e2 := s.Store.Node(r.Context(), task, e.To); e2 == nil {
			sources = append(sources, map[string]any{"id": source.ID, "type": source.Type, "title": source.Title, "relation": e.Type})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i]["created_rev"].(int64) < results[j]["created_rev"].(int64) })
	intent := panoDetailJSON(*n)
	if polish, err := s.Store.NodePolish(r.Context(), task); err == nil {
		intent["polished_title"], intent["polished_statement"] = polish[n.ID].Title, polish[n.ID].Statement
		for _, result := range results {
			p := polish[result["id"].(string)]
			result["polished_title"], result["polished_statement"] = p.Title, p.Statement
		}
		for _, source := range sources {
			source["polished_title"] = polish[source["id"].(string)].Title
		}
	}
	if unit, session, ok, err := s.Store.StepLatestSession(r.Context(), task, id); err == nil && ok {
		intent["unit_id"], intent["transcript_id"] = unit, session
	}
	writeJSON(w, 200, map[string]any{"intent": intent, "results": results, "sources": sources})
}

func panoDetailJSON(n rings.NodeFull) map[string]any {
	return map[string]any{"id": n.ID, "type": n.Type, "title": n.Title, "status": n.Status, "detail": n.Detail, "created_rev": n.CreatedRev, "added_at": n.AddedAt, "confirmed_at": n.ConfirmedAt}
}

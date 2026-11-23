package rings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// AssetRelation is an explicit observation attached to a fact. It does not
// change containment, provenance, execution dependencies or scope authority.
type AssetRelation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type AssetRelationView struct {
	AssetRelation
	Label     string   `json:"label"`
	FromTitle string   `json:"from_title"`
	ToTitle   string   `json:"to_title"`
	FactIDs   []string `json:"fact_ids"`
}

func AssetRelationWord(kind string) string {
	return map[string]string{"resolves_to": "解析到", "hosted_on": "部署在", "related_to": "关联", "redirects_to": "跳转到", "same_system": "同一系统", "address": "入口地址"}[kind]
}

func (r AssetRelationView) Summary() string {
	proof := r.FactIDs
	if len(proof) > 3 {
		proof = proof[:3]
	}
	line := fmt.Sprintf("%s %s %s (依据事实: %s)", r.From, AssetRelationWord(r.Kind), r.To, strings.Join(proof, ","))
	if len(r.FactIDs) > 3 {
		line += fmt.Sprintf("，共%d条依据", len(r.FactIDs))
	}
	return line
}

func AssetRelationsFromDetail(detail map[string]any) ([]AssetRelation, error) {
	raw, ok := detail["asset_relations"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var relations []AssetRelation
	if err := json.Unmarshal(b, &relations); err != nil {
		return nil, fmt.Errorf("资产关系须为关系列表")
	}
	if len(relations) > 64 {
		return nil, fmt.Errorf("一条事实最多提交64条资产关系")
	}
	if len(relations) > 0 {
		if inferred, _ := detail["inferred"].(bool); inferred {
			return nil, fmt.Errorf("推断内容不能确认资产关系")
		}
		evidence, _ := detail["evidence"].(string)
		ref, _ := detail["evidence_ref"].(string)
		if strings.TrimSpace(evidence) == "" && strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("资产关系缺少观察证据")
		}
	}
	seen := map[AssetRelation]bool{}
	var out []AssetRelation
	for _, r := range relations {
		r.From, r.To, r.Kind = strings.TrimSpace(r.From), strings.TrimSpace(r.To), strings.TrimSpace(r.Kind)
		if r.From == "" || r.To == "" || r.From == r.To {
			return nil, fmt.Errorf("资产关系两端须为不同资产")
		}
		if AssetRelationWord(r.Kind) == "" || r.Kind == "address" {
			return nil, fmt.Errorf("资产关系类型无效")
		}
		if (r.Kind == "related_to" || r.Kind == "same_system") && r.From > r.To {
			r.From, r.To = r.To, r.From
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, nil
}

type assetRelationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateAssetRelationAssets(ctx context.Context, reader assetRelationReader, task string, detail map[string]any) error {
	relations, err := AssetRelationsFromDetail(detail)
	if err != nil {
		return err
	}
	for _, r := range relations {
		resolved := []string{r.From, r.To}
		for i, id := range []string{r.From, r.To} {
			if id == "$new" {
				decl, _ := detail["new_asset"].(map[string]any)
				title, _ := decl["title"].(string)
				class, _ := decl["class"].(string)
				if strings.TrimSpace(title) == "" || !IsValidAssetClass(AssetClass(class)) {
					return fmt.Errorf("$new须对应本条事实申报的新资产")
				}
				var found, status string
				err := reader.QueryRowContext(ctx, `SELECT node_id,status FROM nodes WHERE task_id=? AND node_type='asset' AND title=? ORDER BY created_rev LIMIT 1`, task, strings.TrimSpace(title)).Scan(&found, &status)
				if err == nil {
					if status == AssetStatusRetired {
						return fmt.Errorf("新资产标识对应已退役对象")
					}
					resolved[i] = found
				} else if err != sql.ErrNoRows {
					return err
				}
				continue
			}
			var kind, status string
			if err := reader.QueryRowContext(ctx, `SELECT node_type,status FROM nodes WHERE task_id=? AND node_id=?`, task, id).Scan(&kind, &status); err != nil || kind != string(NodeAsset) || status == AssetStatusRetired {
				return fmt.Errorf("关系端点%s不是本任务的有效资产", id)
			}
		}
		if resolved[0] == resolved[1] {
			return fmt.Errorf("资产关系不能指向同一个对象")
		}
	}
	return nil
}

func (s *Store) ValidateAssetRelations(ctx context.Context, task string, detail map[string]any) error {
	return validateAssetRelationAssets(ctx, s.db, task, detail)
}

// Bind a new-object reference once at confirmation. Subsequent title changes
// must not change which asset the original observation refers to.
func validateObservedAssetBinding(tx *sql.Tx, task, fact, asset string) error {
	var status, raw string
	if err := tx.QueryRow(`SELECT status,detail FROM nodes WHERE task_id=? AND node_id=?`, task, fact).Scan(&status, &raw); err != nil {
		return err
	}
	if status != "confirmed" {
		return fmt.Errorf("观察对象须由已确认事实绑定")
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return err
	}
	relations, err := AssetRelationsFromDetail(detail)
	if err != nil {
		return err
	}
	usesNew := false
	for _, r := range relations {
		usesNew = usesNew || r.From == "$new" || r.To == "$new"
	}
	decl, _ := detail["new_asset"].(map[string]any)
	title, _ := decl["title"].(string)
	class, _ := decl["class"].(string)
	if !usesNew || strings.TrimSpace(title) == "" || !IsValidAssetClass(AssetClass(class)) {
		return fmt.Errorf("事实未申报关联的新资产")
	}
	var actualTitle, actualClass, assetStatus string
	if err := tx.QueryRow(`SELECT title,asset_class,status FROM nodes WHERE task_id=? AND node_id=?`, task, asset).Scan(&actualTitle, &actualClass, &assetStatus); err != nil {
		return err
	}
	_, _, declaredKey, declaredErr := AssetIdentity(AssetClass(class), title)
	_, _, actualKey, actualErr := AssetIdentity(AssetClass(actualClass), actualTitle)
	if assetStatus == AssetStatusRetired || (strings.TrimSpace(actualTitle) != strings.TrimSpace(title) && (declaredErr != nil || actualErr != nil || declaredKey != actualKey)) {
		return fmt.Errorf("观察对象与申报的新资产不符")
	}
	var bound string
	err = tx.QueryRow(`SELECT to_node FROM edges WHERE task_id=? AND edge_type=? AND from_node=?`, task, string(EdgeObservedAsset), fact).Scan(&bound)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && bound != asset {
		return fmt.Errorf("事实的观察对象不可重新绑定")
	}
	for _, r := range relations {
		if (r.From == "$new" && r.To == asset) || (r.To == "$new" && r.From == asset) {
			return fmt.Errorf("资产关系不能指向同一个对象")
		}
	}
	return nil
}

// Relations are projected from confirmed observations, not inferred from names
// or co-occurrence. Multiple supporting facts remain independently traceable.
func queryAssetRelations(ctx context.Context, tx *sql.Tx, task string, assets []AssetNodeView, aliasMaps ...map[string]string) ([]AssetRelationView, error) {
	byID := map[string]AssetNodeView{}
	for _, a := range assets {
		byID[a.ID] = a
	}
	bindings := map[string]string{}
	boundRows, err := tx.QueryContext(ctx, `SELECT from_node,to_node FROM edges WHERE task_id=? AND edge_type=?`, task, string(EdgeObservedAsset))
	if err != nil {
		return nil, err
	}
	for boundRows.Next() {
		var fact, id string
		if err := boundRows.Scan(&fact, &id); err != nil {
			boundRows.Close()
			return nil, err
		}
		bindings[fact] = id
	}
	if err := boundRows.Err(); err != nil {
		boundRows.Close()
		return nil, err
	}
	boundRows.Close()
	rows, err := tx.QueryContext(ctx, `SELECT node_id,detail FROM nodes WHERE task_id=? AND node_type='fact' AND status='confirmed' AND (json_type(detail,'$.asset_relations')='array' OR json_type(detail,'$.asset_observations')='object') ORDER BY created_rev,node_id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identityIDs := map[string]string{}
	for _, a := range assets {
		_, _, key, e := AssetIdentity(a.AssetClass, a.Title)
		if e == nil {
			identityIDs[key] = a.ID
		}
	}
	index := map[AssetRelation]int{}
	out := []AssetRelationView{}
	for rows.Next() {
		var fid, raw string
		if err := rows.Scan(&fid, &raw); err != nil {
			return nil, err
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			return nil, err
		}
		relations, err := AssetRelationsFromDetail(detail)
		if err != nil {
			return nil, err
		}
		observations, err := ObservedAssets(detail)
		if err != nil {
			return nil, err
		}
		for _, r := range observations.Relations {
			_, _, from, _ := AssetIdentity(r.FromClass, r.From)
			_, _, to, _ := AssetIdentity(r.ToClass, r.To)
			relations = append(relations, AssetRelation{From: identityIDs[from], To: identityIDs[to], Kind: r.Kind})
		}
		for _, r := range relations {
			if r.From == "$new" {
				r.From = bindings[fid]
			}
			if r.To == "$new" {
				r.To = bindings[fid]
			}
			if len(aliasMaps) > 0 {
				if id := aliasMaps[0][r.From]; id != "" {
					r.From = id
				}
				if id := aliasMaps[0][r.To]; id != "" {
					r.To = id
				}
			}
			from, ok := byID[r.From]
			if !ok {
				continue
			}
			to, ok := byID[r.To]
			if !ok || r.From == r.To {
				continue
			}
			if (r.Kind == "related_to" || r.Kind == "same_system") && r.From > r.To {
				r.From, r.To = r.To, r.From
				from, to = to, from
			}
			if i, ok := index[r]; ok {
				out[i].FactIDs = append(out[i].FactIDs, fid)
			} else {
				index[r] = len(out)
				out = append(out, AssetRelationView{AssetRelation: r, Label: AssetRelationWord(r.Kind), FromTitle: from.Title, ToTitle: to.Title, FactIDs: []string{fid}})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	addresses := map[string]string{}
	for _, a := range assets {
		if c, title, _, e := AssetIdentity(a.AssetClass, a.Title); e == nil && (c == AssetDomain || c == AssetHost) {
			addresses[title] = a.ID
		}
	}
	for _, a := range assets {
		if a.AssetClass != AssetService {
			continue
		}
		_, address, _, e := AssetIdentity(AssetService, a.Title)
		if e != nil {
			continue
		}
		host, _, _ := net.SplitHostPort(address)
		if u, e := url.Parse(address); e == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		if id := addresses[host]; id != "" && id != a.ID {
			out = append(out, AssetRelationView{AssetRelation: AssetRelation{From: a.ID, To: id, Kind: "address"}, Label: AssetRelationWord("address"), FromTitle: a.Title, ToTitle: byID[id].Title, FactIDs: []string{}})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Kind < b.Kind
	})
	return out, nil
}

// BindObservedAssets associates joined identities with their original
// observations, including discoveries made before the membership decision.
// Passing nil observations reads confirmed facts once for the whole batch.
func (s *Store) BindObservedAssets(ctx context.Context, task string, assets []NodeAdded, observations []NodeFull, actor string) ([]Event, error) {
	identities := map[string]string{}
	for _, a := range assets {
		_, _, key, err := AssetIdentity(a.AssetClass, a.Title)
		if err == nil {
			identities[key] = a.NodeID
		}
	}
	if len(identities) == 0 {
		return nil, nil
	}
	if observations == nil {
		var err error
		observations, err = s.NodesByType(ctx, task, NodeFact, "confirmed")
		if err != nil {
			return nil, err
		}
	}
	edges, err := s.Edges(ctx, task, EdgeAbout, EdgeObservedAsset)
	if err != nil {
		return nil, err
	}
	seen := map[[3]string]bool{}
	for _, e := range edges {
		seen[[3]string{string(e.Type), e.From, e.To}] = true
	}
	events := []Event{}
	for _, f := range observations {
		decl, _ := f.Detail["new_asset"].(map[string]any)
		class, _ := decl["class"].(string)
		title, _ := decl["title"].(string)
		_, _, declaredKey, _ := AssetIdentity(AssetClass(class), title)
		observed, err := ObservedAssets(f.Detail)
		if err != nil {
			return nil, err
		}
		observed.Assets = append(observed.Assets, AssetObservation{Class: AssetClass(class), Title: title})
		entries := append([]AssetObservation{}, observed.Assets...)
		for _, a := range entries {
			if class, host, e := AssetAddress(a.Class, a.Title); e == nil {
				observed.Assets = append(observed.Assets, AssetObservation{Class: class, Title: host})
			}
			if a.Class == AssetAPI {
				address := AssetEntryAddress(a.Title)
				observed.Assets = append(observed.Assets, AssetObservation{Class: AssetService, Title: AssetDisplayTitle(AssetService, address)})
			}
		}

		for _, observation := range observed.Assets {
			_, _, key, e := AssetIdentity(observation.Class, observation.Title)
			id := identities[key]
			if e != nil || id == "" {
				continue
			}

			types := []EdgeType{EdgeAbout}
			relations, _ := AssetRelationsFromDetail(f.Detail)
			for _, r := range relations {
				if key == declaredKey && (r.From == "$new" || r.To == "$new") {
					types = append(types, EdgeObservedAsset)
					break
				}
			}
			for _, kind := range types {
				k := [3]string{string(kind), f.ID, id}
				if seen[k] {
					continue
				}
				seen[k] = true
				events = append(events, Event{Kind: EventEdgeAdded, Actor: actor, Payload: EdgeAdded{EdgeType: kind, FromNode: f.ID, ToNode: id}})
			}
		}
	}
	return events, nil
}

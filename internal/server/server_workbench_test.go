package server

import (
	"reflect"
	"sort"
	"testing"
)

func TestWorkbenchProjectionMatchesFullGraph(t *testing.T) {
	h, task, _ := graphSetup(t)
	for _, id := range []string{task, "missing"} {
		if id == "missing" {
			get(t, h, "/api/tasks/"+id+"/workbench", 404, nil)
			continue
		}
		var full taskGraphJSON
		get(t, h, "/api/tasks/"+id+"/graph", 200, &full)
		var summary struct {
			Nodes []graphNodeJSON `json:"nodes"`
			Edges []graphEdgeJSON `json:"edges"`
		}
		get(t, h, "/api/tasks/"+id+"/workbench", 200, &summary)
		wantNodes, wantEdges := []graphNodeJSON{}, []graphEdgeJSON{}
		for _, n := range full.Nodes {
			if n.Type == "goal" || n.Type == "step" || n.Type == "asset" {
				wantNodes = append(wantNodes, n)
			}
		}
		for _, e := range full.Edges {
			if e.Type == "goal" {
				wantEdges = append(wantEdges, e)
			}
		}
		less := func(edges []graphEdgeJSON) {
			sort.Slice(edges, func(i, j int) bool { return edges[i].From+"\x00"+edges[i].To < edges[j].From+"\x00"+edges[j].To })
		}
		less(summary.Edges)
		less(wantEdges)
		if !reflect.DeepEqual(summary.Nodes, wantNodes) || !reflect.DeepEqual(summary.Edges, wantEdges) {
			t.Fatal("工作台丢失原KPI/目标步骤/资产覆盖口径")
		}
		var raw map[string]any
		get(t, h, "/api/tasks/"+id+"/workbench", 200, &raw)
		if len(raw) != 2 {
			t.Fatal("摘要不应重复水位或整图元数据")
		}
	}
	var created map[string]string
	post(t, h, "POST", "/api/tasks", `{"title":"空目标摘要","task_type":"redteam","goals":["目标"],"assets":[{"class":"domain","title":"empty.example.test"}]}`, 200, &created)
	var full taskGraphJSON
	get(t, h, "/api/tasks/"+created["id"]+"/workbench", 200, &full)
	if len(full.Edges) != 0 {
		t.Fatal("无步骤不能推造目标边")
	}
	for _, n := range full.Nodes {
		if n.Type != "goal" && n.Type != "asset" {
			t.Fatal("空任务摘要不应混入其他任务")
		}
	}
}

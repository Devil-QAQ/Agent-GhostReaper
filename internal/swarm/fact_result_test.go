package swarm

import (
	"context"
	"ghostreaper/internal/rings"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFactResultRecordedThroughExistingSubmission(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "fact-result-submit"
	seedGraph(t, store, task, "记录实际观察")
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	cases := []struct {
		statement string
		kinds     []rings.FactResultType
		want      []rings.FactResultType
	}{
		{"/admin/ 返回404，没有取得权限", []rings.FactResultType{rings.FactResultObservation}, []rings.FactResultType{rings.FactResultObservation}},
		{"取得后台口令和812条用户数据", []rings.FactResultType{rings.FactResultData, rings.FactResultCredential}, []rings.FactResultType{rings.FactResultCredential, rings.FactResultData}},
		{"旧调用者未填写类型", nil, []rings.FactResultType{rings.FactResultUnclassified}},
	}
	for _, tc := range cases {
		id, err := env.SubmitCandidateFact(ctx, tc.statement, "本次原始证据", "target", nil, tc.kinds...)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := store.Node(ctx, task, id)
		if got := rings.FactResultsOf(*n); !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, tc.want)
		}
	}

}

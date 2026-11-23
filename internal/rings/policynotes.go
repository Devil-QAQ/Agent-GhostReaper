package rings

import (
	"fmt"
	"strings"
)

// PolicyNotes presents existing operator constraints to the single semantic
// action reviewer. Structured historical rules remain data, not a second
// command classifier or an automatic permission/counter mechanism.
func PolicyNotes(notes string, rules []IntentRule) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(notes))
	for _, r := range rules {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		verb := "允许"
		if r.Disposition == "forbid" {
			verb = "禁止"
		}
		scope := "全任务"
		if len(r.AssetIDs) > 0 {
			scope = strings.Join(r.AssetIDs, "、")
		}
		fmt.Fprintf(&b, "原有约束：%s%s；范围：%s", verb, r.Action, scope)
		if r.Quota > 0 {
			fmt.Fprintf(&b, "；次数上限%d，次数未经完整核对时须审批", r.Quota)
		}
		if n := strings.TrimSpace(r.Note); n != "" {
			b.WriteString("；" + n)
		}
	}
	return b.String()
}

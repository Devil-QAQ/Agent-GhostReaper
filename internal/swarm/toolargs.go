package swarm

import "encoding/json"

func bashCommandOf(input json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	return args.Command
}

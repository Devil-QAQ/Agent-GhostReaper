package rings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Extracted identities retain the observation that justified registration.
// They never contribute authorization and never replace the original evidence.
type AssetObservation struct {
	Class    AssetClass `json:"class"`
	Title    string     `json:"title"`
	Evidence string     `json:"evidence"`
}

type AssetObservationLink struct {
	FromClass AssetClass `json:"from_class"`
	From      string     `json:"from"`
	ToClass   AssetClass `json:"to_class"`
	To        string     `json:"to"`
	Kind      string     `json:"kind"`
	Evidence  string     `json:"evidence"`
}

type AssetObservations struct {
	Assets    []AssetObservation     `json:"assets"`
	Relations []AssetObservationLink `json:"relations,omitempty"`
}

func ObservedAssets(detail map[string]any) (AssetObservations, error) {
	var out AssetObservations
	if raw, exists := detail["asset_observations"]; exists {
		b, err := json.Marshal(raw)
		if err != nil {
			return out, err
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return out, err
		}
		if len(out.Assets) > 256 || len(out.Relations) > 64 {
			return out, fmt.Errorf("资产观察数量过多")
		}
		for _, a := range out.Assets {
			if _, _, _, err := AssetIdentity(a.Class, a.Title); err != nil || strings.TrimSpace(a.Evidence) == "" {
				return out, fmt.Errorf("资产观察缺少有效对象或证据")
			}
		}
		links := out.Relations[:0]
		for _, r := range out.Relations {
			_, _, from, ef := AssetIdentity(r.FromClass, r.From)
			_, _, to, et := AssetIdentity(r.ToClass, r.To)
			if ef == nil && et == nil && from == to {
				continue
			}
			if ef != nil || et != nil || r.Kind == "address" || AssetRelationWord(r.Kind) == "" || strings.TrimSpace(r.Evidence) == "" {
				return out, fmt.Errorf("资产关系观察无效")
			}
			links = append(links, r)
		}
		out.Relations = links
	}
	return out, nil
}

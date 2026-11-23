package rings

import "fmt"

// FactResultType describes an actual result, supplied with the evidence rather
// than inferred from words in a title. A fact may contain several result types.
type FactResultType string

const (
	FactResultCredential   FactResultType = "credential"
	FactResultData         FactResultType = "data"
	FactResultPermission   FactResultType = "permission"
	FactResultFoothold     FactResultType = "foothold"
	FactResultObservation  FactResultType = "plain"
	FactResultUnclassified FactResultType = "unclassified"
)

var factResultOrder = []FactResultType{FactResultCredential, FactResultData, FactResultPermission, FactResultFoothold, FactResultObservation, FactResultUnclassified}

func ValidFactResult(t FactResultType) bool {
	for _, kind := range factResultOrder {
		if t == kind {
			return true
		}
	}
	return false
}

// NormalizeFactResults makes result sets stable without imposing a business
// priority or discarding other outcomes. No supplied classification is unknown.
func NormalizeFactResults(values []FactResultType) []FactResultType {
	seen := map[FactResultType]bool{}
	for _, v := range values {
		seen[v] = true
	}
	if len(seen) == 0 {
		return []FactResultType{FactResultUnclassified}
	}
	out := []FactResultType{}
	for _, v := range factResultOrder {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out
}
func factResultsFromDetail(detail map[string]any) ([]FactResultType, error) {
	value, ok := detail["result_types"]
	if !ok {
		return nil, nil
	}
	var values []FactResultType
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			word, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("事实结果类型必须是文字")
			}
			values = append(values, FactResultType(word))
		}
	case []string:
		for _, word := range v {
			values = append(values, FactResultType(word))
		}
	case []FactResultType:
		values = v
	default:
		return nil, fmt.Errorf("事实结果类型必须是列表")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("事实结果类型为空；拿不准应记为未分类")
	}
	for _, kind := range values {
		if !ValidFactResult(kind) {
			return nil, fmt.Errorf("事实结果类型无效")
		}
	}
	return NormalizeFactResults(values), nil
}
func ValidateFactResult(detail map[string]any) error {
	if inferred, _ := detail["inferred"].(bool); inferred {
		return fmt.Errorf("事实只接收实际观察及证据，未验证的猜测不能提交")
	}
	values, err := factResultsFromDetail(detail)
	if err != nil {
		return err
	}
	for _, kind := range values {
		if len(values) > 1 && (kind == FactResultObservation || kind == FactResultUnclassified) {
			return fmt.Errorf("普通观察或未分类不能与其他结果类型并列")
		}
	}
	return nil
}

// FactResultsOf keeps old records visibly unclassified. Display rewrites cannot
// alter the category, statistics or filters. Invalid historical values are not guessed.
func FactResultsOf(n NodeFull) []FactResultType {
	if n.Type != NodeFact || ValidateFactResult(n.Detail) != nil {
		return []FactResultType{FactResultUnclassified}
	}
	values, _ := factResultsFromDetail(n.Detail)
	return NormalizeFactResults(values)
}

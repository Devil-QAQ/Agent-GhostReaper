package server

import (
	"fmt"
	"ghostreaper/internal/rings"
	"strings"
)

// Network inputs share identity with discovery and adoption. Deliberate legacy
// non-network seeds remain explicit operator input, never automatic discoveries.
func normalizeAssetInput(a assetIn) (assetIn, string, error) {
	a.Title = strings.TrimSpace(a.Title)
	class := rings.AssetClass(strings.TrimSpace(a.Class))
	if a.Title == "" || !assetClassWords[class] {
		return a, "", fmt.Errorf("资产标识或类别无效")
	}
	switch class {
	case rings.AssetDomain, rings.AssetSubdomain, rings.AssetHost, rings.AssetService, rings.AssetAPI, rings.AssetApp, rings.AssetMiniProgram:
		c, title, key, err := rings.AssetIdentity(class, a.Title)
		if err != nil {
			return a, "", err
		}
		a.Class, a.Title = string(c), title
		return a, key, nil
	default:
		a.Class = string(class)
		return a, string(class) + ":" + a.Title, nil
	}
}

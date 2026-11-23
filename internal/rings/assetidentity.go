package rings

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// NetworkAssetIdentity separates address identity from labels and evidence.
// Subdomains are domains; IPs identify addresses, not physical machines. Paths
// describe an endpoint on a service and do not create another service identity.
// The supplied URL is preserved as the address (including its path), so naming
// normalization does not broaden the operator's written target.
func NetworkAssetIdentity(class AssetClass, title string) (AssetClass, string, string, error) {
	title = strings.TrimSpace(title)
	switch class {
	case AssetDomain, AssetSubdomain:
		wild := strings.HasPrefix(title, "*.")
		host := strings.TrimSuffix(strings.TrimPrefix(title, "*."), ".")
		if ip, err := netip.ParseAddr(host); err == nil && !wild {
			return AssetHost, ip.Unmap().String(), "ip:" + ip.Unmap().String(), nil
		}
		host, err := idna.Lookup.ToASCII(strings.ToLower(host))
		if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, "/:@?# \t\n\r") {
			return "", "", "", fmt.Errorf("域名标识无效")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", "", "", fmt.Errorf("域名标识无效")
			}
		}
		if wild {
			host = "*." + host
		}
		return AssetDomain, host, "domain:" + host, nil
	case AssetHost:
		addr, err := netip.ParseAddr(strings.Trim(title, "[]"))
		if err != nil {
			return "", "", "", fmt.Errorf("IP标识无效")
		}
		addr = addr.Unmap()
		return AssetHost, addr.String(), "ip:" + addr.String(), nil
	case AssetService, AssetAPI:
		if !strings.Contains(title, "://") {
			host, port, err := net.SplitHostPort(title)
			if err != nil {
				return "", "", "", fmt.Errorf("服务入口须为完整URL或地址:端口")
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return "", "", "", fmt.Errorf("服务入口端口无效")
			}
			_, normalized, _, err := NetworkAssetIdentity(AssetDomain, host)
			if err != nil || strings.HasPrefix(normalized, "*.") {
				return "", "", "", fmt.Errorf("服务入口地址无效")
			}
			address := net.JoinHostPort(normalized, strconv.Itoa(n))
			return AssetService, address, "service:" + address, nil
		}
		u, err := url.Parse(title)
		if err != nil || u.Scheme == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
			return "", "", "", fmt.Errorf("服务入口须包含协议和地址，且不含登录凭据")
		}
		host := strings.ToLower(u.Hostname())
		if addr, err := netip.ParseAddr(host); err == nil {
			host = addr.Unmap().String()
		} else {
			_, host, _, err = NetworkAssetIdentity(AssetDomain, host)
			if err != nil || strings.HasPrefix(host, "*.") {
				return "", "", "", fmt.Errorf("服务入口地址无效")
			}
		}
		port := u.Port()
		if port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return "", "", "", fmt.Errorf("服务入口端口无效")
			}
			port = strconv.Itoa(n)
		}
		scheme := strings.ToLower(u.Scheme)
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
		address := host
		if port != "" {
			address = net.JoinHostPort(host, port)
		} else if strings.Contains(host, ":") {
			address = "[" + host + "]"
		}
		u.Scheme, u.Host = scheme, address
		return AssetService, u.String(), "service:" + scheme + "://" + address, nil
	default:
		return "", "", "", fmt.Errorf("该内容作为资料保留，不是网络资产")
	}
}

// AssetIdentity also preserves explicitly identified independent applications.
// Background organizations, people and files remain observations.
func AssetIdentity(class AssetClass, title string) (AssetClass, string, string, error) {
	if class == AssetApp || class == AssetMiniProgram {
		title = strings.TrimSpace(title)
		if title == "" || strings.ContainsAny(title, "\r\n\x00") {
			return "", "", "", fmt.Errorf("应用标识无效")
		}
		return class, title, string(class) + ":" + title, nil
	}
	return NetworkAssetIdentity(class, title)
}

func DisplayAssetClass(class AssetClass) AssetClass {
	switch class {
	case AssetSubdomain:
		return AssetDomain
	case AssetAPI:
		return AssetService
	}
	return class
}

// AssetDisplayTitle names a service by its entry, while Title and evidence keep
// the original URL/path and therefore the operator's exact written scope.
func AssetDisplayTitle(class AssetClass, title string) string {
	if c, _, key, err := AssetIdentity(class, title); err == nil && c == AssetService {
		return strings.TrimPrefix(key, "service:")
	}
	return title
}

// AssetDisplayHierarchy organizes only admitted identities. Domain suffixes
// and a service's literal hostname describe address structure; DNS/deployment
// observations never become containment. This is a read projection, not a
// ledger mutation, permission change, or discovery of a missing parent.
func AssetDisplayHierarchy(assets []NodeFull, edges []EdgeView, members map[string]bool) []EdgeView {
	byID := map[string]NodeFull{}
	byAddress := map[string][]string{}
	for _, a := range assets {
		if !members[a.ID] {
			continue
		}
		byID[a.ID] = a
		if c, title, _, err := NetworkAssetIdentity(a.AssetClass, a.Title); err == nil && (c == AssetDomain || c == AssetHost) {
			byAddress[title] = append(byAddress[title], a.ID)
		}
	}
	parents := map[string]EdgeView{}
	for _, e := range edges {
		child, c := byID[e.From]
		parent, p := byID[e.To]
		if e.Type != EdgeSubOf || !c || !p || ValidateAssetParent(child.AssetClass, child.Title, parent.AssetClass, parent.Title) != nil {
			continue
		}
		prior, exists := parents[e.From]
		if !exists || len(parent.Title) > len(byID[prior.To].Title) || len(parent.Title) == len(byID[prior.To].Title) && e.To < prior.To {
			parents[e.From] = e
		}
	}
	for id, a := range byID {
		if _, exists := parents[id]; exists {
			continue
		}
		class, title, _, err := NetworkAssetIdentity(a.AssetClass, a.Title)
		if err != nil {
			continue
		}
		address := ""
		switch class {
		case AssetDomain:
			parts := strings.Split(strings.TrimPrefix(title, "*."), ".")
			for i := 1; i < len(parts); i++ {
				candidate := strings.Join(parts[i:], ".")
				if len(byAddress[candidate]) == 1 {
					address = candidate
					break
				}
			}
		case AssetService:
			if u, err := url.Parse(title); err == nil && u.Hostname() != "" {
				address = u.Hostname()
			} else {
				address, _, _ = net.SplitHostPort(title)
			}
		}
		if ids := byAddress[address]; len(ids) == 1 && ids[0] != id {
			parents[id] = EdgeView{Type: EdgeSubOf, From: id, To: ids[0]}
		}
	}
	out := make([]EdgeView, 0, len(parents))
	for _, e := range parents {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// ValidateAssetParent checks an explicitly supplied containment relation. It
// never infers ownership from a shared IP, DNS answer, or a similar name.
func ValidateAssetParent(childClass AssetClass, childTitle string, parentClass AssetClass, parentTitle string) error {
	childClass, child, _, childErr := NetworkAssetIdentity(childClass, childTitle)
	parentClass, parent, _, parentErr := NetworkAssetIdentity(parentClass, parentTitle)
	if childErr == nil && parentErr == nil {
		if childClass == AssetDomain && parentClass == AssetDomain && child != parent && strings.HasSuffix(strings.TrimPrefix(child, "*."), "."+strings.TrimPrefix(parent, "*.")) {
			return nil
		}
		if childClass == AssetService && (parentClass == AssetDomain || parentClass == AssetHost) {
			host, _, err := net.SplitHostPort(child)
			if strings.Contains(child, "://") {
				if u, e := url.Parse(child); e == nil {
					host, err = u.Hostname(), nil
				}
			}
			if err == nil && host == parent {
				return nil
			}
		}
	}
	return fmt.Errorf("父子层级须为所属域名或服务所在地址；解析、部署及一般关联请记录资产关系")
}

// InventoryAssetIDs is the shared membership rule for planning and current
// asset views. It does not mutate historical nodes, permissions or evidence.
// Old explicit exclusions stay exclusions; they are not rewritten as declines.
func InventoryAssetIDs(assets []NodeFull, roe RoeState) map[string]bool {
	excluded := ExcludedCoveredAssetIDs(assets, roe.ExcludedAssetSet())
	approved := roe.ScopeApprovedSet()
	out := make(map[string]bool, len(assets))
	for _, a := range assets {
		if a.Type != NodeAsset || a.Status == AssetStatusRetired || excluded[a.ID] {
			continue
		}
		if AssetSourceOf(a.Detail) == AssetSourceDiscovered {
			switch DisplayAssetClass(a.AssetClass) {
			case AssetDomain, AssetHost, AssetService, AssetApp, AssetMiniProgram:
			default:
				continue
			}
			adopted, _ := a.Detail["adopted"].(bool)
			if !adopted && AssetScopeOf(a.Detail) == AssetScopeOut && !approved[a.ID] {
				continue
			}
		}
		out[a.ID] = true
	}
	return out
}

// AssetJoinEvents records the consequences of an explicit manual join. A
// pending choice for that same identity is settled, with no access decision.
func (s *Store) AssetJoinEvents(ctx context.Context, task string, assets []NodeAdded, actor string) ([]Event, error) {
	events, err := s.BindObservedAssets(ctx, task, assets, nil, actor)
	if err != nil {
		return nil, err
	}
	identities := map[string]bool{}
	for _, a := range assets {
		_, _, key, e := AssetIdentity(a.AssetClass, a.Title)
		if e == nil {
			identities[key] = true
		}
	}
	pending, err := s.PendingAssetProposals(ctx, task)
	if err != nil {
		return nil, err
	}
	for _, row := range pending {
		var p struct {
			Class string `json:"class"`
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(row.ToolArgs), &p) != nil {
			continue
		}
		_, _, key, e := AssetIdentity(AssetClass(p.Class), p.Title)
		if e == nil && identities[key] {
			events = append(events, Event{Kind: EventApprovalRecord, Actor: actor, Payload: ApprovalRecord{RequestID: row.RequestID, UnitID: row.UnitID, Type: ApprovalRecAssetAdded, ToolArgs: json.RawMessage(row.ToolArgs), Intent: row.Intent, DecidedBy: actor, Reason: "手动加入资产"}})
		}
	}
	return events, nil
}

package websearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"ghostreaper/internal/rings"
)

const SettingKeyServices = "search.services"

var (
	ErrServiceMissing  = errors.New("搜索服务不存在")
	ErrNotConfigured   = errors.New("尚未配置可用搜索服务，可在能力库的搜索配置中添加，也可继续其他资料获取方式")
	ErrServiceRequired = errors.New("存在多个可用搜索服务，请选择服务编号")
	servicesMu         sync.Mutex
)

type Service struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Config
}

type ServiceChange struct {
	ID      string `json:"-"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Config
	APIKey      *string      `json:"api_key,omitempty"`
	Proxy       *string      `json:"proxy,omitempty"`
	ProxyConfig *ProxyChange `json:"proxy_config,omitempty"`
}

func Services(ctx context.Context, store *rings.Store) ([]Service, error) {
	servicesMu.Lock()
	defer servicesMu.Unlock()
	return servicesLocked(ctx, store)
}

func servicesLocked(ctx context.Context, store *rings.Store) ([]Service, error) {
	if store == nil {
		return []Service{}, nil
	}
	entry, exists, err := store.Setting(ctx, SettingKeyServices)
	if err != nil {
		return nil, errors.New("搜索配置无法读取")
	}
	if exists {
		var list []Service
		if json.Unmarshal([]byte(entry.Value), &list) != nil {
			return nil, errors.New("搜索配置格式无效")
		}
		if list == nil {
			list = []Service{}
		}
		changed := false
		for i := range list {
			if IsNative(list[i].Kind) && list[i].LimitsVersion == 0 {
				cfg, e := normalizeConfig(list[i].Config)
				if e != nil {
					return nil, e
				}
				list[i].Config = cfg
				changed = true
			}
		}
		if changed {
			if e := saveServices(ctx, store, list, "limits-update"); e != nil {
				return nil, e
			}
		}
		return list, nil
	}
	list, err := migrateLegacy(ctx, store)
	if err != nil {
		return nil, err
	}
	if err := saveServices(ctx, store, list, "migration"); err != nil {
		return nil, err
	}
	return list, nil
}

func saveServices(ctx context.Context, store *rings.Store, list []Service, by string) error {
	if store == nil {
		return errors.New("搜索配置存储未就绪")
	}
	if list == nil {
		list = []Service{}
	}
	data, err := json.Marshal(list)
	if err != nil {
		return errors.New("搜索配置无法保存")
	}
	if err := store.SetSetting(ctx, SettingKeyServices, string(data), by); err != nil {
		return errors.New("搜索配置无法保存")
	}
	for _, key := range legacyKeys {
		if err := store.DeleteSetting(ctx, key); err != nil {
			return errors.New("旧搜索配置清理失败")
		}
	}
	return nil
}

func SaveService(ctx context.Context, store *rings.Store, change ServiceChange, by string) ([]Service, error) {
	servicesMu.Lock()
	defer servicesMu.Unlock()
	list, err := servicesLocked(ctx, store)
	if err != nil {
		return nil, err
	}
	index := -1
	var previous Service
	for i, item := range list {
		if item.ID == change.ID {
			index, previous = i, item
			break
		}
	}
	if change.ID != "" && index < 0 {
		return nil, ErrServiceMissing
	}
	name := strings.TrimSpace(change.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, errors.New("名称须为 1 至 80 个字符")
	}
	cfg, err := serviceConfig(change, previous)
	if err != nil {
		return nil, err
	}
	for _, item := range list {
		if (cfg.Kind == "github" || IsNative(cfg.Kind)) && item.Kind == cfg.Kind && item.ID != change.ID {
			return nil, errors.New("该站点已添加，可编辑现有卡片")
		}
	}
	service := Service{ID: previous.ID, Name: name, Enabled: change.Enabled, Config: cfg}
	if service.ID == "" {
		service.ID, err = serviceID()
		if err != nil {
			return nil, err
		}
	}
	if index < 0 {
		list = append(list, service)
	} else {
		list[index] = service
	}
	if err := saveServices(ctx, store, list, by); err != nil {
		return nil, err
	}
	return list, nil
}

func serviceConfig(change ServiceChange, previous Service) (Config, error) {
	var err error
	cfg := change.Config
	oldKind, newKind := previous.Kind, cfg.Kind
	if oldKind == "" {
		oldKind = "custom"
	}
	if newKind == "" {
		newKind = "custom"
	}
	if previous.ID != "" && oldKind != newKind {
		return cfg, errors.New("已有卡片不能更换站点类型，请新增卡片")
	}
	cfg.APIKey, cfg.Proxy = previous.APIKey, previous.Proxy
	if change.APIKey != nil {
		cfg.APIKey = strings.TrimSpace(*change.APIKey)
	}
	if change.Proxy != nil {
		cfg.Proxy = strings.TrimSpace(*change.Proxy)
	}
	if change.ProxyConfig != nil {
		if change.Proxy != nil {
			return cfg, errors.New("代理配置重复")
		}
		cfg.Proxy, err = change.ProxyConfig.apply(previous.Proxy)
		if err != nil {
			return cfg, err
		}
	}
	return normalizeConfig(cfg)
}

func DeleteService(ctx context.Context, store *rings.Store, id, by string) ([]Service, error) {
	servicesMu.Lock()
	defer servicesMu.Unlock()
	list, err := servicesLocked(ctx, store)
	if err != nil {
		return nil, err
	}
	for i, item := range list {
		if item.ID == id {
			list = append(list[:i], list[i+1:]...)
			if err := saveServices(ctx, store, list, by); err != nil {
				return nil, err
			}
			return list, nil
		}
	}
	return nil, ErrServiceMissing
}

func ResolveService(ctx context.Context, store *rings.Store, id string) (Service, error) {
	list, err := Services(ctx, store)
	if err != nil {
		return Service{}, err
	}
	available := []Service{}
	for _, item := range list {
		if id != "" && item.ID == id {
			if !item.Enabled {
				return Service{}, errors.New("搜索服务已停用，选择其他服务")
			}
			if !Available(item) {
				return Service{}, errors.New("该站点尚未配置 API Key，配置后可用，也可选择其他服务")
			}
			item.ServiceID = item.ID
			return item, nil
		}
		if Available(item) {
			available = append(available, item)
		}
	}
	if id != "" {
		return Service{}, ErrServiceMissing
	}
	if len(available) == 0 {
		return Service{}, ErrNotConfigured
	}
	if len(available) > 1 {
		return Service{}, ErrServiceRequired
	}
	available[0].ServiceID = available[0].ID
	return available[0], nil
}

func serviceID() (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("搜索服务编号生成失败")
	}
	return hex.EncodeToString(data[:]), nil
}

// Configuration remains editable while keys/proxy credentials stay private.
func PublicConfig(cfg Config) Config {
	key := cfg.APIKey
	cfg.APIKey, cfg.Proxy = "", ""
	redact := func(s string) (string, error) {
		if key != "" {
			s = strings.ReplaceAll(s, key, "{{api_key}}")
		}
		return s, nil
	}
	headers := map[string]string{}
	for name, value := range cfg.Headers {
		headers[name], _ = redact(value)
	}
	cfg.Headers = headers
	if params, err := renderValue(cfg.Params, redact); err == nil {
		cfg.Params, _ = params.(map[string]any)
	}
	return cfg
}

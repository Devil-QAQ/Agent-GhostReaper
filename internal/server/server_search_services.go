package server

import (
	"errors"
	"net/http"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/websearch"
)

// Service views deliberately exclude credentials. Saving an omitted secret
// preserves it; an explicit empty string clears it.
type searchServiceView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	websearch.Config
	HasKey        bool                    `json:"has_key"`
	KeyMask       string                  `json:"key_mask,omitempty"`
	ProxySet      bool                    `json:"proxy_set"`
	ProxyMask     string                  `json:"proxy_mask,omitempty"`
	ProxyEndpoint websearch.ProxyEndpoint `json:"proxy_endpoint"`
	Quota         *websearch.GitHubQuota  `json:"quota,omitempty"`
	QuotaError    string                  `json:"quota_error,omitempty"`
	AssetQuota    *websearch.AssetQuota   `json:"asset_quota,omitempty"`
	Available     bool                    `json:"available"`
}

func (s *Server) writeSearchServices(w http.ResponseWriter, r *http.Request, list []websearch.Service) {
	views := make([]searchServiceView, 0, len(list))
	for _, item := range list {
		views = append(views, searchServiceView{ID: item.ID, Name: item.Name, Enabled: item.Enabled, Config: websearch.PublicConfig(item.Config), HasKey: item.APIKey != "", KeyMask: failover.MaskKey(item.APIKey), ProxySet: item.Proxy != "", ProxyMask: failover.MaskProxy(item.Proxy), ProxyEndpoint: websearch.PublicProxy(item.Proxy)})
		views[len(views)-1].Available = websearch.Available(item)
		if websearch.IsNative(item.Kind) {
			quota, err := websearch.AssetUsage(r.Context(), s.Store, item.Config)
			if err != nil {
				views[len(views)-1].QuotaError = err.Error()
			} else {
				views[len(views)-1].AssetQuota = &quota
			}
		}
		if item.Kind == "github" {
			quota, err := websearch.GitHubUsage(r.Context(), s.Store, item.Config)
			if err != nil {
				views[len(views)-1].QuotaError = err.Error()
			} else {
				views[len(views)-1].Quota = &quota
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"services": views, "providers": websearch.SiteProviders(), "tests": websearch.TestPresets(), "limit_profiles": websearch.LimitProfiles()})
}

func (s *Server) handleSearchServiceTest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var change websearch.ServiceChange
	if !readJSON(w, r, &change) {
		return
	}
	change.ID = r.PathValue("service")
	result, err := websearch.TestService(r.Context(), s.Store, change)
	if err != nil {
		searchServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSearchServices(w http.ResponseWriter, r *http.Request) {
	list, err := websearch.Services(r.Context(), s.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writeSearchServices(w, r, list)
}

func (s *Server) handleSearchServiceSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var change websearch.ServiceChange
	if !readJSON(w, r, &change) {
		return
	}
	change.ID = r.PathValue("service")
	list, err := websearch.SaveService(r.Context(), s.Store, change, "creator")
	if err != nil {
		searchServiceError(w, err)
		return
	}
	s.writeSearchServices(w, r, list)
}

func (s *Server) handleSearchServiceDelete(w http.ResponseWriter, r *http.Request) {
	list, err := websearch.DeleteService(r.Context(), s.Store, r.PathValue("service"), "creator")
	if err != nil {
		searchServiceError(w, err)
		return
	}
	s.writeSearchServices(w, r, list)
}

func (s *Server) handleGitHubQuery(w http.ResponseWriter, r *http.Request) {
	var q websearch.GitHubQuery
	if !readJSON(w, r, &q) {
		return
	}
	result, err := websearch.GitHub(r.Context(), s.Store, r.PathValue("service"), q)
	if err != nil {
		searchServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAssetQuery(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var q websearch.AssetQuery
	if !readJSON(w, r, &q) {
		return
	}
	result, err := websearch.AssetSearch(r.Context(), s.Store, r.PathValue("service"), q)
	if err != nil {
		searchServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func searchServiceError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, websearch.ErrServiceMissing) {
		code = http.StatusNotFound
	}
	writeErr(w, code, err)
}

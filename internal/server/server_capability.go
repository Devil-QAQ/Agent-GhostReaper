package server

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"

	"ghostreaper/internal/capability"
)

func (s *Server) capabilityReady(w http.ResponseWriter) bool {
	if s.Capabilities == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("能力库尚未接入"))
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (s *Server) handleCapabilitySources(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": s.Capabilities.List()})
}

func (s *Server) handleCapabilitySave(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	var source capability.Source
	if !readJSON(w, r, &source) {
		return
	}
	source.ID = r.PathValue("source")
	var err error
	if source.Kind == "folder" {
		err = s.Capabilities.RenameFolder(source.ID, source.Name)
	} else {
		err = s.Capabilities.Save(source)
	}
	if err != nil {
		capabilityConfigError(w, err)
		return
	}
	s.handleCapabilitySources(w, r)
}

func (s *Server) handleCapabilityDelete(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	if err := s.Capabilities.Delete(r.PathValue("source")); err != nil {
		capabilityConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"repositories": s.Capabilities.List()})
}

func (s *Server) handleCapabilityCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	if err := s.Capabilities.RetryCleanup(r.PathValue("source")); err != nil {
		capabilityConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"repositories": s.Capabilities.List()})
}

func (s *Server) handleCapabilityImport(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (2<<30)+(32<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("请选择要导入的文件夹"))
		return
	}
	if err := s.Capabilities.ImportFolderAsync(r.Context(), r.URL.Query().Get("id"), r.URL.Query().Get("name"), reader); err != nil {
		capabilityConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"repositories": s.Capabilities.List()})
}

func (s *Server) handleCapabilityStatus(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	status, err := s.Capabilities.Status(r.PathValue("source"))
	if err != nil {
		capabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCapabilityUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	id := r.PathValue("source")
	started, err := s.Capabilities.StartUpdate(id)
	if err != nil {
		capabilityConfigError(w, err)
		return
	}
	status, err := s.Capabilities.Status(id)
	if err != nil {
		capabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "status": status})
}

func (s *Server) handleCapabilityFiles(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
	library, err := s.Capabilities.Get(r.PathValue("source"))
	if err != nil {
		capabilityError(w, err)
		return
	}
	listing, err := library.List(r.URL.Query().Get("dir"), r.URL.Query().Get("q"), pageNumber)
	if err != nil {
		capabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleCapabilityFile(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	name, commit := r.URL.Query().Get("path"), r.URL.Query().Get("version")
	library, err := s.Capabilities.Get(r.PathValue("source"))
	if err != nil {
		capabilityError(w, err)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		f, _, err := library.Open(name, commit)
		if err != nil {
			capabilityError(w, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			capabilityError(w, err)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	preview, err := library.Preview(name, commit)
	if r.URL.Query().Get("upstream") == "1" {
		preview, err = library.PreviewUpstream(name, commit)
	}
	if err != nil {
		capabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func capabilityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, capability.ErrNotReady):
		writeErr(w, http.StatusConflict, err)
	case errors.Is(err, capability.ErrSourceBusy):
		writeErr(w, http.StatusConflict, err)
	case errors.Is(err, capability.ErrPath):
		writeErr(w, http.StatusBadRequest, err)
	case errors.Is(err, capability.ErrSourceMissing):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, errors.New("文件或目录不存在"))
	default:
		writeErr(w, http.StatusInternalServerError, errors.New("能力库文件读取失败，请重新下载后重试"))
	}
}

func capabilityConfigError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, capability.ErrSourceMissing) {
		status = http.StatusNotFound
	}
	if errors.Is(err, capability.ErrSourceBusy) || errors.Is(err, capability.ErrStale) {
		status = http.StatusConflict
	}
	writeErr(w, status, err)
}

func (s *Server) handleCapabilityMutation(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var m capability.Mutation
	if !readJSON(w, r, &m) {
		return
	}
	if err := s.Capabilities.Mutate(r.PathValue("source"), m); err != nil {
		capabilityConfigError(w, err)
		return
	}
	if m.Action == "delete" {
		status, err := s.Capabilities.Status(r.PathValue("source"))
		if err != nil {
			capabilityError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, status)
		return
	}
	s.handleCapabilityStatus(w, r)
}
func (s *Server) handleCapabilityChanges(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	l, err := s.Capabilities.Get(r.PathValue("source"))
	if err != nil {
		capabilityError(w, err)
		return
	}
	changes, err := l.Changes()
	if err != nil {
		capabilityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, changes)
}

func (s *Server) handleCapabilitySearch(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	result, err := s.Capabilities.Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("source"), page)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("资料查询失败"))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) handleCapabilityContent(w http.ResponseWriter, r *http.Request) {
	if !s.capabilityReady(w) {
		return
	}
	l, err := s.Capabilities.Get(r.PathValue("source"))
	if err != nil {
		capabilityError(w, err)
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	limit := 256 << 10
	if q.Has("limit") {
		limit, _ = strconv.Atoi(q.Get("limit"))
	}
	result, err := l.ReadEffective(q.Get("path"), q.Get("content_version"), offset, limit)
	if err != nil {
		if errors.Is(err, capability.ErrStale) {
			capabilityConfigError(w, err)
		} else {
			capabilityError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

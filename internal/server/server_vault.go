// Package server 的目标凭据 API(P3-09):凭据库的打码清单、保存与
// 删除(系统设置「目标凭据」卡)。秘密只在服务端与执行侧环境变量出现,
// 一切响应只回打码视图(P3-09 完成标准:界面打码);存储层密文(敏感
// 键 cred.*,见 internal/secrets)。执行侧调用(bash 环境注入与任务包
// 清单)在 internal/swarm 装配,本文件只管面板配置面。
package server

import (
	"errors"
	"net/http"
	"strings"

	"ghostreaper/internal/vault"
)

// handleCredentialsList 凭据清单(打码)。
func (s *Server) handleCredentialsList(w http.ResponseWriter, r *http.Request) {
	views, err := vault.Views(r.Context(), s.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": views})
}

// credentialBody 凭据写体:id 空 = 新增;secret 空串 = 编辑保留旧值。
type credentialBody struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	Note     string `json:"note"`
}

// handleCredentialSave 保存(新增/编辑)一条凭据:重名拒、纯符号名拒、
// 新建缺秘密拒(校验归 vault.Save);响应回打码清单。
func (s *Server) handleCredentialSave(w http.ResponseWriter, r *http.Request) {
	var body credentialBody
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := vault.Save(r.Context(), s.Store, vault.Credential{
		ID: strings.TrimSpace(body.ID), Name: body.Name, Username: body.Username,
		Secret: body.Secret, Note: body.Note,
	}, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	views, err := vault.Views(r.Context(), s.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": views})
}

// handleCredentialDelete 删除一条凭据(幂等)。
func (s *Server) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("凭据 ID 不能为空"))
		return
	}
	if err := vault.Delete(r.Context(), s.Store, id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

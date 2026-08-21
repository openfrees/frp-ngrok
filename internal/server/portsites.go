package server

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/store"
)

func portSitesEnabled() bool {
	cfg, err := store.LoadPortSites()
	return err == nil && cfg.Enabled
}

func (s *Server) reservedConsolePorts() []int {
	if s.port > 0 {
		return []int{s.port}
	}
	return nil
}

func (s *Server) portSiteState() (apitypes.PortSitesState, error) {
	cfg, err := store.LoadPortSites()
	if err != nil {
		return apitypes.PortSitesState{}, err
	}
	resp := apitypes.PortSitesState{Enabled: cfg.Enabled, Sites: []apitypes.PortSiteView{}}
	for _, site := range cfg.Sites {
		running := cfg.Enabled && s.sites != nil && s.sites.Running(site.Port)
		resp.Sites = append(resp.Sites, apitypes.PortSiteView{
			Port:               site.Port,
			Root:               site.Root,
			CustomRoot:         site.CustomRoot,
			Running:            running,
			URL:                fmt.Sprintf("http://127.0.0.1:%d", site.Port),
			Managed:            store.IsManagedPortSiteRoot(site.Root, site.Port),
			DeleteFilesDefault: store.DefaultDeleteFiles(site),
		})
	}
	return resp, nil
}

func (s *Server) writePortSites(w http.ResponseWriter) {
	resp, err := s.portSiteState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetPortSites(w http.ResponseWriter, _ *http.Request) {
	s.writePortSites(w)
}

type putPortSitesReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handlePutPortSites(w http.ResponseWriter, r *http.Request) {
	var req putPortSitesReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := store.LoadPortSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cfg.Enabled = req.Enabled
	if err := store.SavePortSites(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !req.Enabled {
		if s.sites != nil {
			_ = s.sites.StopAll()
		}
	} else {
		s.restorePortSites()
	}
	s.writePortSites(w)
}

type createPortSiteReq struct {
	Port  int    `json:"port"`
	Root  string `json:"root"`
	Start bool   `json:"start"`
}

func (s *Server) handleCreatePortSite(w http.ResponseWriter, r *http.Request) {
	if !portSitesEnabled() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请先开启本地端口管理插件"))
		return
	}
	var req createPortSiteReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.CheckPortConflict(req.Port, s.reservedConsolePorts()); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	site, err := store.CreatePortSite(req.Port, req.Root)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if req.Start {
		if err := s.startPortSite(site); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
	}
	s.writePortSites(w)
}

func (s *Server) handleStartPortSite(w http.ResponseWriter, r *http.Request) {
	if !portSitesEnabled() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请先开启本地端口管理插件"))
		return
	}
	port, err := parsePortPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	site, ok, err := store.FindPortSite(port)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("没有端口 %d 的站点", port))
		return
	}
	if err := s.startPortSite(site); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.writePortSites(w)
}

func (s *Server) startPortSite(site store.PortSite) error {
	if s.sites != nil && s.sites.Running(site.Port) {
		return store.SetPortSiteRunning(site.Port, true)
	}
	if err := store.CheckPortConflictExcept(site.Port, s.reservedConsolePorts(), site.Port); err != nil {
		return err
	}
	if err := s.sites.Start(site.Port, site.Root); err != nil {
		return err
	}
	return store.SetPortSiteRunning(site.Port, true)
}

func (s *Server) handleStopPortSite(w http.ResponseWriter, r *http.Request) {
	port, err := parsePortPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, ok, err := store.FindPortSite(port); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("没有端口 %d 的站点", port))
		return
	}
	if s.sites != nil {
		_ = s.sites.Stop(port)
	}
	if err := store.SetPortSiteRunning(port, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writePortSites(w)
}

func (s *Server) handleDeletePortSite(w http.ResponseWriter, r *http.Request) {
	port, err := parsePortPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	deleteFiles := false
	if q := strings.TrimSpace(r.URL.Query().Get("deleteFiles")); q != "" {
		deleteFiles = q == "1" || strings.EqualFold(q, "true")
	}
	if s.sites != nil {
		_ = s.sites.Stop(port)
	}
	if err := store.DeletePortSite(port, deleteFiles); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.writePortSites(w)
}

func (s *Server) handleListPortSiteFiles(w http.ResponseWriter, r *http.Request) {
	site, err := s.lookupPortSite(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	files, dir, err := store.ListPortSiteFiles(site.Root, dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	offset, limit := parseFilePage(r)
	page, total, offset, limit := store.PaginatePortSiteFiles(files, offset, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"files":  page,
		"root":   site.Root,
		"dir":    dir,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

func parseFilePage(r *http.Request) (offset, limit int) {
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			offset = n
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	return offset, limit
}

func (s *Server) handleUploadPortSiteFile(w http.ResponseWriter, r *http.Request) {
	site, err := s.lookupPortSite(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("上传格式有误: %w", err))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请选择要上传的文件"))
		return
	}
	defer file.Close()
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if err := store.WritePortSiteFile(site.Root, dir, hdr.Filename, file); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.handleListPortSiteFiles(w, r)
}

func (s *Server) handleDeletePortSiteFile(w http.ResponseWriter, r *http.Request) {
	site, err := s.lookupPortSite(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	rel := name
	if clean, err := store.NormalizeSiteDir(dir); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	} else if clean != "" {
		rel = path.Join(clean, name)
	}
	if err := store.DeletePortSiteFile(site.Root, rel); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.handleListPortSiteFiles(w, r)
}

func (s *Server) handlePickPortSiteDir(w http.ResponseWriter, _ *http.Request) {
	// 原生对话框可能比全局 WriteTimeout 更久，用户慢慢选目录不该把请求掐死。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	path, canceled, err := store.PickDirectory()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if canceled {
		writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

func (s *Server) handleOpenPortSiteFolder(w http.ResponseWriter, r *http.Request) {
	site, err := s.lookupPortSite(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.OpenPortSiteFolder(site.Root, strings.TrimSpace(r.URL.Query().Get("dir"))); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) lookupPortSite(r *http.Request) (store.PortSite, error) {
	port, err := parsePortPath(r)
	if err != nil {
		return store.PortSite{}, err
	}
	site, ok, err := store.FindPortSite(port)
	if err != nil {
		return store.PortSite{}, err
	}
	if !ok {
		return store.PortSite{}, fmt.Errorf("没有端口 %d 的站点", port)
	}
	return site, nil
}

func parsePortPath(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.PathValue("port"))
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("端口必须是 1-65535 的数字")
	}
	return port, nil
}

// restorePortSites 按配置把上次运行中的站点再拉起来。失败只记日志，不挡控制台。
func (s *Server) restorePortSites() {
	cfg, err := store.LoadPortSites()
	if err != nil {
		log.Printf("[端口管理] 读取配置失败: %v", err)
		return
	}
	if !cfg.Enabled {
		if s.sites != nil {
			_ = s.sites.StopAll()
		}
		return
	}
	for _, site := range cfg.Sites {
		if !site.Running {
			continue
		}
		if err := s.sites.Start(site.Port, site.Root); err != nil {
			log.Printf("[端口管理] 恢复端口 %d 失败: %v", site.Port, err)
		}
	}
}

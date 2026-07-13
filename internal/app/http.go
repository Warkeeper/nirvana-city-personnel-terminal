package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Server struct {
	store    *Store
	frontend fs.FS
	csrf     string
	shutdown func()
	stopping atomic.Bool
}

func NewServer(store *Store, frontend fs.FS) *Server {
	return &Server{
		store:    store,
		frontend: frontend,
		csrf:     randomToken(),
	}
}

func (s *Server) SetShutdown(fn func()) {
	s.shutdown = fn
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/bootstrap", s.handleBootstrap)
	mux.HandleFunc("/api/v1/city/open", s.writeHandler(s.handleOpenCity))
	mux.HandleFunc("/api/v1/city/close", s.writeHandler(s.handleCloseCity))
	mux.HandleFunc("/api/v1/residents/search", s.handleSearchResidents)
	mux.HandleFunc("/api/v1/residents/player/enter", s.writeHandler(s.handleEnterPlayer))
	mux.HandleFunc("/api/v1/residents/npc", s.writeHandler(s.handleUpsertNPC))
	mux.HandleFunc("/api/v1/residents/", s.writeHandler(s.handleResidentPath))
	mux.HandleFunc("/api/v1/identity", s.writeHandler(s.handleSetIdentity))
	mux.HandleFunc("/api/v1/identity/history/", s.writeHandler(s.handleIdentityHistoryPath))
	mux.HandleFunc("/api/v1/gold/records/query", s.handleQueryGoldRecords)
	mux.HandleFunc("/api/v1/gold/records", s.writeHandler(s.handleCreateGoldRecord))
	mux.HandleFunc("/api/v1/gold/records/", s.writeHandler(s.handleGoldRecordPath))
	mux.HandleFunc("/api/v1/travel/extensions", s.writeHandler(s.handleExtendTravel))
	mux.HandleFunc("/api/v1/travel/hide", s.writeHandler(s.handleHideTravel))
	mux.HandleFunc("/api/v1/npc/panel", s.writeHandler(s.handleNPCPanel))
	mux.HandleFunc("/api/v1/stats/today", s.handleTodayStats)
	mux.HandleFunc("/api/v1/summary", s.handleSummary)
	mux.HandleFunc("/api/v1/export/full-data", s.handleExportData)
	mux.HandleFunc("/api/v1/export/full.xlsx", s.handleExportXLSX)
	mux.HandleFunc("/api/v1/cloud/ncpt/sync", s.writeHandler(s.handleNCPTCloudSync))
	mux.HandleFunc("/api/v1/shutdown", s.writeHandler(s.handleShutdown))
	mux.HandleFunc("/", s.handleFrontend)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": s.store.nowString()})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	state, err := s.store.State(r.Context(), s.csrf)
	respond(w, state, err)
}

func (s *Server) handleOpenCity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		Operator string `json:"operator"`
		OpenedAt string `json:"openedAt"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.OpenCity(r.Context(), in.Operator, in.OpenedAt)
	respond(w, state, err)
}

func (s *Server) handleCloseCity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, err := s.store.CloseCity(r.Context())
	respond(w, state, err)
}

func (s *Server) handleSearchResidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	roles, err := s.store.SearchResidents(r.Context(), ResidentSearchInput{
		Query: r.URL.Query().Get("q"),
		Code:  r.URL.Query().Get("code"),
		Name:  r.URL.Query().Get("name"),
		Kind:  r.URL.Query().Get("kind"),
		Limit: limit,
	})
	respond(w, map[string]any{"residents": roles}, err)
}

func (s *Server) handleEnterPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in EnterResidentInput
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.EnterPlayer(r.Context(), in)
	respond(w, state, err)
}

func (s *Server) handleUpsertNPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in NPCInput
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.UpsertNPC(r.Context(), in)
	respond(w, state, err)
}

func (s *Server) handleResidentPath(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/v1/residents/")
	if strings.HasSuffix(rel, "/profile") {
		code := pathCode(strings.TrimSuffix(rel, "/profile"))
		if r.Method != http.MethodPatch && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var in struct {
			Code   string `json:"code"`
			Name   string `json:"name"`
			Remark string `json:"remark"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		state, err := s.store.UpdateResident(r.Context(), code, in.Code, in.Name, in.Remark)
		respond(w, state, err)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleSetIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		Code     string `json:"code"`
		Identity string `json:"identity"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.SetIdentity(r.Context(), in.Code, in.Identity)
	respond(w, state, err)
}

func (s *Server) handleIdentityHistoryPath(w http.ResponseWriter, r *http.Request) {
	idText := strings.TrimPrefix(r.URL.Path, "/api/v1/identity/history/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		respond(w, nil, fmt.Errorf("%w: invalid identity history id", ErrBadRequest))
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, err := s.store.DeleteIdentityHistory(r.Context(), id)
	respond(w, state, err)
}

func (s *Server) handleCreateGoldRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in GoldInput
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.CreateGoldRecord(r.Context(), in)
	respond(w, state, err)
}

func (s *Server) handleQueryGoldRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	page, err := queryInt(r, "page", 1)
	if err != nil {
		respond(w, nil, err)
		return
	}
	pageSize, err := queryInt(r, "pageSize", 20)
	if err != nil {
		respond(w, nil, err)
		return
	}
	result, err := s.store.QueryGoldRecords(r.Context(), GoldRecordQueryInput{
		Date:         r.URL.Query().Get("date"),
		ResidentCode: r.URL.Query().Get("residentCode"),
		Page:         page,
		PageSize:     pageSize,
	})
	respond(w, result, err)
}

func (s *Server) handleGoldRecordPath(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/v1/gold/records/")
	if !strings.HasSuffix(rel, "/void") {
		http.NotFound(w, r)
		return
	}
	idText := strings.TrimSuffix(rel, "/void")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		respond(w, nil, fmt.Errorf("%w: invalid gold record id", ErrBadRequest))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	state, err := s.store.VoidGoldRecord(r.Context(), id)
	respond(w, state, err)
}

func queryInt(r *http.Request, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrBadRequest, name)
	}
	return value, nil
}

func (s *Server) handleExtendTravel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		TravelID int64   `json:"travelId"`
		Hours    float64 `json:"hours"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.ExtendTravel(r.Context(), in.TravelID, in.Hours)
	respond(w, state, err)
}

func (s *Server) handleHideTravel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		TravelID int64 `json:"travelId"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.HideTravel(r.Context(), in.TravelID)
	respond(w, state, err)
}

func (s *Server) handleNPCPanel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in struct {
		Code    string `json:"code"`
		Visible bool   `json:"visible"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	state, err := s.store.SetNPCVisible(r.Context(), in.Code, in.Visible)
	respond(w, state, err)
}

func (s *Server) handleTodayStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	stats, err := s.store.todayStats(r.Context())
	respond(w, stats, err)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	summary, err := s.store.Summary(r.Context(), r.URL.Query().Get("code"))
	respond(w, summary, err)
}

func (s *Server) handleExportData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	data, err := s.store.ExportData(r.Context())
	respond(w, data, err)
}

func (s *Server) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	data, err := s.store.ExportData(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	bytes, err := XLSXBytes(data)
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="ncfms-export.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)
}

func (s *Server) handleNCPTCloudSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var in NCPTCloudSyncInput
	if !decodeJSON(w, r, &in) {
		return
	}
	stats, err := s.store.UploadNCPTSnapshot(r.Context(), in, &http.Client{Timeout: 60 * time.Second})
	respond(w, stats, err)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.stopping.CompareAndSwap(false, true) && s.shutdown != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.shutdown()
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	fullPath := path.Join("frontend", name)
	file, err := s.frontend.Open(fullPath)
	if err != nil {
		file, err = s.frontend.Open("frontend/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		fullPath = "frontend/index.html"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		respond(w, nil, err)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(fullPath))
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
}

func (s *Server) writeHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		if err := s.checkSameOrigin(r); err != nil {
			respond(w, nil, err)
			return
		}
		if r.Header.Get("X-NCFMS-CSRF") != s.csrf {
			respondStatus(w, http.StatusForbidden, "CSRF token is missing or invalid")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			respondStatus(w, http.StatusBadRequest, "Idempotency-Key is required")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			respond(w, nil, err)
			return
		}
		_ = r.Body.Close()
		hash := requestHash(r.Method, r.URL.Path, body)
		status, stored, found, err := s.lookupIdempotency(r.Context(), key, hash)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if found {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(stored))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := &responseCapture{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		if rec.status >= 200 && rec.status < 300 {
			_ = s.storeIdempotency(r.Context(), key, r.Method, r.URL.Path, hash, rec.status, rec.buf.String())
		}
	}
}

func (s *Server) checkSameOrigin(r *http.Request) error {
	for _, header := range []string{"Origin", "Referer"} {
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		u, err := http.NewRequest(http.MethodGet, value, nil)
		if err != nil || u.URL == nil || u.URL.Host == "" {
			continue
		}
		if !strings.EqualFold(u.URL.Host, r.Host) {
			return fmt.Errorf("%w: cross-origin write rejected", ErrBadRequest)
		}
	}
	return nil
}

func (s *Server) lookupIdempotency(ctx context.Context, key, hash string) (int, string, bool, error) {
	var storedHash, body string
	var status int
	err := s.store.db.QueryRowContext(ctx, "SELECT payload_hash, response_status, response_body FROM idempotency_requests WHERE key = ?", key).
		Scan(&storedHash, &status, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if storedHash != hash {
		return http.StatusConflict, `{"error":"Idempotency-Key was already used with a different request"}`, true, nil
	}
	return status, body, true, nil
}

func (s *Server) storeIdempotency(ctx context.Context, key, method, path, hash string, status int, body string) error {
	_, err := s.store.db.ExecContext(ctx, `INSERT INTO idempotency_requests(key, method, path, payload_hash, response_status, response_body, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, key, method, path, hash, status, body, s.store.nowString())
	return err
}

type responseCapture struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *responseCapture) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseCapture) Write(body []byte) (int, error) {
	r.buf.Write(body)
	return r.ResponseWriter.Write(body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		respond(w, nil, fmt.Errorf("%w: %v", ErrBadRequest, err))
		return false
	}
	return true
}

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		switch {
		case errors.As(err, &statusErr):
			status = statusErr.StatusCode()
		case errors.Is(err, ErrBadRequest):
			status = http.StatusBadRequest
		case errors.Is(err, ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, ErrNotFound):
			status = http.StatusNotFound
		}
		respondStatus(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func respondStatus(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if value == nil {
		value = map[string]any{"ok": true}
	}
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter) {
	respondStatus(w, http.StatusMethodNotAllowed, "method not allowed")
}

func requestHash(method, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+"\n"+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

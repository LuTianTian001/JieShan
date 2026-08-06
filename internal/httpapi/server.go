package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/accountsync"
	"github.com/LuTianTian001/JieShan/internal/auth"
	"github.com/LuTianTian001/JieShan/internal/config"
	"github.com/LuTianTian001/JieShan/internal/gateway"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type Server struct {
	cfg             config.Config
	store           *store.Store
	auth            *auth.Service
	cipher          *secrets.Cipher
	upstream        *upstream.Client
	gateway         *gateway.Gateway
	accounts        *accountsync.Service
	mux             *http.ServeMux
	inferenceSlots  chan struct{}
	managementSlots chan struct{}
	loginSlots      chan struct{}
	loginLimiter    *loginFailureLimiter
}

type adminContextKey struct{}

const (
	inferenceRequestLimit  = 8
	managementRequestLimit = 4
)

func New(cfg config.Config, s *store.Store, authService *auth.Service, cipher *secrets.Cipher, upstreamClient *upstream.Client, gatewayService *gateway.Gateway, accountServices ...*accountsync.Service) *Server {
	server := &Server{
		cfg: cfg, store: s, auth: authService, cipher: cipher, upstream: upstreamClient,
		gateway: gatewayService, mux: http.NewServeMux(),
		inferenceSlots: make(chan struct{}, inferenceRequestLimit), managementSlots: make(chan struct{}, managementRequestLimit),
		loginSlots: make(chan struct{}, 1), loginLimiter: newLoginFailureLimiter(loginLimiterMaxEntries),
	}
	if len(accountServices) > 0 {
		server.accounts = accountServices[0]
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			s.mux.ServeHTTP(w, r)
			return
		}
		slots := s.managementSlots
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/chat/completions" {
			slots = s.inferenceSlots
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			s.mux.ServeHTTP(w, r)
		default:
			writeError(w, http.StatusServiceUnavailable, "server is busy", "server_busy")
		}
	}))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.login)
	s.mux.Handle("POST /api/v1/auth/logout", s.admin(http.HandlerFunc(s.logout)))
	s.mux.Handle("GET /api/v1/me", s.admin(http.HandlerFunc(s.me)))
	s.mux.Handle("GET /api/v1/dashboard", s.admin(http.HandlerFunc(s.dashboard)))
	s.mux.Handle("GET /api/v1/monitor/matrix", s.admin(http.HandlerFunc(s.monitorMatrix)))
	s.mux.Handle("GET /api/v1/account-adapters", s.admin(http.HandlerFunc(s.listAccountAdapters)))

	s.mux.Handle("GET /api/v1/upstreams", s.admin(http.HandlerFunc(s.listUpstreams)))
	s.mux.Handle("POST /api/v1/upstreams", s.admin(http.HandlerFunc(s.createUpstream)))
	s.mux.Handle("GET /api/v1/upstreams/{id}", s.admin(http.HandlerFunc(s.getUpstream)))
	s.mux.Handle("PATCH /api/v1/upstreams/{id}", s.admin(http.HandlerFunc(s.updateUpstream)))
	s.mux.Handle("DELETE /api/v1/upstreams/{id}", s.admin(http.HandlerFunc(s.deleteUpstream)))
	s.mux.Handle("GET /api/v1/upstreams/{id}/models", s.admin(http.HandlerFunc(s.listUpstreamModels)))
	s.mux.Handle("POST /api/v1/upstreams/{id}/test", s.admin(http.HandlerFunc(s.testUpstream)))
	s.mux.Handle("POST /api/v1/upstreams/{id}/models/discover", s.admin(http.HandlerFunc(s.discoverModels)))
	s.mux.Handle("POST /api/v1/upstreams/{id}/models/apply", s.admin(http.HandlerFunc(s.applyModels)))
	s.mux.Handle("GET /api/v1/upstreams/{id}/account", s.admin(http.HandlerFunc(s.getUpstreamAccount)))
	s.mux.Handle("PUT /api/v1/upstreams/{id}/account", s.admin(http.HandlerFunc(s.configureUpstreamAccount)))
	s.mux.Handle("DELETE /api/v1/upstreams/{id}/account", s.admin(http.HandlerFunc(s.deleteUpstreamAccount)))
	s.mux.Handle("POST /api/v1/upstreams/{id}/account/refresh", s.admin(http.HandlerFunc(s.refreshUpstreamAccount)))
	s.mux.Handle("GET /api/v1/upstreams/{id}/account/usage", s.admin(http.HandlerFunc(s.listUpstreamAccountUsage)))
	s.mux.Handle("POST /api/v1/upstreams/{id}/balance/refresh", s.admin(http.HandlerFunc(s.refreshUpstreamAccount)))
	s.mux.Handle("GET /api/v1/upstreams/{id}/usage", s.admin(http.HandlerFunc(s.listUpstreamAccountUsage)))

	s.mux.Handle("GET /api/v1/routes", s.admin(http.HandlerFunc(s.listRoutes)))
	s.mux.Handle("POST /api/v1/routes", s.admin(http.HandlerFunc(s.createRoute)))
	s.mux.Handle("GET /api/v1/routes/{id}", s.admin(http.HandlerFunc(s.getRoute)))
	s.mux.Handle("PATCH /api/v1/routes/{id}", s.admin(http.HandlerFunc(s.updateRoute)))
	s.mux.Handle("DELETE /api/v1/routes/{id}", s.admin(http.HandlerFunc(s.deleteRoute)))
	s.mux.Handle("PUT /api/v1/routes/{id}/targets/order", s.admin(http.HandlerFunc(s.reorderTargets)))
	s.mux.Handle("POST /api/v1/routes/{id}/probe", s.admin(http.HandlerFunc(s.probeRoute)))

	s.mux.Handle("GET /api/v1/keys", s.admin(http.HandlerFunc(s.listKeys)))
	s.mux.Handle("POST /api/v1/keys", s.admin(http.HandlerFunc(s.createKey)))
	s.mux.Handle("PATCH /api/v1/keys/{id}", s.admin(http.HandlerFunc(s.updateKey)))
	s.mux.Handle("DELETE /api/v1/keys/{id}", s.admin(http.HandlerFunc(s.deleteKey)))
	s.mux.Handle("POST /api/v1/keys/{id}/reset-usage", s.admin(http.HandlerFunc(s.resetKeyUsage)))

	s.mux.Handle("GET /api/v1/logs/requests", s.admin(http.HandlerFunc(s.listLogs)))
	s.mux.Handle("GET /api/v1/logs/requests/{id}", s.admin(http.HandlerFunc(s.getLog)))
	s.mux.Handle("GET /api/v1/settings", s.admin(http.HandlerFunc(s.getSettings)))
	s.mux.Handle("PATCH /api/v1/settings", s.admin(http.HandlerFunc(s.updateSettings)))

	s.mux.HandleFunc("GET /v1/models", s.gateway.Models)
	s.mux.HandleFunc("POST /v1/chat/completions", s.gateway.ChatCompletions)
	s.mux.HandleFunc("POST /chat/completions", s.gateway.ChatCompletions)
	s.mux.Handle("/", spaHandler(s.cfg.WebDir))
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required", "unauthorized")
			return
		}
		admin, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session expired", "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminContextKey{}, admin)))
	})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.store.DB.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable", "unhealthy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	now := time.Now()
	limitKey := loginClientIP(r, s.cfg.TrustProxy) + "|admin"
	if allowed, retry := s.loginLimiter.allow(limitKey, now); !allowed {
		seconds := int((retry + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts", "rate_limited")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "login is busy, retry shortly", "rate_limited")
		return
	}
	token, admin, expiresAt, err := s.auth.Login(r.Context(), body.Password)
	if err != nil {
		s.loginLimiter.failure(limitKey, now)
		writeError(w, http.StatusUnauthorized, "invalid credentials", "invalid_credentials")
		return
	}
	s.loginLimiter.success(limitKey)
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode, Expires: time.UnixMilli(expiresAt), MaxAge: int(time.Until(time.UnixMilli(expiresAt)).Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": admin.ID, "username": admin.Username}})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		_ = s.auth.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	admin := r.Context().Value(adminContextKey{}).(store.Admin)
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": admin.ID, "username": admin.Username}})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetDashboard(r.Context(), time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	routes, err := s.store.ListRoutes(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	monitored, healthyModels, attention, cooling := 0, 0, 0, 0
	for _, route := range routes {
		if !route.MonitorEnabled {
			continue
		}
		monitored++
		healthy := false
		for _, target := range route.Targets {
			switch targetState(target) {
			case "healthy":
				healthy = true
			case "cooldown":
				cooling++
				attention++
			case "suspect", "credential_error":
				attention++
			}
		}
		if healthy {
			healthyModels++
		}
	}
	successRate := float64(0)
	if stats.RequestsToday > 0 {
		successRate = float64(stats.RequestsToday-stats.FailuresToday) / float64(stats.RequestsToday) * 100
	}
	writeJSON(w, http.StatusOK, map[string]any{"monitoredModels": monitored, "healthyModels": healthyModels, "attentionTargets": attention, "coolingTargets": cooling, "successRate24h": successRate, "requests24h": stats.RequestsToday})
}

func (s *Server) monitorMatrix(w http.ResponseWriter, r *http.Request) {
	routes, err := s.store.ListRoutes(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		items = append(items, routeDTO(route))
	}
	writeJSON(w, http.StatusOK, map[string]any{"generatedAt": time.Now().UTC().Format(time.RFC3339), "probeIntervalSeconds": settings.ProbeIntervalSeconds, "routes": items})
}

type upstreamPayload struct {
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	Protocol      string            `json:"protocol"`
	DashboardURL  string            `json:"dashboardUrl"`
	BaseURL       string            `json:"baseUrl"`
	APIKey        *string           `json:"apiKey"`
	Enabled       *bool             `json:"enabled"`
	CustomHeaders map[string]string `json:"customHeaders"`
}

func (s *Server) listUpstreams(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListUpstreams(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dtos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		models, err := s.store.ListUpstreamModels(r.Context(), item.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		dtos = append(dtos, upstreamDTO(item, models))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}

func (s *Server) getUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetUpstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	models, _ := s.store.ListUpstreamModels(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": upstreamDTO(item, models)})
}

func (s *Server) createUpstream(w http.ResponseWriter, r *http.Request) {
	var body upstreamPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	kind, err := store.NormalizeKind(firstNonEmpty(body.Protocol, body.Kind, "compatible"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_protocol")
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.BaseURL) == "" || body.APIKey == nil || strings.TrimSpace(*body.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "name, baseUrl and apiKey are required", "invalid_request")
		return
	}
	secret, err := s.cipher.Encrypt(strings.TrimSpace(*body.APIKey))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	headers, _ := json.Marshal(body.CustomHeaders)
	id, err := s.store.CreateUpstream(r.Context(), store.UpstreamWrite{Name: strings.TrimSpace(body.Name), Kind: kind, DashboardURL: strings.TrimSpace(body.DashboardURL), BaseURL: strings.TrimSpace(body.BaseURL), Enabled: enabled, CustomHeaders: headers, SecretCipher: secret})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetUpstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": upstreamDTO(item, []store.UpstreamModel{})})
}

func (s *Server) updateUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetUpstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body upstreamPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	name := firstNonEmpty(body.Name, current.Name)
	baseURL := firstNonEmpty(body.BaseURL, current.BaseURL)
	kindInput := firstNonEmpty(body.Protocol, body.Kind, current.Kind)
	kind, err := store.NormalizeKind(kindInput)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_protocol")
		return
	}
	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	dashboardURL := current.DashboardURL
	if body.DashboardURL != "" {
		dashboardURL = body.DashboardURL
	}
	headers := current.CustomHeaders
	if body.CustomHeaders != nil {
		headers, _ = json.Marshal(body.CustomHeaders)
	}
	write := store.UpstreamWrite{Name: name, Kind: kind, DashboardURL: dashboardURL, BaseURL: baseURL, Enabled: enabled, CustomHeaders: headers}
	if body.APIKey != nil {
		write.SecretCipher, err = s.cipher.Encrypt(strings.TrimSpace(*body.APIKey))
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if err := s.store.UpdateUpstream(r.Context(), id, write, body.APIKey != nil, false); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetUpstream(r.Context(), id)
	models, _ := s.store.ListUpstreamModels(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": upstreamDTO(item, models)})
}

func (s *Server) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUpstream(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) listUpstreamModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListUpstreamModels(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) testUpstream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	models, err := s.upstream.Discover(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream_test_failed")
		return
	}
	item, err := s.store.GetUpstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dto := upstreamDTO(item, nil)
	dto["state"] = "healthy"
	dto["modelCount"] = len(models)
	writeJSON(w, http.StatusOK, map[string]any{"item": dto})
}
func (s *Server) discoverModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	models, err := s.upstream.Discover(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "model_discovery_failed")
		return
	}
	stage, err := s.store.StageDiscoveredModels(r.Context(), id, models)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstreamId": id, "discoveredAt": time.Now().UTC().Format(time.RFC3339), "added": stage.Added, "removed": stage.Removed, "unchanged": stage.Unchanged, "complete": true})
}
func (s *Server) applyModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Discovery struct {
			UpstreamID   int64    `json:"upstreamId"`
			DiscoveredAt string   `json:"discoveredAt"`
			Added        []string `json:"added"`
			Removed      []string `json:"removed"`
			Unchanged    []string `json:"unchanged"`
			Complete     bool     `json:"complete"`
		} `json:"discovery"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Discovery.Complete || body.Discovery.UpstreamID != id {
		writeError(w, http.StatusBadRequest, "partial model lists cannot be applied", "incomplete_discovery")
		return
	}
	if _, err := time.Parse(time.RFC3339, body.Discovery.DiscoveredAt); err != nil {
		writeError(w, http.StatusBadRequest, "discovery timestamp is invalid", "invalid_discovery")
		return
	}
	models := append(append([]string{}, body.Discovery.Unchanged...), body.Discovery.Added...)
	_, err := s.store.ApplyDiscoveredModels(r.Context(), id, models)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetUpstream(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	storedModels, _ := s.store.ListUpstreamModels(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": upstreamDTO(item, storedModels)})
}
func (s *Server) listAccountAdapters(w http.ResponseWriter, _ *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.accounts.Adapters()})
}

func (s *Server) getUpstreamAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	account, err := s.accounts.Get(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) configureUpstreamAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body accountsync.ConfigureInput
	if !decodeJSON(w, r, &body) {
		return
	}
	account, err := s.accounts.Configure(r.Context(), id, body)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) deleteUpstreamAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.accounts.Delete(r.Context(), id); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshUpstreamAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	account, err := s.accounts.Refresh(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) listUpstreamAccountUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rangeName := firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("range")), "7d")
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100", "invalid_request")
			return
		}
		limit = parsed
	}
	result, err := s.accounts.Usage(r.Context(), id, rangeName, limit)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) requireAccounts(w http.ResponseWriter) bool {
	if s.accounts != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "account synchronization is unavailable", "service_unavailable")
	return false
}

type routePayload struct {
	PublicModel            string   `json:"publicModel"`
	Model                  string   `json:"model"`
	DisplayName            string   `json:"displayName"`
	Enabled                *bool    `json:"enabled"`
	MonitorEnabled         *bool    `json:"monitorEnabled"`
	Monitored              *bool    `json:"monitored"`
	MonitorIntervalSeconds *int     `json:"monitorIntervalSeconds"`
	CooldownSeconds        *int     `json:"cooldownSeconds"`
	FailureThreshold       *int     `json:"failureThreshold"`
	FailureWindowSeconds   *int     `json:"failureWindowSeconds"`
	TargetModelIDs         *[]int64 `json:"targetModelIds"`
	Targets                []struct {
		UpstreamID  int64  `json:"upstreamId"`
		SourceModel string `json:"sourceModel"`
	} `json:"targets"`
}

func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRoutes(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dtos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, routeDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}
func (s *Server) getRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetRoute(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": routeDTO(item)})
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	var body routePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	targets := []int64{}
	if body.TargetModelIDs != nil {
		targets = *body.TargetModelIDs
	} else if len(body.Targets) > 0 {
		specs := make([]store.RouteTargetSpec, 0, len(body.Targets))
		for _, target := range body.Targets {
			specs = append(specs, store.RouteTargetSpec{UpstreamID: target.UpstreamID, SourceModel: target.SourceModel})
		}
		targets, err = s.store.ResolveRouteTargetModels(r.Context(), specs)
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	monitorFlag := body.MonitorEnabled
	if body.Monitored != nil {
		monitorFlag = body.Monitored
	}
	model := firstNonEmpty(body.Model, body.PublicModel)
	write := store.RouteWrite{PublicModel: model, DisplayName: firstNonEmpty(body.DisplayName, model), Enabled: boolDefault(body.Enabled, true), MonitorEnabled: boolDefault(monitorFlag, false), MonitorIntervalSeconds: intDefault(body.MonitorIntervalSeconds, settings.ProbeIntervalSeconds), CooldownSeconds: intDefault(body.CooldownSeconds, settings.DefaultCooldownSeconds), FailureThreshold: intDefault(body.FailureThreshold, settings.FailureThreshold), FailureWindowSeconds: intDefault(body.FailureWindowSeconds, settings.FailureWindowSeconds), TargetModelIDs: targets}
	if write.PublicModel == "" || len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "publicModel and at least one target are required", "invalid_request")
		return
	}
	id, err := s.store.CreateRoute(r.Context(), write)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetRoute(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"item": routeDTO(item)})
}

func (s *Server) updateRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetRoute(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body routePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	targets := make([]int64, 0, len(current.Targets))
	for _, target := range current.Targets {
		targets = append(targets, target.UpstreamModelID)
	}
	replace := body.TargetModelIDs != nil
	if replace {
		targets = *body.TargetModelIDs
	}
	monitorFlag := body.MonitorEnabled
	if body.Monitored != nil {
		monitorFlag = body.Monitored
	}
	write := store.RouteWrite{PublicModel: firstNonEmpty(body.Model, body.PublicModel, current.PublicModel), DisplayName: firstNonEmpty(body.DisplayName, current.DisplayName, current.PublicModel), Enabled: boolDefault(body.Enabled, current.Enabled), MonitorEnabled: boolDefault(monitorFlag, current.MonitorEnabled), MonitorIntervalSeconds: intDefault(body.MonitorIntervalSeconds, current.MonitorIntervalSeconds), CooldownSeconds: intDefault(body.CooldownSeconds, current.CooldownSeconds), FailureThreshold: intDefault(body.FailureThreshold, current.FailureThreshold), FailureWindowSeconds: intDefault(body.FailureWindowSeconds, current.FailureWindowSeconds), TargetModelIDs: targets}
	if err := s.store.UpdateRoute(r.Context(), id, write, replace); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetRoute(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": routeDTO(item)})
}

func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteRoute(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) reorderTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		TargetIDs []int64 `json:"targetIds"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.ReorderRouteTargets(r.Context(), id, body.TargetIDs); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetRoute(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": routeDTO(item)})
}
func (s *Server) probeRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		TargetID *int64 `json:"targetId"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	_, err := s.gateway.ProbeRoute(r.Context(), id, body.TargetID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetRoute(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": routeDTO(item)})
}

type keyPayload struct {
	Name          string    `json:"name"`
	Enabled       *bool     `json:"enabled"`
	QuotaMicroUSD *int64    `json:"quotaMicroUsd"`
	QuotaUSD      *float64  `json:"quotaUsd"`
	ClearQuota    bool      `json:"clearQuota"`
	RPMLimit      *int      `json:"rpmLimit"`
	AllowedModels *[]string `json:"allowedModels"`
	ExpiresAt     *string   `json:"expiresAt"`
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDownstreamKeys(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dtos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, keyDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var body keyPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	raw, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	quota := body.QuotaMicroUSD
	if body.QuotaUSD != nil {
		value := int64(*body.QuotaUSD * 1_000_000)
		quota = &value
	}
	models := []string{}
	if body.AllowedModels != nil {
		models = *body.AllowedModels
	}
	rpm := 0
	if body.RPMLimit != nil {
		rpm = *body.RPMLimit
	}
	expires, err := parseOptionalTime(body.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	id, err := s.store.CreateDownstreamKey(r.Context(), store.DownstreamKeyWrite{Name: firstNonEmpty(body.Name, "Default key"), Enabled: boolDefault(body.Enabled, true), QuotaMicroUSD: quota, RPMLimit: rpm, AllowedModels: models, ExpiresAt: expires}, prefix, raw)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetDownstreamKey(r.Context(), id)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"item": keyDTO(item), "secret": raw})
}
func (s *Server) updateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetDownstreamKey(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body keyPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	quota := current.QuotaMicroUSD
	if body.ClearQuota {
		quota = nil
	} else if body.QuotaMicroUSD != nil {
		quota = body.QuotaMicroUSD
	} else if body.QuotaUSD != nil {
		value := int64(*body.QuotaUSD * 1_000_000)
		quota = &value
	}
	models := current.AllowedModels
	if body.AllowedModels != nil {
		models = *body.AllowedModels
	}
	rpm := current.RPMLimit
	if body.RPMLimit != nil {
		rpm = *body.RPMLimit
	}
	expires := current.ExpiresAt
	if body.ExpiresAt != nil {
		expires, err = parseOptionalTime(body.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
	}
	err = s.store.UpdateDownstreamKey(r.Context(), id, store.DownstreamKeyWrite{Name: firstNonEmpty(body.Name, current.Name), Enabled: boolDefault(body.Enabled, current.Enabled), QuotaMicroUSD: quota, RPMLimit: rpm, AllowedModels: models, ExpiresAt: expires})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetDownstreamKey(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": keyDTO(item)})
}
func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteDownstreamKey(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) resetKeyUsage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetDownstreamKeyUsage(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetDownstreamKey(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": keyDTO(item)})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, err := s.store.ListRequestLogs(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dtos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, logDTO(item, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}
func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	item, attempts, err := s.store.GetRequestLog(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logDTO(item, attempts))
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO(item))
}
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body struct {
		DefaultCooldownSeconds *int `json:"defaultCooldownSeconds"`
		CooldownSeconds        *int `json:"cooldownSeconds"`
		FailureThreshold       *int `json:"failureThreshold"`
		FailureWindowSeconds   *int `json:"failureWindowSeconds"`
		ProbeIntervalSeconds   *int `json:"probeIntervalSeconds"`
		RequestDeadlineSeconds *int `json:"requestDeadlineSeconds"`
		RequestTimeoutSeconds  *int `json:"requestTimeoutSeconds"`
		MaxAttempts            *int `json:"maxAttempts"`
		LogRetentionDays       *int `json:"logRetentionDays"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cooldown := body.DefaultCooldownSeconds
	if body.CooldownSeconds != nil {
		cooldown = body.CooldownSeconds
	}
	timeout := body.RequestDeadlineSeconds
	if body.RequestTimeoutSeconds != nil {
		timeout = body.RequestTimeoutSeconds
	}
	current.DefaultCooldownSeconds = intDefault(cooldown, current.DefaultCooldownSeconds)
	current.FailureThreshold = intDefault(body.FailureThreshold, current.FailureThreshold)
	current.FailureWindowSeconds = intDefault(body.FailureWindowSeconds, current.FailureWindowSeconds)
	current.ProbeIntervalSeconds = intDefault(body.ProbeIntervalSeconds, current.ProbeIntervalSeconds)
	current.RequestDeadlineSeconds = intDefault(timeout, current.RequestDeadlineSeconds)
	current.MaxAttempts = intDefault(body.MaxAttempts, current.MaxAttempts)
	current.LogRetentionDays = intDefault(body.LogRetentionDays, current.LogRetentionDays)
	item, err := s.store.UpdateSettings(r.Context(), current)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO(item))
}

func (s *Server) secureCookie(r *http.Request) bool {
	return r.TLS != nil || (s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id", "invalid_request")
		return 0, false
	}
	return id, true
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON value", "invalid_request")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDownstreamKeyHasReservations) {
		writeError(w, http.StatusConflict, "downstream key still has active requests", "key_in_use")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "resource not found", "not_found")
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		writeError(w, http.StatusConflict, "resource already exists", "conflict")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error(), "request_failed")
}
func writeAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "resource not found", "not_found")
		return
	}
	if errors.Is(err, accountsync.ErrAccountDisabled) {
		writeError(w, http.StatusConflict, err.Error(), "account_disabled")
		return
	}
	if errors.Is(err, accountsync.ErrSyncInProgress) {
		writeError(w, http.StatusConflict, err.Error(), "sync_in_progress")
		return
	}
	if errors.Is(err, accountsync.ErrInvalidCredentials) || errors.Is(err, accountsync.ErrUnsupportedAdapter) {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_account_configuration")
		return
	}
	var syncErr *accountsync.SyncError
	if errors.As(err, &syncErr) {
		status := http.StatusBadGateway
		switch syncErr.Code {
		case "invalid_configuration", "unsupported", "incompatible_response":
			status = http.StatusUnprocessableEntity
		case "rate_limited":
			status = http.StatusTooManyRequests
		case "timeout":
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, syncErr.Error(), syncErr.Code)
		return
	}
	writeStoreError(w, err)
}
func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"message": message, "error": map[string]any{"message": message, "code": code}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
func intDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseOptionalTime(value *string) (*int64, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil, fmt.Errorf("expiresAt must be an RFC3339 timestamp")
	}
	ms := parsed.UnixMilli()
	return &ms, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func spaHandler(webDir string) http.Handler {
	info, err := os.Stat(webDir)
	if err != nil || !info.IsDir() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"name": "JieShan", "status": "backend ready"})
		})
	}
	fileServer := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(webDir, filepath.Clean(strings.TrimPrefix(r.URL.Path, "/")))
		if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(os.DirFS(webDir), "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

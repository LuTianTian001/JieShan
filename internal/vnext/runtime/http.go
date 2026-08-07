package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
	"github.com/LuTianTian001/JieShan/internal/vnext/webui"
)

type adminProtector interface {
	WrapAdmin(http.Handler) http.Handler
}

type databasePinger interface {
	PingContext(context.Context) error
}

func composeHTTPHandler(
	adminMiddleware adminProtector,
	auth http.Handler,
	webDir string,
	database *vnextstore.Store,
	dataPlane http.Handler,
	inventory http.Handler,
	downstreamKeys http.Handler,
	routingProfiles http.Handler,
	siteAccounts http.Handler,
	pricing http.Handler,
	requestLogs http.Handler,
	systemLogs http.Handler,
	capacitySnapshot http.Handler,
	monitor http.Handler,
	settings http.Handler,
) (http.Handler, error) {
	if nilLike(adminMiddleware) || nilLike(auth) || database == nil || nilLike(dataPlane) || nilLike(inventory) ||
		nilLike(downstreamKeys) || nilLike(routingProfiles) || nilLike(siteAccounts) || nilLike(pricing) ||
		nilLike(requestLogs) || nilLike(systemLogs) || nilLike(capacitySnapshot) || nilLike(monitor) || nilLike(settings) {
		return nil, errors.New("JieShan HTTP dependencies are incomplete")
	}

	adminMux := http.NewServeMux()
	adminMux.Handle(InventoryAdminPrefix, inventory)
	adminMux.Handle(InventoryAdminPrefix+"/", inventory)
	adminMux.Handle(DownstreamKeysAdminPrefix, downstreamKeys)
	adminMux.Handle(DownstreamKeysAdminPrefix+"/", downstreamKeys)
	adminMux.Handle(RoutingProfilesAdminPrefix, routingProfiles)
	adminMux.Handle(RoutingProfilesAdminPrefix+"/", routingProfiles)
	adminMux.Handle(SiteAccountsAdminPrefix, siteAccounts)
	adminMux.Handle(SiteAccountsAdminPrefix+"/", siteAccounts)
	adminMux.Handle(PricingAdminPrefix, pricing)
	adminMux.Handle(PricingAdminPrefix+"/", pricing)
	adminMux.Handle(RequestLogsAdminPrefix, requestLogs)
	adminMux.Handle(RequestLogsAdminPrefix+"/", requestLogs)
	adminMux.Handle(SystemLogsAdminPrefix, systemLogs)
	adminMux.Handle(SystemLogsAdminPrefix+"/", systemLogs)
	adminMux.Handle(CapacityAdminPrefix, capacitySnapshot)
	adminMux.Handle(CapacityAdminPrefix+"/", capacitySnapshot)
	adminMux.Handle(MonitorAdminPrefix, monitor)
	adminMux.Handle(MonitorAdminPrefix+"/", monitor)
	adminMux.Handle(SettingsAdminPrefix, settings)
	adminMux.Handle(SettingsAdminPrefix+"/", settings)
	protectedAdmin := adminMiddleware.WrapAdmin(adminMux)
	if nilLike(protectedAdmin) {
		return nil, errors.New("JieShan administrator middleware returned no handler")
	}

	reserved := http.NewServeMux()
	reserved.Handle(HealthPath, healthHandler{database: database.DB})
	reserved.Handle(AuthPrefix, auth)
	reserved.Handle(AuthPrefix+"/", auth)
	reserved.Handle("/api/vnext/", protectedAdmin)
	reserved.Handle("/", dataPlane)
	application, err := webui.New(webui.Options{DistDir: webDir, ReservedHandler: reserved})
	if err != nil {
		return nil, fmt.Errorf("create JieShan web UI handler: %w", err)
	}
	return application, nil
}

type healthHandler struct {
	database databasePinger
}

func (handler healthHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeHealthJSON(writer, http.StatusMethodNotAllowed, map[string]string{
			"status": "error", "stack": "jieshan", "error": "method_not_allowed",
		}, request.Method == http.MethodHead)
		return
	}
	if handler.database == nil {
		writeHealthJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable", "stack": "jieshan",
		}, request.Method == http.MethodHead)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := handler.database.PingContext(ctx); err != nil {
		status := "unavailable"
		if errors.Is(err, sql.ErrConnDone) {
			status = "closed"
		}
		writeHealthJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"status": status, "stack": "jieshan",
		}, request.Method == http.MethodHead)
		return
	}
	writeHealthJSON(writer, http.StatusOK, map[string]string{
		"status": "ok", "stack": "jieshan",
	}, request.Method == http.MethodHead)
}

func writeHealthJSON(writer http.ResponseWriter, status int, value any, head bool) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if !head {
		_ = json.NewEncoder(writer).Encode(value)
	}
}

package inventoryapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/LuTianTian001/JieShan/internal/vnext/platformdetect"
)

type PlatformDetector interface {
	Detect(context.Context, platformdetect.Input) platformdetect.Result
}

type platformSelectionRepository interface {
	GetPlatformSelection(context.Context, int64) (*platformdetect.ManualSelection, error)
}

func (handler *Handler) getPlatformDetection(w http.ResponseWriter, r *http.Request, siteID int64) {
	if handler.platformDetector == nil {
		writeError(w, http.StatusServiceUnavailable, "platform_detection_unavailable", "platform detection is unavailable")
		return
	}
	site, err := handler.repository.GetSite(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	var manual *platformdetect.ManualSelection
	if repository, ok := handler.repository.(platformSelectionRepository); ok {
		manual, err = repository.GetPlatformSelection(r.Context(), siteID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeRepositoryError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, handler.platformDetector.Detect(r.Context(), platformdetect.Input{
		SiteID: site.ID, Origin: site.DashboardURL, Manual: manual,
	}))
}

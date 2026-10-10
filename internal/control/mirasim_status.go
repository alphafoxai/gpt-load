package control

import (
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/mirasimstatus"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
)

// handleMirasimStatus is deliberately independent of local service state. No
// caller-provided URL, headers, cookies, credentials or force-refresh is used.
func (s *Server) handleMirasimStatus(c *gin.Context) {
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		writeServiceError(c, "mirasim_status", app_errors.ErrBadRequest)
		return
	}
	c.Header("Cache-Control", "no-store")
	if s.mirasimStatus == nil {
		response.SuccessI18n(c, "common.success", mirasimstatus.Snapshot{
			SourceURL: mirasimstatus.SourceURL,
			CheckedAt: time.Now().UTC(),
			Stale:     true,
			Error:     "Mirasim status: reader is unavailable",
		})
		return
	}
	response.SuccessI18n(c, "common.success", s.mirasimStatus.Snapshot(c.Request.Context()))
}

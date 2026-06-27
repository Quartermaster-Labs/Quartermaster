package server

import (
	"net/http"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/shared"
)

// handleAPIUpdate downloads the latest release installer and launches it, then
// triggers a graceful shutdown so the installer can replace the running exe.
// Windows release builds only; 409 when no update is available.
func (s *Server) handleAPIUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil || !s.updater.Enabled() {
		shared.SendResponse(w, r, http.StatusNotImplemented, "auto-update is not available for this build")
		return
	}
	if !s.updater.Status().Available {
		shared.SendResponse(w, r, http.StatusConflict, "no update available")
		return
	}
	if err := s.updater.DownloadAndLaunch(r.Context()); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	shared.SendResponse(w, r, http.StatusOK, "installer launched; shutting down to apply the update")

	// Give the response time to flush, then shut down so the installer can
	// overwrite the locked executable. The wizard is interactive (several
	// pages), so the app is long gone before it reaches the file-copy step.
	if s.shutdownHook != nil {
		go func() {
			time.Sleep(time.Second)
			s.shutdownHook()
		}()
	}
}

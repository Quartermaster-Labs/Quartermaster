package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
	"github.com/quartermaster-labs/quartermaster/internal/update"
)

// handleAPIUpdate starts an update and returns immediately.
//
// The work runs on the server's own lifetime context, NOT the request's: the
// download is tens of MB, and a browser tab closing (or a reverse proxy timing
// out an idle request) must not abort an update that is already swapping files
// on disk. Callers poll GET /api/update/status for progress.
func (s *Server) handleAPIUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil || !s.updater.Enabled() {
		shared.SendResponse(w, r, http.StatusNotImplemented, "auto-update is not available for this build")
		return
	}
	st := s.updater.Status()
	if st.Blocked != "" {
		shared.SendResponse(w, r, http.StatusConflict, "cannot self-update here: "+st.Blocked)
		return
	}
	if !st.Available {
		shared.SendResponse(w, r, http.StatusConflict, "no update available")
		return
	}

	go func() {
		if err := s.updater.Apply(s.shutdownCtx); err != nil {
			return // the error is on the status the UI is polling
		}
		// A desktop install restarts itself: shut down, and main relaunches the
		// swapped binary once the listen sockets are free. A supervised one does
		// not — the swap is staged and the operator's next restart picks it up.
		if s.updater.Status().Restart != update.RestartAuto || s.shutdownHook == nil {
			return
		}
		// Let the last status poll land before the server goes away.
		time.Sleep(time.Second)
		s.shutdownHook()
	}()

	shared.SendResponse(w, r, http.StatusAccepted, "update started")
}

// handleAPIUpdateStatus reports check + apply progress, including why a swap is
// impossible here (container, read-only install dir) and who restarts after it.
func (s *Server) handleAPIUpdateStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.updater == nil {
		json.NewEncoder(w).Encode(update.Status{Phase: update.PhaseIdle})
		return
	}
	json.NewEncoder(w).Encode(s.updater.Status())
}

// RelaunchPending reports whether an applied update wants this process replaced
// by the swapped-in binary once shutdown finishes. main asks after teardown, so
// the replacement starts only after the listen sockets are released.
func (s *Server) RelaunchPending() bool {
	return s.updater != nil && s.updater.Relaunch()
}

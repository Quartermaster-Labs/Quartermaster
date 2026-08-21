package server

import (
	"context"
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

// handleAPIUpdateCheck polls the release feed on demand and answers with the
// status the poll produced, so the caller needs no follow-up GET.
//
// The six-hourly poll already keeps the status fresh; this exists because a
// user who just published (or just heard about) a release should not have to
// wait out that tick, and because a "check for updates" control that only reads
// a cached answer is a lie. Bounded by its own timeout rather than the
// request's -- an unreachable GitHub must fail as a check, not hang the tab.
func (s *Server) handleAPIUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil || !s.updater.Enabled() {
		shared.SendResponse(w, r, http.StatusNotImplemented, "this build does not check for updates")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.updater.CheckNow(ctx); err != nil {
		// 502, not 500: the failure is GitHub's or the network's, and the
		// message is what the UI shows instead of a silent no-op.
		shared.SendResponse(w, r, http.StatusBadGateway, "update check failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.updater.Status())
}

// RelaunchPending reports whether an applied update wants this process replaced
// by the swapped-in binary once shutdown finishes. main asks after teardown, so
// the replacement starts only after the listen sockets are released.
func (s *Server) RelaunchPending() bool {
	return s.updater != nil && s.updater.Relaunch()
}

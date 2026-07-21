package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// Autostart ("start with the system") is deliberately NOT part of the autogen
// sidecar: it is per-machine OS state, not per-install config, and several
// quartermaster installs can exist side by side on one box. All of them write
// the SAME single registry value (autostartValueName), so at most one install
// ever launches at login. The dashboard reads back the stored command line and,
// when it points at a different exe, shows who owns it plus a "take over"
// action instead of silently clobbering the other install.
//
// Platform hooks live in autostart_windows.go / autostart_other.go:
//
//	autostartSupported() bool
//	autostartRead() (cmd string, err error)
//	autostartWrite(cmd string) error
//	autostartClear() error
const autostartValueName = "Quartermaster"

type autostartStatus struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`   // a Run entry exists (ours or someone else's)
	OwnedByUs bool   `json:"ownedByUs"` // ...and it points at this exe
	OwnerExe  string `json:"ownerExe"`  // exe parsed out of the stored command
	OwnerCmd  string `json:"ownerCmd"`  // full stored command
	SelfExe   string `json:"selfExe"`
	SelfCmd   string `json:"selfCmd"` // what we would write
}

type autostartPutBody struct {
	Enabled bool `json:"enabled"`
	// Takeover permits overwriting/removing an entry owned by a different
	// install. The UI only sets it after the user clicks "Take over".
	Takeover bool `json:"takeover"`
}

// autostartSelfCommand rebuilds this process's launch line for the Run key:
// the absolute exe path plus the flags it was started with, minus any -tray
// form (autostart always runs as a tray app, per the setting's contract).
// Relative path arguments are made absolute because the Run key launches with
// an arbitrary working directory.
func autostartSelfCommand() (exe string, cmd string, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", "", err
	}
	exe, _ = filepath.Abs(exe)
	parts := []string{quoteArg(exe)}
	for _, a := range os.Args[1:] {
		name, val, hasVal := strings.Cut(a, "=")
		if strings.HasPrefix(strings.TrimLeft(name, "-"), "tray") && strings.HasPrefix(name, "-") {
			continue // re-added below in canonical form
		}
		if hasVal {
			parts = append(parts, quoteArg(name+"="+absIfPath(val)))
			continue
		}
		parts = append(parts, quoteArg(absIfPath(a)))
	}
	parts = append(parts, "-tray")
	return exe, strings.Join(parts, " "), nil
}

// absIfPath absolutises an argument that looks like a relative path to an
// existing file/dir. Non-path arguments (":8080", "5s", flag names) pass
// through untouched.
func absIfPath(a string) string {
	if a == "" || strings.HasPrefix(a, "-") || filepath.IsAbs(a) {
		return a
	}
	if _, err := os.Stat(a); err != nil {
		return a
	}
	abs, err := filepath.Abs(a)
	if err != nil {
		return a
	}
	return abs
}

func quoteArg(a string) string {
	if a == "" || strings.ContainsAny(a, " \t\"") {
		return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return a
}

// autostartExeOf pulls the executable out of a stored command line, honouring
// a leading quoted path ("C:\Program Files\...\quartermaster.exe" -tray).
func autostartExeOf(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if cmd[0] == '"' {
		if end := strings.IndexByte(cmd[1:], '"'); end >= 0 {
			return cmd[1 : end+1]
		}
		return strings.Trim(cmd, `"`)
	}
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

func sameExe(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, err1 := filepath.Abs(filepath.Clean(a))
	cb, err2 := filepath.Abs(filepath.Clean(b))
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(ca, cb) // Windows paths are case-insensitive
}

func (s *Server) autostartStatus() (autostartStatus, error) {
	selfExe, selfCmd, err := autostartSelfCommand()
	if err != nil {
		return autostartStatus{}, err
	}
	st := autostartStatus{Supported: autostartSupported(), SelfExe: selfExe, SelfCmd: selfCmd}
	if !st.Supported {
		return st, nil
	}
	cur, err := autostartRead()
	if err != nil {
		return st, err
	}
	if cur != "" {
		st.Enabled = true
		st.OwnerCmd = cur
		st.OwnerExe = autostartExeOf(cur)
		st.OwnedByUs = sameExe(st.OwnerExe, selfExe)
	}
	return st, nil
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleAPIAutostartGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.autostartStatus()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading autostart failed: "+err.Error())
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleAPIAutostartPut(w http.ResponseWriter, r *http.Request) {
	var body autostartPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	st, err := s.autostartStatus()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading autostart failed: "+err.Error())
		return
	}
	if !st.Supported {
		shared.SendResponse(w, r, http.StatusNotImplemented, "start with the system is only supported on Windows")
		return
	}
	// Another install owns the shared Run entry: refuse until the user
	// explicitly takes over, so two installs can't both fight for the ports.
	if st.Enabled && !st.OwnedByUs && !body.Takeover {
		writeJSONStatus(w, http.StatusConflict, st)
		return
	}
	if body.Enabled {
		err = autostartWrite(st.SelfCmd)
	} else {
		err = autostartClear()
	}
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "writing autostart failed: "+err.Error())
		return
	}
	st, err = s.autostartStatus()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, st)
}

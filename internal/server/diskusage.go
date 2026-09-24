package server

// How much disk the models folder takes, for the dashboard's "On disk" tile.
//
// The tile used to sum each catalog row's weights file, which is wrong in both
// directions: autogen lists several launch profiles of one gguf (ctx tiers,
// the -vision twin) as separate rows, so one file counted up to five times,
// while mmproj/draft/VAE/encoder files that no row names as its weights were
// never counted at all. Measured on a real catalog: 956 GB summed vs ~420 GB
// on disk. Walking the folder is the only answer that agrees with the OS file
// manager, so that is what this does.
//
// filepath.WalkDir behaves the same on every platform. Symlinks are NOT
// followed (the walk reports the link, not its target), which is what Explorer,
// Finder and a plain `du` do too, and it keeps a link pointing back up the tree
// from looping forever.

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// The dashboard asks on every visit; a models tree on a spinning disk is a
// second or more to walk, and its total moves only when a download lands.
const diskUsageTTL = 2 * time.Minute

type diskUsageDTO struct {
	Root  string `json:"root"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// Skipped counts entries the walk could not read (permissions, a file
	// deleted mid-walk). Non-zero means Bytes is a floor, not the total.
	Skipped   int    `json:"skipped,omitempty"`
	ScannedAt string `json:"scannedAt"`
}

// One cached result, keyed by the root it measured: a live reload can move the
// models folder, and the old tree's total must not be served for the new one.
// The mutex is held across the walk, so concurrent callers wait for the one
// walk in flight instead of each starting their own.
var diskUsageCache struct {
	mu   sync.Mutex
	root string
	at   time.Time
	res  diskUsageDTO
}

// handleAPIHubDiskUsage reports the total size of every file under the models
// root. 501 when there is no models root (no -generate), which the UI treats as
// "fall back to the catalog's own sizes".
func (s *Server) handleAPIHubDiskUsage(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.hubModelsRoot())
	if root == "" {
		shared.SendResponse(w, r, http.StatusNotImplemented, "the models folder is not configured in this build")
		return
	}
	res, err := modelsDiskUsage(root, r.URL.Query().Get("refresh") != "")
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "cannot measure the models folder: "+err.Error())
		return
	}
	writeJSON(w, res)
}

func modelsDiskUsage(root string, force bool) (diskUsageDTO, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return diskUsageDTO{}, err
	}
	c := &diskUsageCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.root == abs && time.Since(c.at) < diskUsageTTL {
		return c.res, nil
	}
	res, err := walkDiskUsage(abs)
	if err != nil {
		return diskUsageDTO{}, err
	}
	c.root, c.at, c.res = abs, time.Now(), res
	return res, nil
}

// walkDiskUsage sums the apparent size of every regular file under root.
// Split out so it can be tested against a temp dir without a Server.
func walkDiskUsage(root string) (diskUsageDTO, error) {
	// The root itself may be a symlink (a models folder moved to another drive
	// and linked back). Resolve it, or WalkDir sees a link and walks nothing.
	start, err := filepath.EvalSymlinks(root)
	if err != nil {
		return diskUsageDTO{}, err
	}
	res := diskUsageDTO{Root: root}
	err = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == start {
				return err
			}
			// An unreadable subtree or a file that vanished mid-walk costs its
			// own bytes, not the whole answer.
			res.Skipped++
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			res.Skipped++
			return nil
		}
		res.Bytes += fi.Size()
		res.Files++
		return nil
	})
	if err != nil {
		return diskUsageDTO{}, err
	}
	res.ScannedAt = time.Now().UTC().Format(time.RFC3339)
	return res, nil
}

package api

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/replace"
)

// libraryDisks reports total/free space per filesystem the libraries
// live on, deduplicated by device (replaces a hardcoded pool path).
func libraryDisks(libs []config.Library) []map[string]any {
	type fsRow struct {
		names       []string
		total, free int64
	}
	byDev := map[uint64]*fsRow{}
	var order []uint64
	for _, l := range libs {
		var fs syscall.Statfs_t
		var st syscall.Stat_t
		if syscall.Statfs(l.Path, &fs) != nil || syscall.Stat(l.Path, &st) != nil {
			continue
		}
		dev := uint64(st.Dev)
		if byDev[dev] == nil {
			byDev[dev] = &fsRow{total: int64(fs.Blocks) * fs.Bsize, free: int64(fs.Bavail) * fs.Bsize}
			order = append(order, dev)
		}
		byDev[dev].names = append(byDev[dev].names, l.Name)
	}
	out := []map[string]any{}
	for _, d := range order {
		r := byDev[d]
		out = append(out, map[string]any{"libraries": r.names, "total": r.total, "free": r.free})
	}
	return out
}

// mountsFile is overridable in tests.
var mountsFile = "/proc/self/mounts"

// skipMount hides system and app-internal mounts from the directory
// browser's top level.
func skipMount(p string) bool {
	if p == "/" {
		return true
	}
	for _, pre := range []string{"/proc", "/sys", "/dev", "/run", "/etc", "/usr", "/config", "/tmp", "/var", "/boot", "/lib", "/opt/distillarr"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// mountPoints lists the container's user-visible mounts (where the
// user's media volumes are).
func mountPoints() []string {
	f, err := os.Open(mountsFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		p := strings.ReplaceAll(fields[1], `\040`, " ")
		if skipMount(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// browseDirs lists subdirectories for the setup wizard's library picker.
// With no path it returns the mount points; otherwise the path's child
// directories (hidden ones left out). Only directories are ever listed.
func (s *Server) browseDirs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "dirs": mountPoints()})
		return
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		fail(w, 400, os.ErrInvalid)
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		fail(w, 400, err)
		return
	}
	dirs := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(p, e.Name()))
		} else if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(p, e.Name())); err == nil && fi.IsDir() {
				dirs = append(dirs, filepath.Join(p, e.Name()))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": filepath.Dir(p), "dirs": dirs})
}

// trashDirs says where each library's originals go on replace and
// whether that library sits on a mergerfs pool, for Settings.
func trashDirs(cfg config.Config) []map[string]any {
	out := []map[string]any{}
	for _, l := range cfg.Libraries {
		_, fstype := replace.MountPoint(l.Path)
		out = append(out, map[string]any{"library": l.Name, "path": l.Path,
			"trash_dir": jobs.TrashDirFor(cfg, filepath.Join(l.Path, "x")), "mergerfs": fstype == "fuse.mergerfs"})
	}
	return out
}

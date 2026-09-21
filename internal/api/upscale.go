package api

import (
	"fmt"
	"net/http"

	"mediatrans/internal/encode"
	"mediatrans/internal/hwprobe"
	"mediatrans/internal/recs"
	"mediatrans/internal/res"
	"mediatrans/internal/still"
	"mediatrans/internal/upscale"
)

// upscaleTarget is one resolution a file can be upscaled to.
type upscaleTarget struct {
	Class int `json:"class"`
	W     int `json:"w"`
	H     int `json:"h"`
}

// upscaleSuggestion is the backend's recommended starting point.
type upscaleSuggestion struct {
	To      int    `json:"to"`
	Preset  string `json:"preset"`
	Content string `json:"content"` // film | anime
	Why     string `json:"why"`
}

// upscaleInfo describes what upscaling a file offers and what to start with.
func (s *Server) upscaleInfo(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	class := res.Class(f.Width, f.Height)
	targets := []upscaleTarget{}
	for _, c := range []int{720, 1080, 2160} {
		if ow, oh, ok := res.Up(f.Width, f.Height, c); ok {
			targets = append(targets, upscaleTarget{Class: c, W: ow, H: oh})
		}
	}

	content, why := upscale.Film, "live action"
	if recs.IsAnimation(f) {
		content, why = upscale.Anime, "tagged animation in Jellyfin"
	}
	sug := upscaleSuggestion{Preset: upscale.ForContent(content).ID, Content: content}
	// One step up is the sensible default: SD and 720p go to 1080p, 1080p to 4K.
	switch {
	case class >= 2160:
	case class >= 1080:
		sug.To = 2160
	default:
		sug.To = 1080
	}
	sug.Why = fmt.Sprintf("%s, %dp source", why, class)

	rep := s.eng.Report()
	var devs []hwprobe.VulkanDevice
	if rep != nil {
		devs = rep.Vulkan
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":    map[string]any{"w": f.Width, "h": f.Height, "class": class},
		"targets":   targets,
		"suggested": sug,
		"presets":   upscale.All(),
		"vulkan":    devs,
		"available": hwprobe.BestVulkan(rep, "") != nil,
	})
}

// stillReq asks for one A/B frame.
type stillReq struct {
	Settings encode.Settings `json:"settings"`
	At       float64         `json:"at"`
}

// makeStill renders (or returns cached) a standard-resize frame and the same
// frame through the real upscale chain, for tuning.
func (s *Server) makeStill(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req stillReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if req.Settings.UpscaleTo <= 0 {
		fail(w, 400, fmt.Errorf("choose an upscale target"))
		return
	}
	if hwprobe.BestVulkan(s.eng.Report(), "") == nil {
		fail(w, 409, fmt.Errorf("no working Vulkan device: upscaling is unavailable on this host"))
		return
	}
	st := s.concrete(req.Settings)
	res, err := s.still.Make(r.Context(), still.Request{
		FileID: f.ID, Path: f.Path, Size: f.Size, MtimeNS: f.MtimeNS,
		Duration: f.Duration, At: req.At, Settings: st,
	})
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key": res.Key, "w": res.W, "h": res.H, "at": res.At, "ms": res.MS, "cached": res.Cached,
		"a_url": "/api/v1/stills/" + res.Key + "/a.png",
		"b_url": "/api/v1/stills/" + res.Key + "/b.png",
	})
}

// stillFile serves a rendered still. Keys are content hashes, so a URL never
// changes meaning and can be cached indefinitely.
func (s *Server) stillFile(w http.ResponseWriter, r *http.Request) {
	path, ok := s.still.Path(r.PathValue("key"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

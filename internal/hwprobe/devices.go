// SPDX-License-Identifier: GPL-3.0-or-later

package hwprobe

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CreatureSurvive/distillarr/internal/encode"
)

// Device names an encoder device for display: a render node, the NVIDIA GPU
// or the CPU. The UI shows these instead of any hardcoded GPU model.
type Device struct {
	Backend encode.Backend `json:"backend"`
	Node    string         `json:"node,omitempty"`
	Name    string         `json:"name"`
}

var (
	nameNoise   = strings.NewReplacer("(R)", "", "(r)", "", "(TM)", "", "(tm)", "")
	trailParens = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	pciVendors  = map[string]string{"0x8086": "Intel", "0x1002": "AMD", "0x10de": "NVIDIA"}
)

// cleanName turns a driver's device string ("Intel(R) Arc(tm) A380 Graphics
// (DG2)") into a display name ("Intel Arc A380 Graphics").
func cleanName(s string) string {
	s = trailParens.ReplaceAllString(nameNoise.Replace(s), "")
	return strings.Join(strings.Fields(s), " ")
}

// DeviceName names a render node. The Vulkan listing carries the driver's
// own model name for nodes it was paired with; otherwise the PCI vendor from
// sysfs gives at least "Intel GPU (renderD128)".
func (r *Report) DeviceName(node string) string {
	if r != nil {
		for _, v := range r.Vulkan {
			if v.Node == node && v.Name != "" {
				if n := cleanName(v.Name); n != "" {
					return n
				}
			}
		}
	}
	base := filepath.Base(node)
	b, err := os.ReadFile(filepath.Join("/sys/class/drm", base, "device", "vendor"))
	if err == nil {
		if v, ok := pciVendors[strings.ToLower(strings.TrimSpace(string(b)))]; ok {
			return v + " GPU (" + base + ")"
		}
	}
	return base
}

// Devices names every render node in the report.
func (r *Report) Devices() map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	for _, n := range r.RenderNodes {
		out[n] = r.DeviceName(n)
	}
	return out
}

// ActiveDevice is the device a backend+codec encode runs on.
func (r *Report) ActiveDevice(b encode.Backend, c encode.Codec) Device {
	switch b {
	case encode.SW:
		return Device{Backend: b, Name: "CPU (software)"}
	case encode.NVENC:
		if r != nil {
			for _, v := range r.Vulkan {
				if strings.Contains(strings.ToUpper(v.Name), "NVIDIA") {
					return Device{Backend: b, Name: cleanName(v.Name)}
				}
			}
		}
		return Device{Backend: b, Name: "NVIDIA GPU"}
	}
	if r == nil {
		return Device{Backend: b, Name: "GPU"}
	}
	node := NodeFor(r, b, c)
	return Device{Backend: b, Node: node, Name: r.DeviceName(node)}
}

// UpscaleDevice names the GPU upscales run on by default, or "" when none works.
func (r *Report) UpscaleDevice() string {
	if d := BestVulkan(r, ""); d != nil {
		return cleanName(d.Name)
	}
	return ""
}

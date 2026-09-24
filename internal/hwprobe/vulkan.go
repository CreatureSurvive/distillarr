package hwprobe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/media"
)

// VulkanDevice is one Vulkan-capable GPU as libplacebo (the upscaler) sees
// it. Vulkan picks devices by index, not by DRM path, so Node records which
// render node is the same physical GPU (matched by PCI device id); the two
// can differ in ordering, which is why neither is ever hardcoded.
type VulkanDevice struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	DeviceID string `json:"device_id"`      // PCI device id, e.g. 0x56a5
	Discrete bool   `json:"discrete"`       // discrete GPU (vs integrated)
	Node     string `json:"node,omitempty"` // matching /dev/dri render node
	OK       bool   `json:"ok"`             // libplacebo smoke test passed
	MS       int64  `json:"ms"`
	Error    string `json:"error,omitempty"`
}

// vkListing matches ffmpeg's verbose "GPU listing" rows, e.g.
//
//	[AVHWDeviceContext @ 0x58e4] 0: Intel(R) Arc(tm) A380 Graphics (DG2) (discrete) (0x56a5)
//
// The queue-family rows ("0: graphics compute transfer ...") don't end in a
// device type and hex id, so they never match.
var vkListing = regexp.MustCompile(`\]\s+(\d+): (.+) \((discrete|integrated|virtual|cpu|other)\) \((0x[0-9a-fA-F]+)\)\s*$`)

// parseVulkanListing extracts the enumerated GPUs from ffmpeg's verbose
// output, in index order and without duplicates.
func parseVulkanListing(out string) []VulkanDevice {
	seen := map[int]bool{}
	var devs []VulkanDevice
	for _, line := range strings.Split(out, "\n") {
		m := vkListing.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || seen[idx] {
			continue
		}
		seen[idx] = true
		devs = append(devs, VulkanDevice{
			Index:    idx,
			Name:     m[2],
			DeviceID: strings.ToLower(m[4]),
			Discrete: m[3] == "discrete",
		})
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Index < devs[j].Index })
	return devs
}

// nodeDeviceIDs maps each render node to its PCI device id via sysfs.
func nodeDeviceIDs(nodes []string) map[string]string {
	ids := map[string]string{}
	for _, n := range nodes {
		b, err := os.ReadFile(filepath.Join("/sys/class/drm", filepath.Base(n), "device", "device"))
		if err == nil {
			ids[n] = strings.ToLower(strings.TrimSpace(string(b)))
		}
	}
	return ids
}

// matchNodes assigns each device the render node with the same PCI device
// id. Each node is used once, so two identical GPUs pair up in order rather
// than both claiming the first node.
func matchNodes(devs []VulkanDevice, nodes []string, ids map[string]string) {
	used := map[string]bool{}
	for i := range devs {
		for _, n := range nodes {
			if !used[n] && ids[n] == devs[i].DeviceID {
				devs[i].Node = n
				used[n] = true
				break
			}
		}
	}
}

// Both probe commands print per-frame signalstats for a colour-bar pattern
// 2x upscaled: the reference in plain software, the candidate through the real
// libplacebo chain on Vulkan device idx. Exiting cleanly is not proof of a
// working upscaler: through Vulkan on Mesa, libplacebo can return YUV frames
// whose chroma reads back as zero (a solid green picture) with no error at
// all, so the candidate's average chroma has to match the reference.
const statsTail = ",signalstats,metadata=mode=print:file=-"

func upscaleProbeArgs(vf string, vk ...string) []string {
	return cat([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, vk,
		[]string{"-f", "lavfi", "-i", "smptehdbars=size=320x240:rate=10:duration=1",
			"-vf", vf + statsTail, "-frames:v", "5", "-f", "null", "-"})
}

func referenceProbeArgs() []string {
	return upscaleProbeArgs("scale=640:480:flags=lanczos,format=yuv420p")
}

// vulkanProbeArgs runs the same chain the encoder uses (RGB out of libplacebo,
// BT.709 YUV from swscale); see upscale.Spec.Chain.
func vulkanProbeArgs(idx int) []string {
	return upscaleProbeArgs("format=nv12,hwupload,libplacebo=w=640:h=480:upscaler=ewa_lanczos:format=rgba,"+
		"hwdownload,format=rgba,scale=out_color_matrix=bt709:out_range=tv,format=yuv420p",
		"-init_hw_device", "vulkan=vk:"+strconv.Itoa(idx), "-filter_hw_device", "vk")
}

// chromaAvg averages the U and V means over every frame in signalstats output.
func chromaAvg(out string) (u, v float64, ok bool) { return media.MeanChroma(out) }

// chromaTolerance is how far (of 255) the candidate's mean chroma may sit from
// the reference's. The two decode the pattern with slightly different
// matrices, worth a few levels; the broken readback is off by ~128.
const chromaTolerance = 12.0

// chromaMatches reports whether a candidate's chroma agrees with the reference.
func chromaMatches(ru, rv, cu, cv float64) bool {
	return abs(ru-cu) <= chromaTolerance && abs(rv-cv) <= chromaTolerance
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// probeVulkan enumerates Vulkan GPUs, pairs them with render nodes and runs
// the upscale smoke test on each. An ffmpeg without Vulkan, or a host with
// no Vulkan driver, yields an empty list rather than an error: upscaling is
// simply unavailable there.
func probeVulkan(nodes []string) []VulkanDevice {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _ := execCommandCombined(ctx, "ffmpeg", "-hide_banner", "-loglevel", "verbose",
		"-init_hw_device", "vulkan=vk:0", "-f", "lavfi", "-i", "nullsrc=d=0.01", "-f", "null", "-")
	devs := parseVulkanListing(string(out))
	matchNodes(devs, nodes, nodeDeviceIDs(nodes))

	// One software reference serves every device.
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	rout, rerr := execCommandCombined(rctx, "ffmpeg", referenceProbeArgs()...)
	rcancel()
	ru, rv, refOK := chromaAvg(string(rout))
	if rerr != nil || !refOK {
		for i := range devs {
			devs[i].Error = "no software reference to compare against: " + trimErr(string(rout))
		}
		return devs
	}

	for i := range devs {
		start := time.Now()
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		o, err := execCommandCombined(sctx, "ffmpeg", vulkanProbeArgs(devs[i].Index)...)
		scancel()
		devs[i].MS = time.Since(start).Milliseconds()
		switch cu, cv, ok := chromaAvg(string(o)); {
		case err != nil:
			if devs[i].Error = trimErr(string(o)); devs[i].Error == "" {
				devs[i].Error = err.Error()
			}
		case !ok:
			devs[i].Error = "upscale produced no frames to measure"
		case !chromaMatches(ru, rv, cu, cv):
			devs[i].Error = fmt.Sprintf("upscaled colour is wrong (mean U/V %.0f/%.0f, expected %.0f/%.0f): driver returns corrupt frames",
				cu, cv, ru, rv)
		default:
			devs[i].OK = true
		}
	}
	return devs
}

// BestVulkan returns the working Vulkan device to run the upscaler on. When
// the encode's render node is itself a working Vulkan device it wins, which
// keeps decode, upscale and encode on one GPU; otherwise the discrete GPU,
// then the fastest smoke test. nil means no usable Vulkan device.
func BestVulkan(rep *Report, encodeNode string) *VulkanDevice {
	if rep == nil {
		return nil
	}
	var best *VulkanDevice
	for i := range rep.Vulkan {
		d := &rep.Vulkan[i]
		if !d.OK {
			continue
		}
		if encodeNode != "" && d.Node == encodeNode {
			return d
		}
		if best == nil || (d.Discrete && !best.Discrete) || (d.Discrete == best.Discrete && d.MS < best.MS) {
			best = d
		}
	}
	return best
}

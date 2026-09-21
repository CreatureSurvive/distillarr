package hwprobe

import "testing"

// Captured from `ffmpeg -loglevel verbose -init_hw_device vulkan=vk:0` on
// example: Arc A380 at Vulkan index 0, UHD 630 at index 1.
const vkOut = `[AVHWDeviceContext @ 0x58e45ad44980] Supported layers:
[AVHWDeviceContext @ 0x58e45ad44980] 	VK_LAYER_MESA_device_select
[AVHWDeviceContext @ 0x58e45ad44980] GPU listing:
[AVHWDeviceContext @ 0x58e45ad44980]     0: Intel(R) Arc(tm) A380 Graphics (DG2) (discrete) (0x56a5)
[AVHWDeviceContext @ 0x58e45ad44980]     1: Intel(R) UHD Graphics 630 (CML GT2) (integrated) (0x9bc8)
[AVHWDeviceContext @ 0x58e45ad44980] Requested device: 0
[AVHWDeviceContext @ 0x58e45ad44980]     0: graphics compute transfer sparse (queues: 1)
`

func TestParseVulkanListing(t *testing.T) {
	devs := parseVulkanListing(vkOut)
	if len(devs) != 2 {
		t.Fatalf("want 2 devices (queue-family rows must not match), got %d: %+v", len(devs), devs)
	}
	a, u := devs[0], devs[1]
	if a.Index != 0 || a.DeviceID != "0x56a5" || !a.Discrete || a.Name != "Intel(R) Arc(tm) A380 Graphics (DG2)" {
		t.Errorf("Arc parsed wrong: %+v", a)
	}
	if u.Index != 1 || u.DeviceID != "0x9bc8" || u.Discrete {
		t.Errorf("UHD 630 parsed wrong: %+v", u)
	}
	if got := parseVulkanListing("no gpus here\nDevice creation failed: -19.\n"); len(got) != 0 {
		t.Errorf("a host without Vulkan must yield no devices, got %+v", got)
	}
	if got := parseVulkanListing(vkOut + vkOut); len(got) != 2 {
		t.Errorf("a repeated listing must not duplicate devices, got %d", len(got))
	}
}

func TestMatchNodes(t *testing.T) {
	devs := parseVulkanListing(vkOut)
	nodes := []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	ids := map[string]string{"/dev/dri/renderD128": "0x9bc8", "/dev/dri/renderD129": "0x56a5"}
	matchNodes(devs, nodes, ids)
	// Vulkan index order is the reverse of render node order on some systems:
	// the mapping must come from PCI ids, never from position.
	if devs[0].Node != "/dev/dri/renderD129" || devs[1].Node != "/dev/dri/renderD128" {
		t.Errorf("wrong node pairing: %+v", devs)
	}

	twins := []VulkanDevice{{Index: 0, DeviceID: "0x1"}, {Index: 1, DeviceID: "0x1"}}
	matchNodes(twins, []string{"a", "b"}, map[string]string{"a": "0x1", "b": "0x1"})
	if twins[0].Node != "a" || twins[1].Node != "b" {
		t.Errorf("identical GPUs must pair off one node each: %+v", twins)
	}
}

func TestBestVulkan(t *testing.T) {
	rep := &Report{Vulkan: []VulkanDevice{
		{Index: 0, Discrete: true, Node: "/dev/dri/renderD129", OK: true, MS: 900},
		{Index: 1, Discrete: false, Node: "/dev/dri/renderD128", OK: true, MS: 400},
	}}
	if d := BestVulkan(rep, ""); d == nil || d.Index != 0 {
		t.Errorf("with no encode node the discrete GPU should win: %+v", d)
	}
	if d := BestVulkan(rep, "/dev/dri/renderD128"); d == nil || d.Index != 1 {
		t.Errorf("a working Vulkan device on the encode node should win: %+v", d)
	}
	rep.Vulkan[0].OK = false
	if d := BestVulkan(rep, "/dev/dri/renderD129"); d == nil || d.Index != 1 {
		t.Errorf("a failed device must be skipped: %+v", d)
	}
	rep.Vulkan[1].OK = false
	if BestVulkan(rep, "") != nil || BestVulkan(nil, "") != nil {
		t.Error("no working device (or no report) must yield nil")
	}
}

package hwprobe

import (
	"testing"

	"mediatrans/internal/encode"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Intel(R) Arc(tm) A380 Graphics (DG2)":   "Intel Arc A380 Graphics",
		"Intel(R) UHD Graphics 630 (CML GT2)":    "Intel UHD Graphics 630",
		"AMD Radeon RX 6600 (RADV NAVI23)":       "AMD Radeon RX 6600",
		"NVIDIA GeForce RTX 3060":                "NVIDIA GeForce RTX 3060",
		"llvmpipe (LLVM 15.0.6, 256 bits)":       "llvmpipe",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActiveDevice(t *testing.T) {
	rep := &Report{
		RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		Results: []EncoderResult{
			{Backend: encode.QSV, Codec: encode.HEVC, Node: "/dev/dri/renderD128", OK: true, MS: 900},
			{Backend: encode.QSV, Codec: encode.HEVC, Node: "/dev/dri/renderD129", OK: true, MS: 400},
		},
		Vulkan: []VulkanDevice{
			{Index: 0, Name: "Intel(R) Arc(tm) A380 Graphics (DG2)", Node: "/dev/dri/renderD129", OK: true},
			{Index: 1, Name: "Intel(R) UHD Graphics 630 (CML GT2)", Node: "/dev/dri/renderD128", OK: true},
		},
	}
	d := rep.ActiveDevice(encode.QSV, encode.HEVC)
	if d.Node != "/dev/dri/renderD129" || d.Name != "Intel Arc A380 Graphics" {
		t.Fatalf("got %+v", d)
	}
	if n := rep.ActiveDevice(encode.SW, encode.HEVC).Name; n != "CPU (software)" {
		t.Fatalf("sw name %q", n)
	}
	if n := rep.Devices()["/dev/dri/renderD128"]; n != "Intel UHD Graphics 630" {
		t.Fatalf("devices name %q", n)
	}
}

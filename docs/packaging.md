# Packaging and deployment

## Image

`ghcr.io/creaturesurvive/distillarr` (amd64). It contains jellyfin-ffmpeg
(QSV, VA-API, NVENC, x265, SVT-AV1, x264), Intel's media driver (`iHD`),
Mesa's VA-API drivers (AMD `radeonsi`), a static `ffmpeg-vmaf` for quality
measurement, Real-ESRGAN (ncnn/Vulkan) for neural upscaling, and
Tesseract/pgsrip for subtitle OCR.

## Users, groups and permissions

The container starts as root only long enough to:

1. drop to `PUID`:`PGID` (default `1000:1000`);
2. add the groups that own the GPU device nodes you pass in
   (`/dev/dri/*`, `/dev/nvidia*`), so you don't need to know your host's
   `render` group id;
3. `chown -R` `/config` to that user if it belongs to someone else.

`UMASK` (default `022`) applies to files it creates. Replaced media files
always get the original's permission bits back, whatever the umask.

The user needs read and write access to your media folders (it replaces
files in place and keeps originals in a trash folder next to them).

If you'd rather not start as root, set `user: "1000:1000"` in compose. The
entrypoint then runs the app directly, and you add the GPU group
yourself:

```yaml
    user: "1000:1000"
    group_add:
      - "993" # getent group render | cut -d: -f3 (varies between hosts)
```

## GPUs

| Vendor | Compose | Notes |
|---|---|---|
| Intel (iGPU, Arc) | `examples/docker-compose.intel.yml` | `devices: /dev/dri`. `LIBVA_DRIVER_NAME=iHD`. Arc is needed for AV1 encoding. |
| AMD | `examples/docker-compose.amd.yml` | `devices: /dev/dri`, `LIBVA_DRIVER_NAME=radeonsi`. Encoding is VA-API (AMF doesn't exist on Linux). |
| NVIDIA | `examples/docker-compose.nvidia.yml` | NVIDIA Container Toolkit, `runtime: nvidia` (or a `deploy` device reservation), `NVIDIA_DRIVER_CAPABILITIES` including `video` and `graphics`. |
| None | `examples/docker-compose.cpu.yml` | Software encoding; no upscaling. |

Every encoder is tested at startup; only ones that produce a valid file
are used (Settings → System → Hardware shows the results).

**Upscaling needs Vulkan.** Shader and neural upscales run on a Vulkan
GPU: Mesa's drivers for Intel and AMD (included in the image), or the
NVIDIA driver's Vulkan ICD (exposed by the `graphics` capability). The
hardware probe checks that the GPU produces correct colour, not just that
it runs; if none passes, upscaling is turned off.

## Paths

- `/config`: database, A/B preview clips, image caches, OCR language
  data, chunks of neural upscales in progress. Keep it on a local disk.
- Media: mount it at the same path Sonarr/Radarr/Jellyfin use if you can
  (e.g. `/data`); otherwise set a path mapping per integration.
- Trash: by default `.distillarr-trash` at the root of each library's
  filesystem, so keeping an original is an instant hardlink. On mergerfs
  or Unraid user shares, keep every library inside one pool/share, and
  mount the share rather than the individual disks.

## Unraid

`examples/unraid/distillarr.xml` is a Community Applications template
(PUID 99 / PGID 100, `/mnt/user/appdata/distillarr` for config). Add it
under Docker → Template repositories, or copy it to
`/boot/config/plugins/dockerMan/templates-user/`.

# ============================================================
# mediiatrans — media analysis & transcoding platform
# ============================================================
# Stage 1: React/Vite frontend  → static dist
# Stage 2: Go backend           → static binary with embedded UI
# Stage 3: ubuntu + jellyfin-ffmpeg7 + Intel GPU stack
#
# The ffmpeg/GPU runtime follows a proven pattern:
# Jellyfin's shared ffmpeg build (linked
# against system libva/libvpl, so QSV works via dlopen) plus
# Intel's repo for the non-free iHD driver.
#
# Hardware encoders: Intel QSV, VA-API, (NVENC when GPU present),
# software libx265 / libsvtav1. AMD is handled through VA-API on
# Linux (AMF encoders do not exist in Linux ffmpeg builds).
# ============================================================

# ---- Stage 1: frontend ----
FROM node:24-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Stage 1b: quality metrics ----
# jellyfin-ffmpeg has no libvmaf; a static build (with VMAF models built
# in) is used only for measuring quality, as /usr/local/bin/ffmpeg-vmaf.
FROM debian:bookworm-slim AS vmaf
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/ff.tar.xz \
      https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n8.1-latest-linux64-gpl-8.1.tar.xz \
    && tar -xf /tmp/ff.tar.xz -C /tmp --wildcards '*/bin/ffmpeg' \
    && mv /tmp/ffmpeg-*/bin/ffmpeg /ffmpeg-vmaf \
    && /ffmpeg-vmaf -hide_banner -filters | grep -q libvmaf

# ---- Stage 1c: neural upscaler ----
# Real-ESRGAN (ncnn/Vulkan) for the neural upscale tier. Pinned release with a
# checksum; only the binary and its models are kept. Measured on the Arc A380:
# ~2.4 fps for the light anime model at 480p input (roughly 0.1x realtime), so
# it is an overnight, per-episode tool, not a per-film one.
FROM debian:bookworm-slim AS ncnn
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/rg.zip \
      https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-ubuntu.zip \
    && echo "e5aa6eb131234b87c0c51f82b89390f5e3e642b7b70f2b9bbe95b6a285a40c96  /tmp/rg.zip" | sha256sum -c - \
    && mkdir -p /opt/realesrgan \
    && unzip -q /tmp/rg.zip realesrgan-ncnn-vulkan 'models/*' -d /opt/realesrgan \
    && chmod +x /opt/realesrgan/realesrgan-ncnn-vulkan

# ---- Stage 2: backend ----
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
COPY internal/ ./internal/
COPY --from=web /web/dist ./web/dist/
RUN CGO_ENABLED=0 GOFLAGS=-trimpath \
    go build -ldflags="-s -w" -o /out/mediatrans .

# ---- Stage 3: runtime ----
FROM ubuntu:24.04

ARG JELLYFIN_FFMPEG_VERSION=7
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        gpg \
        curl \
        libgomp1 \
        procps \
        tini \
    && rm -rf /var/lib/apt/lists/*

# ---- Jellyfin FFmpeg (shared build with QSV/VA-API/NVENC) ----
RUN set -e && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
        | gpg --dearmor -o /usr/share/keyrings/jellyfin-archive-keyring.gpg && \
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/jellyfin-archive-keyring.gpg] \
        https://repo.jellyfin.org/ubuntu noble main" \
        > /etc/apt/sources.list.d/jellyfin.list && \
    apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        jellyfin-ffmpeg${JELLYFIN_FFMPEG_VERSION} && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffmpeg /usr/local/bin/ffmpeg && \
    ln -sf /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe && \
    rm -rf /var/lib/apt/lists/*

# ---- Intel GPU / QSV runtime (iHD driver + oneVPL) ----
RUN set -e && \
    curl -fsSL https://repositories.intel.com/gpu/intel-graphics.key \
        | gpg --dearmor -o /usr/share/keyrings/intel-graphics.gpg && \
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/intel-graphics.gpg] \
        https://repositories.intel.com/gpu/ubuntu jammy client" \
        > /etc/apt/sources.list.d/intel-gpu.list && \
    apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        intel-media-va-driver-non-free \
        libigdgmm12 \
        libva2 \
        libva-drm2 \
        libdrm2 \
        vainfo \
    && rm -rf /var/lib/apt/lists/*

RUN echo "=== ffmpeg ===" && ffmpeg -version | head -1 && \
    echo "=== hw encoders ===" && \
    ffmpeg -encoders 2>/dev/null | grep -oE '[a-z0-9_]+_(qsv|nvenc|vaapi)' | sort -u || true

ENV LIBVA_DRIVERS_PATH="/usr/lib/x86_64-linux-gnu/dri" \
    LIBVA_DRIVER_NAME="iHD" \
    MEDIIATRANS_LISTEN=":8080" \
    MEDIIATRANS_DB="/config/mediatrans.db"

COPY --from=build /out/mediatrans /usr/local/bin/mediatrans
COPY --from=vmaf /ffmpeg-vmaf /usr/local/bin/ffmpeg-vmaf
COPY --from=ncnn /opt/realesrgan /opt/realesrgan

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/api/v1/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/mediatrans"]

import { type ReactNode } from "react";
import { type AudioTrack, type FileItem, type HwReport, type Settings, type Stream } from "./api";
import { Seg, Toggle } from "./components";
import { backendLabel, channelsLabel, codecLabel, resClass, resLabel } from "./format";

// Backends that passed a real test encode for a codec.
export function workingBackends(rep: HwReport | null | undefined, codec: string): string[] {
  if (!rep) return ["sw"];
  const out: string[] = [];
  for (const b of ["qsv", "vaapi", "nvenc", "sw"]) {
    if (rep.results.some((r) => r.backend === b && r.codec === codec && r.ok)) out.push(b);
  }
  return out.length ? out : ["sw"];
}

export function hasBars(f: FileItem): boolean {
  return f.crop_w > 0 && f.crop_h > 0 && (f.crop_w < f.width || f.crop_h < f.height);
}

export function cropRect(f: FileItem): string {
  return `${f.crop_w}:${f.crop_h}:${f.crop_x}:${f.crop_y}`;
}

function barsHint(f: FileItem): string {
  if (!hasBars(f)) return f.crop_checked ? "The picture fills the frame" : "Detection runs in the background after a scan";
  return `Picture is ${f.crop_w}×${f.crop_h} inside the ${f.width}×${f.height} frame. Cropping saves a little and changes the frame shape; check films that switch aspect ratio (IMAX scenes)`;
}

export function qualityWord(q: number): string {
  if (q >= 78) return "near-lossless";
  if (q >= 68) return "high";
  if (q >= 55) return "balanced";
  if (q >= 45) return "compact";
  return "very small";
}

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="opt-row">
      <div className="opt-label">
        {label}
        {hint && <div className="opt-hint">{hint}</div>}
      </div>
      <div className="opt-ctl">{children}</div>
    </div>
  );
}

type Props = {
  value: Settings;
  onChange: (s: Settings) => void;
  file: FileItem;
  streams: Stream[];
  hw: HwReport | null;
  autoBackend?: string;
};

export default function EncodeOptions({ value: s, onChange, file, streams, hw, autoBackend }: Props) {
  const set = (patch: Partial<Settings>) => onChange({ ...s, ...patch });
  const backends = workingBackends(hw, s.codec);
  const av1ok = workingBackends(hw, "av1").length > 0;
  const effBackend = !s.backend || s.backend === "auto" ? autoBackend || backends[0] : s.backend;
  const swAv1 = effBackend === "sw" && s.codec === "av1";
  const swHevc = effBackend === "sw" && s.codec === "hevc";
  const audioStreams = streams.filter((x) => x.kind === "audio");
  const subStreams = streams.filter((x) => x.kind === "subtitle");

  const trackOf = (idx: number, codec: string): AudioTrack => {
    const t = s.audio?.find((a) => a.index === idx);
    if (t) return t;
    const pcm = codec.startsWith("pcm_") || codec === "lpcm";
    return pcm && s.audio_pcm_target !== "copy"
      ? { index: idx, action: "convert", codec: s.audio_pcm_target || "flac" }
      : { index: idx, action: "copy" };
  };
  const setTrack = (t: AudioTrack) => {
    const rest = (s.audio || []).filter((a) => a.index !== t.index);
    set({ audio: [...rest, t] });
  };
  const subKept = (idx: number) => !(s.subs || []).some((x) => x.index === idx && x.action === "drop");
  const setSub = (idx: number, keep: boolean) => {
    const rest = (s.subs || []).filter((x) => x.index !== idx);
    set({ subs: keep ? rest : [...rest, { index: idx, action: "drop" }] });
  };

  return (
    <div className="opts">
      <section className="opt-section">
        <h3 className="opt-h">Video</h3>
        <Row label="Codec">
          <Seg
            value={s.codec}
            onChange={(codec) => set({ codec, backend: "auto", film_grain: 0, tune: "" })}
            options={[
              { value: "hevc", label: "HEVC" },
              { value: "av1", label: av1ok ? "AV1" : "AV1 (no encoder)" },
            ]}
          />
        </Row>
        <Row label="Encoder" hint={effBackend === "sw" ? "CPU: slowest, best compression per bit" : "GPU: fast, slightly larger files"}>
          <Seg
            value={s.backend || "auto"}
            onChange={(backend) => set({ backend })}
            options={[
              { value: "auto", label: `Auto (${backendLabel(autoBackend || backends[0])})` },
              ...backends.map((b) => ({ value: b, label: backendLabel(b) })),
            ]}
          />
        </Row>
        <Row label="Quality" hint="Higher keeps more detail and makes a bigger file">
          <div className="quality">
            <input
              type="range" min={30} max={90} step={1} value={s.quality}
              onChange={(e) => set({ quality: Number(e.target.value) })}
              aria-label="Quality"
            />
            <span className="mono quality-val">
              {s.quality} <span className="dim">{qualityWord(s.quality)}</span>
            </span>
          </div>
        </Row>
        <Row label="Speed" hint="Slower presets squeeze a few % more out at the same quality">
          <Seg
            value={s.speed || "medium"}
            onChange={(speed) => set({ speed })}
            options={["faster", "fast", "medium", "slow", "slower"].map((v) => ({ value: v, label: v }))}
          />
        </Row>
        <Row label="Bit depth" hint="10-bit compresses better and avoids banding">
          <Seg
            value={s.bit_depth === 8 ? 8 : 10}
            onChange={(bit_depth) => set({ bit_depth })}
            options={[{ value: 10, label: "10-bit" }, { value: 8, label: "8-bit" }]}
          />
        </Row>
        <Row label="Resolution">
          <Seg
            value={s.max_height || 0}
            onChange={(max_height) => set({ max_height })}
            options={[
              { value: 0, label: `Keep ${resLabel(file.width, file.height)} (${file.width}×${file.height})` },
              ...(resClass(file.width, file.height) > 1080 ? [{ value: 1080, label: "1080p" }] : []),
              ...(resClass(file.width, file.height) > 720 ? [{ value: 720, label: "720p" }] : []),
            ]}
          />
        </Row>
        <Row label="Black bars" hint={barsHint(file)}>
          {hasBars(file) ? (
            <Seg
              value={s.crop ? "crop" : "keep"}
              onChange={(v) => set({ crop: v === "crop" ? cropRect(file) : "" })}
              options={[
                { value: "keep", label: "Keep full frame" },
                { value: "crop", label: `Crop to ${file.crop_w}×${file.crop_h}` },
              ]}
            />
          ) : (
            <span className="dim small">{file.crop_checked ? "None found" : "Not checked yet"}</span>
          )}
        </Row>
        <Row label="Deinterlace" hint={file.interlaced ? "Source is interlaced" : "Source looks progressive"}>
          <Seg
            value={s.deinterlace || "auto"}
            onChange={(deinterlace) => set({ deinterlace })}
            options={[{ value: "auto", label: "Auto" }, { value: "on", label: "On" }, { value: "off", label: "Off" }]}
          />
        </Row>
        {file.hdr && file.hdr !== "dolby_vision" && (
          <Row label="HDR" hint="Tone-mapping runs on the CPU and makes an SDR file">
            <Seg
              value={s.tonemap_hdr ? "sdr" : "keep"}
              onChange={(v) => set({ tonemap_hdr: v === "sdr" })}
              options={[{ value: "keep", label: "Keep HDR" }, { value: "sdr", label: "Tone-map to SDR" }]}
            />
          </Row>
        )}
        {swAv1 && (
          <Row label="Film grain" hint="Synthesises grain at playback instead of storing it">
            <div className="quality">
              <input type="range" min={0} max={30} value={s.film_grain || 0}
                onChange={(e) => set({ film_grain: Number(e.target.value) })} aria-label="Film grain" />
              <span className="mono quality-val">{s.film_grain ? s.film_grain : "off"}</span>
            </div>
          </Row>
        )}
        {swHevc && (
          <Row label="Tune">
            <Seg
              value={s.tune || ""}
              onChange={(tune) => set({ tune })}
              options={[{ value: "", label: "None" }, { value: "animation", label: "Animation" }, { value: "grain", label: "Grain" }]}
            />
          </Row>
        )}
      </section>

      <section className="opt-section">
        <h3 className="opt-h">Audio</h3>
        {audioStreams.length === 0 && <div className="dim">No audio tracks.</div>}
        {audioStreams.map((a) => {
          const t = trackOf(a.stream_index, a.codec);
          const lossy = t.action === "convert" && t.codec !== "flac";
          return (
            <div key={a.stream_index} className="track">
              <div className="track-name">
                <span className="mono dim">#{a.stream_index}</span> {(a.lang || "und").toUpperCase()} {codecLabel(a.codec)}{" "}
                {channelsLabel(a.channels)}
                {a.title && <span className="dim"> · {a.title}</span>}
              </div>
              <div className="track-ctl">
                <Seg
                  value={t.action}
                  onChange={(action) => setTrack({ ...t, action, codec: action === "convert" ? t.codec || "aac" : undefined })}
                  options={[{ value: "copy", label: "Keep" }, { value: "convert", label: "Convert" }, { value: "drop", label: "Remove" }]}
                />
                {t.action === "convert" && (
                  <>
                    <select className="input input-sm" value={t.codec || "aac"} onChange={(e) => setTrack({ ...t, codec: e.target.value })}>
                      <option value="flac">FLAC (lossless)</option>
                      <option value="opus">Opus</option>
                      <option value="aac">AAC</option>
                      <option value="eac3">E-AC-3</option>
                    </select>
                    {lossy && (
                      <select className="input input-sm" value={t.bitrate || 0} onChange={(e) => setTrack({ ...t, bitrate: Number(e.target.value) || undefined })}>
                        <option value={0}>auto bitrate</option>
                        {[128, 192, 256, 384, 448, 640].map((k) => <option key={k} value={k}>{k} kb/s</option>)}
                      </select>
                    )}
                    {a.channels > 2 && (
                      <Toggle on={t.channels === 2} onChange={(v) => setTrack({ ...t, channels: v ? 2 : undefined })} label="Stereo" />
                    )}
                  </>
                )}
              </div>
            </div>
          );
        })}
      </section>

      <section className="opt-section">
        <h3 className="opt-h">Subtitles</h3>
        {subStreams.length === 0 && file.sidecars.length === 0 && <div className="dim">No subtitles.</div>}
        {subStreams.map((x) => (
          <div key={x.stream_index} className="track">
            <div className="track-name">
              <span className="mono dim">#{x.stream_index}</span> {(x.lang || "und").toUpperCase()} {x.codec}
              {x.forced && <span className="chip">forced</span>}
              {!x.is_text && <span className="dim"> · image</span>}
            </div>
            <div className="track-ctl">
              <Seg value={subKept(x.stream_index) ? "keep" : "drop"} onChange={(v) => setSub(x.stream_index, v === "keep")}
                options={[{ value: "keep", label: "Keep" }, { value: "drop", label: "Remove" }]} />
            </div>
          </div>
        ))}
        {file.sidecars.length > 0 && (
          <div className="dim small" style={{ marginTop: 8 }}>
            External files ({file.sidecars.map((x) => `${x.lang || "?"}.${x.kind}`).join(", ")}) are never touched.
          </div>
        )}
      </section>

      <section className="opt-section">
        <h3 className="opt-h">Output</h3>
        <Row label="Container" hint="MP4 output gets the hvc1 tag and its index at the start (Apple-compatible). Auto uses MP4 whenever every kept track fits">
          <Seg value={s.container || "auto"} onChange={(container) => set({ container })}
            options={[{ value: "auto", label: "Auto" }, { value: "mkv", label: "MKV" }, { value: "mp4", label: "MP4" }]} />
        </Row>
        <Row label="Extra ffmpeg options" hint="Added before the output file, e.g. -g 240">
          <input className="input mono" value={s.extra_args || ""} placeholder="none"
            onChange={(e) => set({ extra_args: e.target.value })} spellCheck={false} />
        </Row>
      </section>
    </div>
  );
}

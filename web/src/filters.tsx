// SPDX-License-Identifier: GPL-3.0-or-later

// Facet filters (container, video/audio codec, resolution, issue) shared
// by the library browser and the issues list. Options and counts come
// from the library itself, so only values that exist are offered.
import { useEffect, useState } from "react";
import { api, type Composition, type Facet, type IssueType } from "./api";
import { codecLabel, resLabel } from "./format";

export type Facets = { container: string; video: string; audio: string; res: string; issue: string; upscale?: string; hardlinked?: string; managed?: string };
export const NO_FACETS: Facets = { container: "", video: "", audio: "", res: "", issue: "", upscale: "", hardlinked: "", managed: "" };

const LEGACY_CONTAINERS = new Set(["avi", "wmv", "asf", "flv", "mpg", "mpeg", "ts", "m2ts", "vob", "divx", "ogm", "rm", "rmvb", "3gp"]);
const LEGACY_CODECS = new Set(["mpeg1video", "mpeg2video", "mpeg4", "msmpeg4v1", "msmpeg4v2", "msmpeg4v3", "wmv1", "wmv2", "wmv3", "vc1", "h263", "vp8", "theora", "flv1"]);

const AUDIO_LABEL: Record<string, string> = {
  aac: "AAC", ac3: "AC-3 (Dolby Digital)", eac3: "E-AC-3 (DD+)", dts: "DTS", truehd: "TrueHD",
  flac: "FLAC", alac: "ALAC", opus: "Opus", mp3: "MP3", mp2: "MP2", pcm: "PCM (uncompressed)", vorbis: "Vorbis",
};
const CONTAINER_LABEL: Record<string, string> = { mkv: "MKV", mp4: "MP4 / M4V / MOV", avi: "AVI", ts: "MPEG-TS", m2ts: "M2TS", wmv: "WMV" };

// Merge mp4/m4v/mov into one option (the server filters them together).
function containers(list: Facet[]): Facet[] {
  const out: Facet[] = [];
  let mp4: Facet | null = null;
  for (const f of list) {
    if (["mp4", "m4v", "mov"].includes(f.value)) {
      mp4 = mp4 ? { value: "mp4", files: mp4.files + f.files, bytes: mp4.bytes + f.bytes } : { ...f, value: "mp4" };
    } else out.push(f);
  }
  if (mp4) out.push(mp4);
  return out.sort((a, b) => b.files - a.files);
}

function withLegacy(list: Facet[], legacy: Set<string>): Facet[] {
  const l = list.filter((f) => legacy.has(f.value));
  if (l.length === 0) return list;
  const sum = l.reduce((a, f) => ({ value: "legacy", files: a.files + f.files, bytes: a.bytes + f.bytes }), { value: "legacy", files: 0, bytes: 0 });
  return [...list, sum];
}

export function useComposition(library?: string) {
  const [c, setC] = useState<Composition | null>(null);
  useEffect(() => {
    api.composition(library).then(setC).catch(() => {});
  }, [library]);
  return c;
}

export function FacetFilters({
  library, value, onChange, issues = true,
}: {
  library?: string;
  value: Facets;
  onChange: (v: Facets) => void;
  issues?: boolean;
}) {
  const comp = useComposition(library);
  const [types, setTypes] = useState<IssueType[]>([]);
  useEffect(() => {
    if (issues) api.issues().then((r) => setTypes(r.types)).catch(() => {});
  }, [issues]);

  const set = (k: keyof Facets, v: string) => onChange({ ...value, [k]: v });
  const count = (n: number) => ` · ${n.toLocaleString()}`;
  const libCount = (t: IssueType) => (library === "movies" ? t.movies : library === "tvshows" ? t.episodes : t.movies + t.episodes);

  // Saved sessions predate some facets: a missing key reads as "any".
  const sel = (k: keyof Facets, label: string, opts: { value: string; label: string }[]) => (
    <select className={`input facet${value[k] ? " facet-on" : ""}`} value={value[k] ?? ""} aria-label={label}
      onChange={(e) => set(k, e.target.value)}>
      <option value="">{label}: any</option>
      {opts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );

  const active = Object.values(value).some(Boolean);

  return (
    <div className="facets">
      {sel("container", "Container", comp ? withLegacy(containers(comp.containers), LEGACY_CONTAINERS).map((f) => ({
        value: f.value, label: (f.value === "legacy" ? "Legacy (AVI, TS, WMV…)" : CONTAINER_LABEL[f.value] || f.value.toUpperCase()) + count(f.files),
      })) : [])}
      {sel("video", "Video", comp ? withLegacy(comp.video, LEGACY_CODECS).map((f) => ({
        value: f.value, label: (f.value === "legacy" ? "Legacy (MPEG-2, Xvid, VC-1…)" : codecLabel(f.value)) + count(f.files),
      })) : [])}
      {sel("audio", "Audio", comp ? comp.audio.map((f) => ({
        value: f.value, label: (AUDIO_LABEL[f.value] || f.value.toUpperCase()) + count(f.files),
      })) : [])}
      {sel("res", "Resolution", comp ? comp.res.filter((f) => f.value !== "0").map((f) => ({
        value: f.value, label: resLabel(Number(f.value)) + count(f.files),
      })) : [])}
      {sel("upscale", "Upscaling", [
        { value: "upscalable", label: "Could be upscaled (below 4K)" },
        { value: "upscaled", label: "Already upscaled" },
      ])}
      {sel("hardlinked", "Hardlinked", [
        { value: "yes", label: "Shares data with another file" },
      ])}
      {sel("managed", "Managed by", [
        { value: "yes", label: "Sonarr/Radarr" },
        { value: "no", label: "Not managed" },
      ])}
      {issues && sel("issue", "Issue", types.filter((t) => t.scope === "file" && libCount(t) > 0).map((t) => ({
        value: t.key, label: t.label + count(libCount(t)),
      })))}
      {active && <button className="linkish" onClick={() => onChange(NO_FACETS)}>Clear filters</button>}
    </div>
  );
}

// Query params for api.files.
export function facetParams(f: Facets) {
  return { container: f.container, codec: f.video, audio: f.audio, res: f.res, issue: f.issue, upscale: f.upscale ?? "", hardlinked: f.hardlinked ?? "", managed: f.managed ?? "" };
}

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type FileItem, type Series } from "../api";
import { FileCard } from "../components";
import { bytes, bitrate } from "../format";
import type { LiveState } from "../App";
import "../components.css";

type Mode = "movies" | "shows";
type ShowMode = "series" | "episodes";

export default function Library({ live }: { live: LiveState }) {
  const [mode, setMode] = useState<Mode>("movies");
  const [showMode, setShowMode] = useState<ShowMode>("series");
  const [search, setSearch] = useState("");
  const [codec, setCodec] = useState("");
  const [candidatesOnly, setCandidatesOnly] = useState(true);
  const [minSize, setMinSize] = useState("");

  const [files, setFiles] = useState<FileItem[]>([]);
  const [series, setSeries] = useState<Series[]>([]);
  const [cursor, setCursor] = useState<string>("");
  const [more, setMore] = useState(false);
  const [stats, setStats] = useState<{ files: number; total_size: number; projected_saved: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const scanTick = live.scan?.running ? 1 : 0;

  const loadFirst = useCallback(async () => {
    setLoading(true);
    try {
      if (mode === "movies") {
        const res = await api.files({
          library: "movies",
          title: search || undefined,
          codec: codec || undefined,
          candidates: candidatesOnly ? 1 : undefined,
          min_size: minSize ? String(parseFloat(minSize) * 1e9) : undefined,
          limit: 60,
        });
        setFiles(res.files);
        setCursor(res.next_cursor);
        setMore(!!res.next_cursor);
      } else if (showMode === "series") {
        const res = await api.series(search || undefined);
        setSeries(res.series);
      } else {
        const res = await api.files({
          library: "tvshows",
          codec: codec || undefined,
          candidates: candidatesOnly ? 1 : undefined,
          limit: 60,
        });
        setFiles(res.files);
        setCursor(res.next_cursor);
        setMore(!!res.next_cursor);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [mode, showMode, search, codec, candidatesOnly, minSize]);

  useEffect(() => {
    const t = setTimeout(loadFirst, search ? 300 : 0);
    return () => clearTimeout(t);
  }, [loadFirst, scanTick]);

  useEffect(() => {
    api.libraryStats(mode === "movies" ? "movies" : "tvshows").then(setStats).catch(() => {});
  }, [mode, live.queueVersion]);

  const loadMore = async () => {
    if (!cursor) return;
    const [idStr, titleStr] = cursor.split(/:(.*)/s);
    const res = await api.files({
      library: mode === "movies" ? "movies" : "tvshows",
      codec: codec || undefined,
      candidates: candidatesOnly ? 1 : undefined,
      cursor: idStr,
      cursor_title: titleStr || "",
      limit: 60,
    });
    setFiles((f) => [...f, ...res.files]);
    setCursor(res.next_cursor);
    setMore(!!res.next_cursor);
  };

  return (
    <div ref={listRef}>
      <div className="page-head">
        <div>
          <h1 className="page-title">Library</h1>
          <div className="page-sub">
            {stats
              ? `${stats.files.toLocaleString()} files · ${bytes(stats.total_size)}${
                  stats.projected_saved > 0 ? ` · ${bytes(stats.projected_saved)} reclaimable` : ""
                }`
              : "—"}
          </div>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={() => api.scanStart().catch(() => {})}>
            {live.scan?.running ? "scanning…" : "Rescan"}
          </button>
        </div>
      </div>

      <div className="toolbar" style={{ marginBottom: 18 }}>
        <div className="tabs">
          <button className={`tab${mode === "movies" ? " active" : ""}`} onClick={() => setMode("movies")}>
            Movies
          </button>
          <button className={`tab${mode === "shows" ? " active" : ""}`} onClick={() => setMode("shows")}>
            Shows
          </button>
        </div>
        {mode === "shows" && (
          <div className="tabs">
            <button className={`tab${showMode === "series" ? " active" : ""}`} onClick={() => setShowMode("series")}>
              By show
            </button>
            <button className={`tab${showMode === "episodes" ? " active" : ""}`} onClick={() => setShowMode("episodes")}>
              All episodes
            </button>
          </div>
        )}
        <label className="filter">
          <span aria-hidden>⌕</span>
          <input
            placeholder={mode === "movies" ? "search movies" : "search shows"}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        {mode === "movies" || showMode === "episodes" ? (
          <>
            <label className="filter">
              codec
              <select value={codec} onChange={(e) => setCodec(e.target.value)}>
                <option value="">any</option>
                <option value="h264">H.264</option>
                <option value="hevc">HEVC</option>
                <option value="mpeg2video">MPEG-2</option>
                <option value="mpeg4">MPEG-4</option>
                <option value="vc1">VC-1</option>
              </select>
            </label>
            <label className="filter">
              <input type="checkbox" checked={candidatesOnly} onChange={(e) => setCandidatesOnly(e.target.checked)} />
              worth re-encoding
            </label>
            <label className="filter">
              ≥
              <input
                style={{ width: 46 }}
                placeholder="GB"
                value={minSize}
                onChange={(e) => setMinSize(e.target.value)}
              />
              GB
            </label>
          </>
        ) : null}
      </div>

      {loading && files.length === 0 && series.length === 0 ? (
        <div className="empty">
          <div className="big">Reading tape…</div>
          {live.scan?.running && (
            <div className="mono dim">
              {live.scan.phase} · {live.scan.probed} probed
            </div>
          )}
        </div>
      ) : mode === "shows" && showMode === "series" ? (
        <SeriesList series={series} />
      ) : (
        <>
          <div className="grid">
            {files.map((f) => (
              <FileCard key={f.id} item={f} />
            ))}
          </div>
          {files.length === 0 && (
            <div className="empty">
              <div className="big">Nothing matches</div>
              <div>
                {candidatesOnly
                  ? "No candidates with these filters. Widen the filters or run a rescan."
                  : "Try clearing filters or run a rescan."}
              </div>
            </div>
          )}
          {more && (
            <div style={{ textAlign: "center", marginTop: 22 }}>
              <button className="btn" onClick={loadMore}>
                Load more
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function SeriesList({ series }: { series: Series[] }) {
  if (series.length === 0) {
    return (
      <div className="empty">
        <div className="big">No shows found</div>
        <div>Run a rescan to index the TV library.</div>
      </div>
    );
  }
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Show</th>
          <th>Episodes</th>
          <th>Size</th>
          <th>Median bitrate</th>
          <th>Codecs</th>
        </tr>
      </thead>
      <tbody>
        {series.map((s) => (
          <tr key={s.title}>
            <td>
              <a href={`#/show/${encodeURIComponent(s.title)}`} style={{ fontWeight: 600 }}>
                {s.title}
                {s.year > 0 && <span className="dim"> ({s.year})</span>}
              </a>
            </td>
            <td className="mono dim">{s.episodes}</td>
            <td className="mono dim">{bytes(s.total_size)}</td>
            <td className="mono dim">{bitrate(s.avg_bitrate)}</td>
            <td>
              <span className="chip">{s.codecs}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Issues: what's wrong across the library, grouped by type, with quick
// fixes (remux, video untouched) and bulk re-encode queuing.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, type FileItem, type IssueType, type MixedSeason } from "../api";
import { CodecChip, Empty, EpisodeLabel, IssueChips, Seg, hasQuickFix, toast } from "../components";
import { FacetFilters, NO_FACETS, facetParams, type Facets } from "../filters";
import { bytes, codecLabel, resLabel, seasonName } from "../format";
import { useSessionState } from "./Library";
import type { LiveState } from "../App";

const PAGE = 50;
const FIX_LABEL = { quick: "Quick fix", reencode: "Re-encode", info: "Info" } as const;

export default function Issues({ live }: { live: LiveState }) {
  const [types, setTypes] = useState<IssueType[]>([]);
  const [sel, setSel] = useSessionState("is.type", "");
  const [lib, setLib] = useSessionState("is.lib", "");
  const [facets, setFacets] = useSessionState<Facets>("is.facets", NO_FACETS);
  const [search, setSearch] = useSessionState("is.search", "");
  const [files, setFiles] = useState<FileItem[]>([]);
  const [total, setTotal] = useState(0);
  const [mixed, setMixed] = useState<MixedSeason[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const sentinel = useRef<HTMLDivElement>(null);
  const reqId = useRef(0);

  const loadTypes = () => api.issues().then((r) => setTypes(r.types)).catch(() => {});
  useEffect(() => { loadTypes(); }, [live.queueVersion, live.scan?.running]);

  const count = (t: IssueType) => (lib === "movies" ? t.movies : lib === "tvshows" ? t.episodes : t.movies + t.episodes);
  const current = types.find((t) => t.key === sel);

  // Default to the first type that has anything, quick fixes first.
  useEffect(() => {
    if (types.length === 0 || (current && count(current) > 0)) return;
    const order = ["quick", "reencode", "info"];
    const first = [...types].sort((a, b) => order.indexOf(a.fix) - order.indexOf(b.fix)).find((t) => count(t) > 0);
    if (first && first.key !== sel) setSel(first.key);
  }, [types, lib]);

  const params = useCallback((offset: number) => ({
    library: lib, title: search, ...facetParams(facets), issue: sel, sort: "size", offset, limit: PAGE,
  }), [lib, search, facets, sel]);

  useEffect(() => {
    if (!sel) return;
    const id = ++reqId.current;
    setLoading(true);
    if (sel === "mixed_season") {
      api.mixedSeasons(search).then((r) => id === reqId.current && setMixed(r.seasons)).finally(() => id === reqId.current && setLoading(false));
      return;
    }
    const t = setTimeout(() => {
      api.files(params(0)).then((r) => {
        if (id !== reqId.current) return;
        setFiles(r.files);
        setTotal(r.total);
      }).finally(() => id === reqId.current && setLoading(false));
    }, search ? 250 : 0);
    return () => clearTimeout(t);
  }, [params, live.queueVersion]);

  useEffect(() => {
    const el = sentinel.current;
    if (!el || sel === "mixed_season") return;
    const io = new IntersectionObserver((e) => {
      if (!e[0].isIntersecting || loading || files.length >= total) return;
      setLoading(true);
      const id = reqId.current;
      api.files(params(files.length)).then((r) => id === reqId.current && setFiles((f) => [...f, ...r.files])).finally(() => setLoading(false));
    }, { rootMargin: "600px" });
    io.observe(el);
    return () => io.disconnect();
  }, [files.length, total, loading, params, sel]);

  const fixOne = async (f: FileItem, runNow: boolean) => {
    try {
      await api.fixFile(f.id, runNow);
      toast(runNow ? "Quick fix starting" : "Quick fix queued");
      setFiles((l) => l.map((x) => (x.id === f.id ? { ...x, queued: true } : x)));
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const fixAll = async (t: IssueType) => {
    const n = count(t);
    const what = t.fix === "quick"
      ? `Queue a quick fix (remux, video untouched) for ${n.toLocaleString()} file${n === 1 ? "" : "s"}?`
      : `Queue a full re-encode for ${n.toLocaleString()} file${n === 1 ? "" : "s"} with their recommended settings? They run inside your processing windows.`;
    if (!confirm(what + (n > 2000 ? "\n\nOnly the 2,000 largest are queued per click." : ""))) return;
    setBusy(true);
    try {
      const r = await api.fixIssue(t.key, { library: lib || undefined });
      toast(`${r.queued} queued${r.skipped ? `, ${r.skipped} skipped (already queued)` : ""}`);
      loadTypes();
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setBusy(false);
    }
  };

  const visible = types.filter((t) => t.scope === "file" || lib !== "movies");
  const totalFiles = types.filter((t) => t.fix === "quick").reduce((a, t) => a + count(t), 0);

  return (
    <div>
      <header className="page-head">
        <div>
          <h1 className="page-title">Issues</h1>
          <div className="page-sub">
            {types.length ? <>{totalFiles.toLocaleString()} quick-fixable problem{totalFiles === 1 ? "" : "s"} · compatibility, legacy formats and wasted space</> : "—"}
          </div>
        </div>
        <Seg value={lib} onChange={setLib} label="Library" options={[
          { value: "", label: "All" }, { value: "movies", label: "Movies" }, { value: "tvshows", label: "TV" },
        ]} />
      </header>

      <div className="issue-cards">
        {visible.map((t) => {
          const n = count(t);
          return (
            <button key={t.key} className={`issue-card fx-${t.fix}${sel === t.key ? " on" : ""}${n === 0 ? " empty" : ""}`}
              onClick={() => setSel(t.key)} aria-pressed={sel === t.key}>
              <div className="ic-top">
                <span className={`chip issue-${t.fix}`}>{FIX_LABEL[t.fix]}</span>
                <span className="ic-count mono">{n.toLocaleString()}</span>
              </div>
              <div className="ic-label">{t.label}</div>
              <div className="ic-sub mono faint">
                {t.scope === "season" ? `${n} season${n === 1 ? "" : "s"}` : lib === "" ? `${t.movies} movies · ${t.episodes} episodes` : "files"}
                {t.bytes > 0 && ` · ${bytes(t.bytes)}`}
              </div>
            </button>
          );
        })}
      </div>

      {current && (
        <section className="panel issue-detail">
          <div className="issue-detail-head">
            <div>
              <h2 className="panel-title">{current.label}</h2>
              <p className="dim small">{current.help}</p>
            </div>
            {current.scope === "file" && current.fix !== "info" && count(current) > 0 && (
              <button className={`btn${current.fix === "quick" ? " btn-primary" : ""}`} disabled={busy} onClick={() => fixAll(current)}>
                {current.fix === "quick" ? `Fix all ${count(current).toLocaleString()}` : `Queue all ${count(current).toLocaleString()}`}
              </button>
            )}
          </div>

          <div className="filters">
            <input className="input search" type="search" placeholder={sel === "mixed_season" ? "Search shows" : "Search titles"}
              value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Search" />
          </div>
          {sel !== "mixed_season" && <FacetFilters library={lib || undefined} value={facets} onChange={setFacets} issues={false} />}

          {sel === "mixed_season" ? (
            mixed.length === 0 && !loading ? <Empty title="No mixed seasons">Every season uses one format.</Empty> : (
              <ul className="issue-rows">
                {mixed.map((m) => (
                  <li key={`${m.show}-${m.season}`} className="issue-row">
                    <a className="ir-main" href={`#/show/${encodeURIComponent(m.show)}?season=${m.season}`}>
                      <div className="ir-title">{m.show} <span className="dim">· {seasonName(m.season)}</span></div>
                      <div className="chips">
                        {m.codecs.split(",").length > 1 && <span className="chip issue-reencode">{m.codecs.split(",").map(codecLabel).join(" + ")}</span>}
                        {m.containers.split(",").length > 1 && <span className="chip issue-quick">{m.containers.toUpperCase().replace(/,/g, " + ")}</span>}
                        {m.classes.split(",").length > 1 && <span className="chip">{m.classes.split(",").map((c) => resLabel(Number(c))).join(" + ")}</span>}
                      </div>
                    </a>
                    <div className="ir-side mono small dim">{m.episodes} eps · {bytes(m.size)}</div>
                  </li>
                ))}
              </ul>
            )
          ) : (
            <>
              <div className="result-count mono dim">{loading && files.length === 0 ? "Loading…" : `${total.toLocaleString()} file${total === 1 ? "" : "s"}`}</div>
              {!loading && files.length === 0 ? <Empty title="Nothing here">No files with this issue match the filters.</Empty> : (
                <ul className="issue-rows">
                  {files.map((f) => (
                    <li key={f.id} className="issue-row">
                      <a className="ir-main" href={`#/file/${f.id}`}>
                        <div className="ir-title">
                          {f.library === "tvshows" ? <>{f.title} <span className="dim">·</span> <EpisodeLabel f={f} /></> : <>{f.title} {f.year > 0 && <span className="dim">({f.year})</span>}</>}
                        </div>
                        <div className="chips">
                          <CodecChip codec={f.video_codec} />
                          <span className="chip">{f.container.toUpperCase()}</span>
                          {f.audio.slice(0, 3).map((a, i) => <span key={i} className="chip">{a.codec.startsWith("pcm") ? "PCM" : a.codec.toUpperCase()}</span>)}
                          {f.height > 0 && <span className="chip">{resLabel(f.width, f.height)}</span>}
                        </div>
                        <IssueChips f={f} skip={[sel]} />
                      </a>
                      <div className="ir-side">
                        <span className="mono small dim">{bytes(f.size)}</span>
                        {f.queued ? <span className="tag tag-queued">queued</span> : hasQuickFix(f) ? (
                          <div className="ir-actions">
                            <button className="btn mini" onClick={() => fixOne(f, false)}>Fix</button>
                            <button className="btn mini" title="Fix now, ignoring the schedule" onClick={() => fixOne(f, true)}>Now</button>
                          </div>
                        ) : (
                          <a className="btn mini" href={`#/file/${f.id}`}>Open</a>
                        )}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              <div ref={sentinel} style={{ height: 1 }} />
            </>
          )}
        </section>
      )}
    </div>
  );
}

// SPDX-License-Identifier: GPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type FileItem } from "../api";
import { Empty, MovieCard, Toggle } from "../components";
import { FacetFilters, NO_FACETS, facetParams, type Facets } from "../filters";
import { bytes } from "../format";
import type { LiveState } from "../App";

const PAGE = 60;

export function useSessionState<T>(key: string, init: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const s = sessionStorage.getItem(key);
      return s ? (JSON.parse(s) as T) : init;
    } catch {
      return init;
    }
  });
  const set = (x: T) => {
    setV(x);
    try {
      sessionStorage.setItem(key, JSON.stringify(x));
    } catch {}
  };
  return [v, set];
}

export default function Library({ live }: { live: LiveState }) {
  const [search, setSearch] = useSessionState("mv.search", "");
  const [sort, setSort] = useSessionState("mv.sort", "savings");
  const [facets, setFacets] = useSessionState<Facets>("mv.facets", NO_FACETS);
  const [worth, setWorth] = useSessionState("mv.worth", true);
  const [files, setFiles] = useState<FileItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [stats, setStats] = useState<{ files: number; total_size: number; projected_saved: number } | null>(null);
  const sentinel = useRef<HTMLDivElement>(null);
  const reqId = useRef(0);

  const params = useCallback(
    (offset: number) => ({ library: "movies", title: search, sort, ...facetParams(facets), candidates: worth, offset, limit: PAGE }),
    [search, sort, facets, worth]
  );

  useEffect(() => {
    const id = ++reqId.current;
    const t = setTimeout(() => {
      setLoading(true);
      api.files(params(0)).then((r) => {
        if (id !== reqId.current) return;
        setFiles(r.files);
        setTotal(r.total);
      }).finally(() => id === reqId.current && setLoading(false));
    }, search ? 250 : 0);
    return () => clearTimeout(t);
  }, [params, live.queueVersion, live.arrVersion]);

  useEffect(() => {
    api.libraryStats("movies").then(setStats).catch(() => {});
  }, [live.queueVersion, live.scan?.running]);

  // Infinite scroll.
  useEffect(() => {
    const el = sentinel.current;
    if (!el) return;
    const io = new IntersectionObserver((entries) => {
      if (!entries[0].isIntersecting || loading || files.length >= total) return;
      setLoading(true);
      const id = reqId.current;
      api.files(params(files.length)).then((r) => {
        if (id === reqId.current) setFiles((f) => [...f, ...r.files]);
      }).finally(() => setLoading(false));
    }, { rootMargin: "800px" });
    io.observe(el);
    return () => io.disconnect();
  }, [files.length, total, loading, params]);

  return (
    <div>
      <header className="page-head">
        <div>
          <h1 className="page-title">Movies</h1>
          <div className="page-sub">
            {stats ? (
              <>
                {stats.files.toLocaleString()} files · {bytes(stats.total_size)}
                {stats.projected_saved > 0 && <> · <span className="teal">{bytes(stats.projected_saved)} reclaimable</span></>}
              </>
            ) : "—"}
          </div>
        </div>
        <button className="btn" onClick={() => api.scanStart().catch(() => {})} disabled={live.scan?.running}>
          {live.scan?.running ? "Scanning…" : "Rescan"}
        </button>
      </header>

      <div className="filters">
        <input className="input search" type="search" placeholder="Search movies" value={search}
          onChange={(e) => setSearch(e.target.value)} aria-label="Search movies" />
        <select className="input" value={sort} onChange={(e) => setSort(e.target.value)} aria-label="Sort">
          <option value="savings">Most space to save</option>
          <option value="size">Largest</option>
          <option value="bitrate">Highest bitrate</option>
          <option value="title">Title</option>
          <option value="added">Recently changed</option>
        </select>
        <Toggle on={worth} onChange={setWorth} label="Worth re-encoding" />
      </div>
      <FacetFilters library="movies" value={facets} onChange={setFacets} />

      <div className="result-count mono dim">{loading && files.length === 0 ? "Loading…" : `${total.toLocaleString()} movies`}</div>

      {!loading && files.length === 0 ? (
        <Empty title="Nothing matches">
          {worth ? "No movie clears your savings threshold with these filters. Turn off “Worth re-encoding” to see everything." : "Clear the search or filters."}
        </Empty>
      ) : (
        <div className="grid">
          {files.map((f) => <MovieCard key={f.id} f={f} />)}
        </div>
      )}
      <div ref={sentinel} style={{ height: 1 }} />
    </div>
  );
}

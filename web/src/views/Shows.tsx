// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useState } from "react";
import { api, type Series } from "../api";
import { Empty, ShowCard, Toggle } from "../components";
import { FacetFilters, NO_FACETS, facetParams, type Facets } from "../filters";
import { bytes } from "../format";
import type { LiveState } from "../App";
import { useScrollRestore } from "../scroll";
import { useSessionState } from "./Library";

// Last result, so coming back from a show renders the list (and its scroll
// position) at once while the slow aggregate query refreshes it.
let lastList: { key: string; list: Series[] } | null = null;

export default function Shows({ live }: { live: LiveState }) {
  const [search, setSearch] = useSessionState("sh.search", "");
  const [sort, setSort] = useSessionState("sh.sort", "reclaimable");
  const [worth, setWorth] = useSessionState("sh.worth", false);
  const [facets, setFacets] = useSessionState<Facets>("sh.facets", NO_FACETS);
  const query = { title: search, sort, candidates: worth, ...facetParams(facets) };
  const queryKey = JSON.stringify(query);
  const [list, setList] = useState<Series[] | null>(() => (lastList?.key === queryKey ? lastList.list : null));
  const [stats, setStats] = useState<{ files: number; total_size: number; projected_saved: number } | null>(null);

  useEffect(() => {
    const t = setTimeout(() => {
      api.series(query).then((r) => {
        lastList = { key: queryKey, list: r.series };
        setList(r.series);
      }).catch(() => setList([]));
    }, search ? 250 : 0);
    return () => clearTimeout(t);
  }, [queryKey, live.queueVersion]);
  useScrollRestore("shows", list !== null);

  useEffect(() => {
    api.libraryStats("tvshows").then(setStats).catch(() => {});
  }, [live.queueVersion]);

  return (
    <div>
      <header className="page-head">
        <div>
          <h1 className="page-title">Shows</h1>
          <div className="page-sub">
            {stats ? (
              <>
                {list?.length ?? "…"} shows · {stats.files.toLocaleString()} episodes · {bytes(stats.total_size)}
                {stats.projected_saved > 0 && <> · <span className="teal">{bytes(stats.projected_saved)} reclaimable</span></>}
              </>
            ) : "—"}
          </div>
        </div>
      </header>

      <div className="filters">
        <input className="input search" type="search" placeholder="Search shows" value={search}
          onChange={(e) => setSearch(e.target.value)} aria-label="Search shows" />
        <select className="input" value={sort} onChange={(e) => setSort(e.target.value)} aria-label="Sort">
          <option value="reclaimable">Most space to save</option>
          <option value="size">Largest</option>
          <option value="bitrate">Highest bitrate</option>
          <option value="episodes">Most episodes</option>
          <option value="title">Title</option>
        </select>
        <Toggle on={worth} onChange={setWorth} label="Has episodes worth re-encoding" />
      </div>
      <FacetFilters library="tvshows" value={facets} onChange={setFacets} />

      {list === null ? (
        <div className="result-count mono dim">Loading…</div>
      ) : list.length === 0 ? (
        <Empty title="No shows">{search || Object.values(facets).some(Boolean) ? "Nothing matches that search or these filters." : "Run a rescan to index the TV library."}</Empty>
      ) : (
        <div className="grid">
          {list.map((s) => <ShowCard key={s.title} s={s} />)}
        </div>
      )}
    </div>
  );
}

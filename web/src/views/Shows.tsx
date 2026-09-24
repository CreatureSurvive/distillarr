// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useState } from "react";
import { api, type Series } from "../api";
import { Empty, ShowCard, Toggle } from "../components";
import { bytes } from "../format";
import type { LiveState } from "../App";
import { useSessionState } from "./Library";

export default function Shows({ live }: { live: LiveState }) {
  const [search, setSearch] = useSessionState("sh.search", "");
  const [sort, setSort] = useSessionState("sh.sort", "reclaimable");
  const [worth, setWorth] = useSessionState("sh.worth", false);
  const [list, setList] = useState<Series[] | null>(null);
  const [stats, setStats] = useState<{ files: number; total_size: number; projected_saved: number } | null>(null);

  useEffect(() => {
    const t = setTimeout(() => {
      api.series({ title: search, sort, candidates: worth }).then((r) => setList(r.series)).catch(() => setList([]));
    }, search ? 250 : 0);
    return () => clearTimeout(t);
  }, [search, sort, worth, live.queueVersion]);

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

      {list === null ? (
        <div className="result-count mono dim">Loading…</div>
      ) : list.length === 0 ? (
        <Empty title="No shows">{search ? "Nothing matches that search." : "Run a rescan to index the TV library."}</Empty>
      ) : (
        <div className="grid">
          {list.map((s) => <ShowCard key={s.title} s={s} />)}
        </div>
      )}
    </div>
  );
}

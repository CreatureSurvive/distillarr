// SPDX-License-Identifier: GPL-3.0-or-later

// Retained originals grouped by library, then show and season. Libraries
// are open sections; shows and seasons start collapsed so a big trash stays
// scannable. Every level can be restored or deleted as a whole.

import type { MouseEvent, ReactNode } from "react";
import { api, type TrashBulkResult, type TrashItem } from "../api";
import { toast } from "../components";
import { ago, bytes, se, seasonName } from "../format";

type Act = (fn: () => Promise<unknown>, msg: string) => void;

const LIB_LABEL: Record<string, string> = { movies: "Movies", tvshows: "TV shows" };

function libLabel(lib: string): string {
  if (!lib) return "Not in the library";
  return LIB_LABEL[lib] || lib.charAt(0).toUpperCase() + lib.slice(1);
}

function fileName(p: string): string {
  return p.split("/").pop() || p;
}

const sum = (items: TrashItem[]) => items.reduce((a, t) => a + t.size, 0);
const count = (n: number) => `${n} file${n === 1 ? "" : "s"}`;

// groupBy keeps first-seen order; callers sort the keys themselves.
function groupBy<K>(items: TrashItem[], key: (t: TrashItem) => K): Map<K, TrashItem[]> {
  const m = new Map<K, TrashItem[]>();
  for (const t of items) {
    const k = key(t);
    const g = m.get(k);
    if (g) g.push(t);
    else m.set(k, [t]);
  }
  return m;
}

// bulk restores or deletes a whole group, reporting partial failures.
async function bulk(items: TrashItem[], restore: boolean) {
  const ids = items.map((t) => t.id);
  const r: TrashBulkResult = restore ? await api.restoreTrashMany(ids) : await api.deleteTrashMany(ids);
  if (r.count === 0) throw new Error(r.errors[0] || "nothing was changed");
  if (r.failed > 0) {
    toast(`${r.failed} of ${ids.length} failed: ${r.errors[0] || "unknown error"}`, "err");
  }
  return r;
}

function GroupActions({ items, what, act }: { items: TrashItem[]; what: string; act: Act }) {
  // Buttons live inside <summary>; stop the click from toggling it.
  const guard = (fn: () => void) => (e: MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    fn();
  };
  const n = items.length;
  return (
    <span className="toolbar">
      <button className="btn mini" onClick={guard(() =>
        confirm(`Put back all ${n} originals of ${what} and delete their encoded versions?`) &&
        act(() => bulk(items, true), `Restored ${what}`))}>Restore all</button>
      <button className="btn mini btn-danger" onClick={guard(() =>
        confirm(`Permanently delete all ${n} originals of ${what} (${bytes(sum(items))})?`) &&
        act(() => bulk(items, false), `Deleted ${what} from the trash`))}>Delete all</button>
    </span>
  );
}

function Row({ t, label, act }: { t: TrashItem; label: ReactNode; act: Act }) {
  return (
    <li>
      <div className="trash-name">
        <div>{label}</div>
        <div className="mono faint small" title={t.orig_path}>{!t.title && <>{fileName(t.orig_path)} · </>}{bytes(t.size)} · {ago(t.created_at)}</div>
      </div>
      <div className="toolbar">
        <button className="btn mini" onClick={() => confirm("Put the original back and delete the encoded version?") && act(() => api.restoreTrash(t.id), "Original restored")}>Restore</button>
        <button className="btn mini btn-danger" onClick={() => confirm("Delete this original permanently?") && act(() => api.deleteTrash(t.id), "Deleted")}>Delete</button>
      </div>
    </li>
  );
}

function Summary({ title, items, what, act }: { title: ReactNode; items: TrashItem[]; what: string; act: Act }) {
  return (
    <summary>
      <span className="trash-group-title">{title}</span>
      <span className="mono faint small">{count(items.length)} · {bytes(sum(items))}</span>
      <GroupActions items={items} what={what} act={act} />
    </summary>
  );
}

function Show({ title, items, act }: { title: string; items: TrashItem[]; act: Act }) {
  const seasons = [...groupBy(items, (t) => t.season)].sort((a, b) => a[0] - b[0]);
  return (
    <details className="trash-group">
      <Summary title={title} items={items} what={title} act={act} />
      <div className="trash-children">
        {seasons.map(([s, eps]) => (
          <details className="trash-group" key={s}>
            <Summary title={seasonName(s)} items={eps} what={`${title} ${seasonName(s)}`} act={act} />
            <ul className="trash-list">
              {[...eps].sort((a, b) => a.episode - b.episode).map((t) => (
                <Row key={t.id} t={t} act={act} label={<>{se(t.season, t.episode)}{t.ep_title && <span className="dim"> · {t.ep_title}</span>}</>} />
              ))}
            </ul>
          </details>
        ))}
      </div>
    </details>
  );
}

function Library({ lib, items, act }: { lib: string; items: TrashItem[]; act: Act }) {
  const isEp = (t: TrashItem) => !!t.title && t.episode > 0;
  const shows = [...groupBy(items.filter(isEp), (t) => t.title)].sort((a, b) => a[0].localeCompare(b[0]));
  const loose = items.filter((t) => !isEp(t)).sort((a, b) =>
    (a.title || fileName(a.orig_path)).localeCompare(b.title || fileName(b.orig_path)));
  const label = libLabel(lib);
  return (
    <div className="trash-lib">
      <div className="trash-lib-head">
        <h3>{label}</h3>
        <span className="mono faint small">{count(items.length)} · {bytes(sum(items))}</span>
        <GroupActions items={items} what={label} act={act} />
      </div>
      {shows.map(([title, eps]) => <Show key={title} title={title} items={eps} act={act} />)}
      {loose.length > 0 && (
        <ul className="trash-list">
          {loose.map((t) => (
            <Row key={t.id} t={t} act={act} label={t.title ? <>{t.title}{t.year > 0 && <span className="dim"> ({t.year})</span>}</> : fileName(t.orig_path)} />
          ))}
        </ul>
      )}
    </div>
  );
}

export function TrashList({ items, act }: { items: TrashItem[]; act: Act }) {
  const order = (lib: string) => (lib === "movies" ? 0 : lib === "tvshows" ? 1 : lib ? 2 : 3);
  const libs = [...groupBy(items, (t) => t.library)].sort((a, b) => order(a[0]) - order(b[0]) || a[0].localeCompare(b[0]));
  return <>{libs.map(([lib, its]) => <Library key={lib} lib={lib} items={its} act={act} />)}</>;
}

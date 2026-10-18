import { useMemo, useState } from "react";
import { api, type Drive } from "./api";
import type { Ctx } from "./App";
import { DriveChip, byId, driveColor } from "./drives";
import { ago, ageDays, bytes, count } from "./format";
import { go } from "./route";

export default function Overview({ ctx }: { ctx: Ctx }) {
  const { state, summary } = ctx;
  const known = state.drives.filter((d) => d.id != null);
  const fresh = state.drives.filter((d) => d.id == null);
  const groups = useMemo(() => {
    const m = new Map<string, Drive[]>();
    for (const d of known) {
      const g = d.group || "Ungrouped";
      m.set(g, [...(m.get(g) || []), d]);
    }
    return [...m.entries()].sort(([a], [b]) => (a === "Ungrouped" ? 1 : b === "Ungrouped" ? -1 : a.localeCompare(b)));
  }, [known]);

  const copies = summary?.copies || [];
  const one = copies.find((c) => c.ndrives === 1);
  const two = copies.find((c) => c.ndrives === 2);
  const more = copies.filter((c) => c.ndrives >= 3).reduce((a, c) => ({ files: a.files + c.files, bytes: a.bytes + c.bytes }), { files: 0, bytes: 0 });
  const total = summary?.totals.bytes || 0;

  if (known.length === 0) {
    return (
      <div className="empty-hero">
        <h1>No drives reconciled yet</h1>
        <p>Connect a drive and press <b>Reconcile drives</b>. Its file list is saved to your inventory folder, and you can disconnect it afterwards.</p>
        <button className="primary" onClick={() => ctx.openReconcile()}>Reconcile drives</button>
      </div>
    );
  }

  return (
    <div className="overview">
      <section className="tiles">
        <Tile label="Distinct content" value={bytes(total)} sub={`${count(summary?.totals.files)} files across ${known.length} drives`} />
        <Tile tone="bad" label="Only one copy" value={bytes(one?.bytes)} sub={`${count(one?.files)} files on a single drive`}
          onClick={() => go("files", { copies: "1", junk: "" })} />
        <Tile tone="warn" label="Two copies" value={bytes(two?.bytes)} sub={`${count(two?.files)} files`} onClick={() => go("files", { copies: "2" })} />
        <Tile tone="ok" label="Three or more" value={bytes(more.bytes)} sub={`${count(more.files)} files`} onClick={() => go("files", { copies: "3+" })} />
      </section>

      {total > 0 && (
        <section className="card">
          <div className="card-h"><h2>How many drives hold each file</h2><span className="muted">by size · OS litter excluded · click to list</span></div>
          <div className="stack">
            {[
              { k: "1", label: "1 drive", v: one?.bytes || 0, cls: "bad" },
              { k: "2", label: "2 drives", v: two?.bytes || 0, cls: "warn" },
              { k: "3+", label: "3+ drives", v: more.bytes, cls: "ok" },
            ].filter((s) => s.v > 0).map((s) => (
              <div key={s.k} className={`seg ${s.cls}`} style={{ flexGrow: s.v }} onClick={() => go("files", { copies: s.k })}
                title={`${s.label}: ${bytes(s.v)}`}>
                <span>{s.label} · {bytes(s.v)} · {Math.round((s.v / total) * 100)}%</span>
              </div>
            ))}
          </div>
        </section>
      )}

      {fresh.length > 0 && (
        <section className="card notice">
          <div>
            <b>Connected but never reconciled:</b>{" "}
            {fresh.map((d) => <DriveChip key={d.uuid} drive={d} small />)}
          </div>
          <button className="primary" onClick={() => ctx.openReconcile(fresh.map((d) => d.uuid!))}>Reconcile {fresh.length === 1 ? "it" : "them"}</button>
        </section>
      )}

      {groups.map(([g, ds]) => (
        <section key={g}>
          <h2 className="group-h">{groups.length === 1 && g === "Ungrouped" ? "Drives" : g} <span className="muted">{ds.length} drive{ds.length > 1 ? "s" : ""}</span></h2>
          <div className="drive-grid">
            {ds.map((d) => <DriveCard key={d.id} d={d} ctx={ctx} />)}
          </div>
        </section>
      ))}

      {known.length > 1 && summary && <Overlap ctx={ctx} />}
    </div>
  );
}

function Tile({ label, value, sub, tone, onClick }: { label: string; value: string; sub: string; tone?: string; onClick?: () => void }) {
  return (
    <div className={`tile ${tone || ""}${onClick ? " clickable" : ""}`} onClick={onClick}>
      <div className="tile-l">{label}</div>
      <div className="tile-v">{value}</div>
      <div className="tile-s">{sub}</div>
    </div>
  );
}

function DriveCard({ d, ctx }: { d: Drive; ctx: Ctx }) {
  const [editing, setEditing] = useState(false);
  const [group, setGroup] = useState(d.group);
  const cap = d.capacity || 0;
  const free = d.free_now ?? d.free ?? 0;
  const used = cap ? (cap - free) / cap : 0;
  const stale = ageDays(d.scanned_at) > 30;
  const save = async () => {
    setEditing(false);
    if (d.stem && group !== d.group) {
      await api.label(d.stem, group, d.note);
      ctx.refresh();
    }
  };
  return (
    <div className="drive-card" style={{ ["--c" as string]: driveColor(d.id) }}>
      <div className="dc-top">
        <div className="dc-name">
          <i className="dot" />
          <b title={d.name}>{d.name}</b>
          {d.connected ? <span className="tag live">connected</span> : <span className="tag">offline</span>}
          {d.role && <span className="tag">{d.role}</span>}
        </div>
        {editing ? (
          <input className="group-in" autoFocus value={group} placeholder="group, e.g. Old HDDs"
            onChange={(e) => setGroup(e.target.value)} onBlur={save} onKeyDown={(e) => e.key === "Enter" && save()} />
        ) : (
          <button className="link small" onClick={() => setEditing(true)} title="Group drives together on this page">
            {d.group ? "✎ group" : "+ group"}
          </button>
        )}
      </div>
      <div className="dc-bar" title={`${bytes(cap - free)} used of ${bytes(cap)}`}><span style={{ width: `${used * 100}%` }} /></div>
      <div className="dc-cap muted">{bytes(cap - free)} used · {bytes(free)} free · {d.filesystem}</div>
      <div className="dc-stats">
        <div><span className="k">Files</span><span>{count(d.files)}</span></div>
        <div><span className="k">In files</span><span>{bytes(d.bytes)}</span></div>
        <div className={d.unique_care_bytes ? "hot" : ""} title="Content that exists on no other reconciled drive, excluding installers, app bundles, build output, film rips and ROMs">
          <span className="k">Only here</span>
          <span>{bytes(d.unique_care_bytes)}<small className="muted"> of {bytes(d.unique_bytes)}</small></span>
        </div>
      </div>
      <div className="dc-foot">
        <span className={stale ? "stale" : "muted"} title={d.scanned_at || ""}>
          {stale ? "⚠ " : ""}reconciled {ago(d.scanned_at)}{d.errors_total ? ` · ${d.errors_total} unreadable` : ""}
        </span>
        <span className="actions">
          <button className="link" onClick={() => go("files", { drives: d.id, mode: "only" })}>Only here</button>
          <button className="link" onClick={() => go("folders", { drive: d.id })}>Folders</button>
          {d.connected && <button className="link" onClick={() => ctx.openReconcile([d.uuid!])}>Reconcile</button>}
        </span>
      </div>
    </div>
  );
}

function Overlap({ ctx }: { ctx: Ctx }) {
  const drives = ctx.state.drives.filter((d) => d.id != null);
  const m = byId(drives);
  const shared = new Map<string, { bytes: number; files: number }>();
  let max = 1;
  for (const o of ctx.summary!.overlap) {
    shared.set(`${o.a}-${o.b}`, o);
    shared.set(`${o.b}-${o.a}`, o);
    max = Math.max(max, o.bytes);
  }
  return (
    <section className="card">
      <div className="card-h"><h2>Shared content between drives</h2><span className="muted">distinct files present on both · click a cell to list them</span></div>
      <div className="matrix-wrap">
        <table className="matrix">
          <thead>
            <tr>
              <th />
              {drives.map((d) => <th key={d.id} className="col"><div><i style={{ background: driveColor(d.id) }} />{d.name}</div></th>)}
            </tr>
          </thead>
          <tbody>
            {drives.map((r) => (
              <tr key={r.id}>
                <th><DriveChip drive={r} small /></th>
                {drives.map((c) => {
                  if (r.id === c.id) return <td key={c.id} className="self" title={`${r.name} holds ${bytes(r.bytes)}`}>{bytes(r.bytes)}</td>;
                  const o = shared.get(`${r.id}-${c.id}`);
                  const a = o ? 0.12 + 0.88 * Math.sqrt(o.bytes / max) : 0;
                  return (
                    <td key={c.id} className={o ? "clickable" : "none"} style={{ ["--a" as string]: a }}
                      title={o ? `${m.get(r.id!)?.name} ∩ ${m.get(c.id!)?.name}: ${bytes(o.bytes)}, ${count(o.files)} files` : "nothing in common"}
                      onClick={() => o && go("files", { drives: `${r.id},${c.id}`, mode: "all" })}>
                      {o ? bytes(o.bytes) : "·"}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

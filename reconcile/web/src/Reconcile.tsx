import { useEffect, useState } from "react";
import { api, type Job } from "./api";
import type { Ctx } from "./App";
import { DriveChip } from "./drives";
import { ago, bytes, count, duration } from "./format";

const ACTIVE = new Set(["queued", "scanning", "indexing"]);

export default function Reconcile({ ctx, preselect, onClose }: { ctx: Ctx; preselect: string[]; onClose: () => void }) {
  const connected = ctx.state.drives.filter((d) => d.connected && d.uuid);
  const busy = new Set(ctx.state.jobs.filter((j) => ACTIVE.has(j.state)).map((j) => j.uuid));
  const [sel, setSel] = useState<Set<string>>(() => new Set(preselect));
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);

  useEffect(() => {
    ctx.refresh(); // pick up drives plugged in since the last poll
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = (u: string) => setSel((s) => { const n = new Set(s); n.has(u) ? n.delete(u) : n.add(u); return n; });
  const startable = [...sel].filter((u) => !busy.has(u) && connected.some((d) => d.uuid === u));
  const start = async () => {
    setStarting(true);
    try {
      const r = await api.reconcile(startable);
      if (r.not_connected.length) setError(`${r.not_connected.length} drive(s) were disconnected`);
      setSel(new Set());
      ctx.refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setStarting(false);
    }
  };
  const jobs = ctx.state.jobs;

  return (
    <div className="drawer-bg" onClick={onClose}>
      <aside className="drawer" onClick={(e) => e.stopPropagation()}>
        <div className="panel-h">
          <div>
            <b>Reconcile drives</b>
            <div className="muted small">Re-reads each drive's file list into your inventory. Read-only: nothing on the drives changes. Drives scan in parallel.</div>
          </div>
          <button className="x" onClick={onClose}>✕</button>
        </div>

        <div className="pad">
          {connected.length === 0 ? <p className="muted">No external drives connected.</p> : (
            <>
              <div className="sel-h">
                <span className="lbl">Connected</span>
                <button className="link small" onClick={() => setSel(new Set(connected.filter((d) => !busy.has(d.uuid!)).map((d) => d.uuid!)))}>select all</button>
              </div>
              <ul className="conn">
                {connected.map((d) => {
                  const running = busy.has(d.uuid!);
                  return (
                    <li key={d.uuid} className={running ? "running" : ""} onClick={() => !running && toggle(d.uuid!)}>
                      <input type="checkbox" readOnly disabled={running} checked={running || sel.has(d.uuid!)} />
                      <DriveChip drive={d} small />
                      <span className="muted small grow">
                        {bytes((d.capacity || 0) - (d.free_now ?? d.free ?? 0))} used · {d.id == null ? "never reconciled" : `reconciled ${ago(d.scanned_at)}`}
                      </span>
                      {running && <span className="tag live">running</span>}
                    </li>
                  );
                })}
              </ul>
              <button className="primary wide" disabled={!startable.length || starting} onClick={start}>
                {startable.length ? `Reconcile ${startable.length} drive${startable.length > 1 ? "s" : ""}` : "Select drives"}
              </button>
              {error && <p className="err">{error}</p>}
            </>
          )}
        </div>

        {jobs.length > 0 && (
          <div className="pad">
            <div className="lbl">Jobs</div>
            <ul className="jobs">{jobs.map((j) => <JobRow key={j.id} j={j} />)}</ul>
          </div>
        )}
      </aside>
    </div>
  );
}

function JobRow({ j }: { j: Job }) {
  const used = (j.capacity || 0) - (j.free || 0);
  const frac = j.state === "done" ? 1 : used > 0 ? Math.min(0.99, j.bytes / used) : 0;
  const rate = j.elapsed > 1 ? j.files / j.elapsed : 0;
  const label: Record<Job["state"], string> = {
    queued: "queued", scanning: "reading files", indexing: "updating index", done: "done", failed: "failed", cancelled: "cancelled",
  };
  return (
    <li className={`job ${j.state}`}>
      <div className="job-h">
        <b>{j.name}</b>
        <span className={`tag st-${j.state}`}>{ACTIVE.has(j.state) && <span className="spin" />}{label[j.state]}</span>
        <span className="grow" />
        {(j.state === "scanning" || j.state === "queued") && <button className="link small" onClick={() => api.cancel(j.id)}>cancel</button>}
      </div>
      {j.state !== "failed" && j.state !== "cancelled" && (
        <div className="jbar" title="Estimated from the drive's used space"><span style={{ width: `${frac * 100}%` }} /></div>
      )}
      <div className="muted small">
        {count(j.files)} files · {bytes(j.bytes)} · {duration(j.elapsed)}{rate > 0 && ACTIVE.has(j.state) ? ` · ${count(Math.round(rate))} files/s` : ""}
      </div>
      {j.current && j.state === "scanning" && <div className="muted small ellip" title={j.current}>{j.current}</div>}
      {j.error && <div className="err small">{j.error}</div>}
      {j.state === "cancelled" && <div className="muted small">The previous inventory of this drive was kept.</div>}
    </li>
  );
}

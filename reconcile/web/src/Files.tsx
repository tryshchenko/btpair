import { useEffect, useMemo, useRef, useState } from "react";
import { AgGridReact } from "ag-grid-react";
import type { ColDef, GridApi, ICellRendererParams, IDatasource, SortModelItem } from "ag-grid-community";
import { api, type Drive, type FileRow, type Location } from "./api";
import type { Ctx } from "./App";
import { DriveChip, byId } from "./drives";
import { bytes, count, date, parseSize } from "./format";
import { go, replaceParams } from "./route";
import { useTheme } from "./theme";

const MODES: [string, string][] = [
  ["any", "on any of"],
  ["all", "on all of"],
  ["only", "only on"],
  ["none", "not on"],
];
const COPIES: [string, string][] = [["", "Any"], ["1", "1 drive"], ["2", "2 drives"], ["3+", "3+ drives"]];
const SORT_KEYS = new Set(["name", "size", "ext", "ndrives", "copies", "newest", "category", "sample"]);

type GridCtx = { drives: Map<number, Drive>; categories: Ctx["state"]["categories"] };

/** Turns the page's URL params into the server's /api/files params. */
function apiParams(p: URLSearchParams, sort: SortModelItem | undefined): URLSearchParams {
  const q = new URLSearchParams();
  const pass = (from: string, to = from) => p.get(from) && q.set(to, p.get(from)!);
  pass("q");
  pass("paths");
  pass("drives");
  pass("mode");
  pass("exts");
  pass("drive");
  pass("prefix");
  const min = parseSize(p.get("min") || "");
  const max = parseSize(p.get("max") || "");
  if (min != null) q.set("min_size", String(min));
  if (max != null) q.set("max_size", String(max));
  const copies = p.get("copies");
  if (copies === "1") q.set("max_copies", "1");
  if (copies === "2") (q.set("min_copies", "2"), q.set("max_copies", "2"));
  if (copies === "3+") q.set("min_copies", "3");
  if (p.get("cats")) q.set("categories", p.get("cats")!.split(",").map((c) => (c === "content" ? "" : c)).join(","));
  else if (p.get("junk") !== "show") q.set("hide_junk", "1");
  if (sort && SORT_KEYS.has(sort.colId)) {
    q.set("sort", sort.colId);
    q.set("dir", sort.sort);
  }
  return q;
}

export default function Files({ ctx, params }: { ctx: Ctx; params: URLSearchParams }) {
  const theme = useTheme();
  const gridRef = useRef<GridApi<FileRow> | null>(null);
  const [totals, setTotals] = useState<{ n: number; bytes: number } | null>(null);
  const [selected, setSelected] = useState<FileRow | null>(null);
  const [error, setError] = useState("");
  const drives = useMemo(() => byId(ctx.state.drives), [ctx.state.drives]);
  const known = ctx.state.drives.filter((d) => d.id != null);
  const key = params.toString();

  const set = (k: string, v: string | null) => {
    const q = new URLSearchParams(params);
    if (v == null || v === "") q.delete(k);
    else q.set(k, v);
    replaceParams("files", q);
  };

  // Text inputs update the URL after a short pause, not on every keystroke.
  const [text, setText] = useState({ q: params.get("q") || "", min: params.get("min") || "", max: params.get("max") || "", exts: params.get("exts") || "" });
  useEffect(() => {
    setText({ q: params.get("q") || "", min: params.get("min") || "", max: params.get("max") || "", exts: params.get("exts") || "" });
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const t = setTimeout(() => {
      const q = new URLSearchParams(params);
      let changed = false;
      for (const [k, v] of Object.entries(text)) {
        const cur = params.get(k) || "";
        const val = k === "exts" ? v.toLowerCase().replace(/[\s.]+/g, ",").replace(/,+/g, ",").replace(/^,|,$/g, "") : v.trim();
        if (val !== cur) {
          changed = true;
          if (val) q.set(k, val);
          else q.delete(k);
        }
      }
      if (changed) replaceParams("files", q);
    }, 350);
    return () => clearTimeout(t);
  }, [text]); // eslint-disable-line react-hooks/exhaustive-deps

  const datasource = useMemo<IDatasource>(() => ({
    getRows: async (p) => {
      try {
        const q = apiParams(params, p.sortModel[0]);
        q.set("offset", String(p.startRow));
        q.set("limit", String(p.endRow - p.startRow));
        const res = await api.files(q);
        if (res.total != null) setTotals({ n: res.total, bytes: res.total_bytes || 0 });
        const last = res.rows.length < p.endRow - p.startRow ? p.startRow + res.rows.length : undefined;
        p.successCallback(res.rows, last);
        setError("");
      } catch (e) {
        setError(String(e));
        p.failCallback();
      }
    },
  }), [key]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    gridRef.current?.setGridOption("datasource", datasource);
  }, [datasource]);
  useEffect(() => {
    gridRef.current?.purgeInfiniteCache();
  }, [ctx.version]);

  const columns = useMemo<ColDef<FileRow>[]>(() => [
    { field: "name", headerName: "Name", flex: 2, minWidth: 220, tooltipField: "name" },
    { field: "size", headerName: "Size", width: 110, type: "rightAligned", sort: "desc", sortingOrder: ["desc", "asc"], valueFormatter: (v) => bytes(v.value) },
    { field: "ndrives", headerName: "On drives", width: 300, sortingOrder: ["asc", "desc"], cellRenderer: DrivesCell },
    { field: "copies", headerName: "Copies", width: 90, type: "rightAligned", headerTooltip: "Total copies, including duplicates on the same drive" },
    { field: "category", headerName: "Kind", width: 140, cellRenderer: CategoryCell },
    { field: "ext", headerName: "Ext", width: 80 },
    { field: "newest", headerName: "Modified", width: 115, valueFormatter: (v) => date(v.value), headerTooltip: "Newest modification date among the copies" },
    { field: "sample", headerName: "Path (first copy)", flex: 3, minWidth: 260, tooltipField: "sample", cellRenderer: PathCell },
  ], []);

  const drivesParam = new Set((params.get("drives") || "").split(",").filter(Boolean).map(Number));
  const toggleDrive = (id: number) => {
    const s = new Set(drivesParam);
    s.has(id) ? s.delete(id) : s.add(id);
    set("drives", [...s].join(","));
  };
  const cats = (params.get("cats") || "").split(",").filter(Boolean);
  const scoped = params.get("drive") && params.get("prefix");

  return (
    <div className="files">
      <div className="toolbar">
        <div className="row">
          <input className="search" placeholder={params.get("paths") ? "Search full paths…" : "Search file names…"} value={text.q}
            onChange={(e) => setText({ ...text, q: e.target.value })} />
          <label className="check"><input type="checkbox" checked={!!params.get("paths")} onChange={(e) => set("paths", e.target.checked ? "1" : null)} />search paths</label>
          <span className="sep" />
          <span className="lbl">Size</span>
          <input className="size" placeholder="min: 100MB" value={text.min} onChange={(e) => setText({ ...text, min: e.target.value })} />
          <span className="muted">–</span>
          <input className="size" placeholder="max" value={text.max} onChange={(e) => setText({ ...text, max: e.target.value })} />
          <span className="sep" />
          <span className="lbl">Ext</span>
          <input className="ext" list="exts" placeholder="jpg, mov…" value={text.exts} onChange={(e) => setText({ ...text, exts: e.target.value })} />
          <datalist id="exts">{ctx.summary?.exts.slice(0, 60).map((e) => <option key={e} value={e} />)}</datalist>
          <span className="sep" />
          <span className="lbl">Copies</span>
          <span className="seg-ctl">
            {COPIES.map(([v, l]) => (
              <button key={v} className={(params.get("copies") || "") === v ? "on" : ""} onClick={() => set("copies", v)}>{l}</button>
            ))}
          </span>
        </div>
        <div className="row">
          <span className="lbl">Drives</span>
          <select value={params.get("mode") || "any"} onChange={(e) => set("mode", e.target.value === "any" ? null : e.target.value)}>
            {MODES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </select>
          <span className="chips">
            {known.map((d) => (
              <DriveChip key={d.id} drive={d} small active={drivesParam.has(d.id!)} dim={drivesParam.size > 0 && !drivesParam.has(d.id!)}
                onClick={() => toggleDrive(d.id!)} />
            ))}
            {drivesParam.size > 0 && <button className="link small" onClick={() => set("drives", null)}>clear</button>}
          </span>
          <span className="sep" />
          <CategoryPicker ctx={ctx} value={cats} onChange={(v) => set("cats", v.join(","))} />
          {cats.length === 0 && (
            <label className="check" title="OS litter (.DS_Store, ._ files) and empty files">
              <input type="checkbox" checked={params.get("junk") !== "show"} onChange={(e) => set("junk", e.target.checked ? null : "show")} />hide OS litter
            </label>
          )}
        </div>
        {scoped && (
          <div className="row scope">
            Inside <DriveChip drive={drives.get(Number(params.get("drive"))) || { id: Number(params.get("drive")), name: "?" }} small />
            <code>{params.get("prefix")}/</code>
            <button className="link small" onClick={() => { const q = new URLSearchParams(params); q.delete("drive"); q.delete("prefix"); replaceParams("files", q); }}>✕ remove</button>
          </div>
        )}
        <div className="row summary-row">
          {totals ? <span><b>{count(totals.n)}</b> distinct files · <b>{bytes(totals.bytes)}</b></span> : <span className="muted">counting…</span>}
          {error && <span className="err">{error}</span>}
          <span className="grow" />
          <button className="link" onClick={() => go("files")}>reset filters</button>
        </div>
      </div>

      <div className="grid-and-panel">
        <div className={`grid ${theme === "dark" ? "ag-theme-quartz-dark" : "ag-theme-quartz"}`}>
          <AgGridReact<FileRow>
            columnDefs={columns}
            defaultColDef={{ sortable: true, resizable: true, suppressHeaderMenuButton: true }}
            rowModelType="infinite"
            cacheBlockSize={200}
            maxBlocksInCache={50}
            infiniteInitialRowCount={1}
            rowHeight={34}
            context={{ drives, categories: ctx.state.categories } satisfies GridCtx}
            onGridReady={(e) => {
              gridRef.current = e.api;
              e.api.setGridOption("datasource", datasource);
            }}
            getRowId={(p) => p.data.id}
            rowSelection="single"
            onRowClicked={(e) => setSelected(e.data ?? null)}
            tooltipShowDelay={400}
            overlayNoRowsTemplate="No files match these filters"
          />
        </div>
        {selected && <LocationsPanel row={selected} drives={drives} onClose={() => setSelected(null)} />}
      </div>
    </div>
  );
}

function DrivesCell(p: ICellRendererParams<FileRow, number, GridCtx>) {
  if (!p.data) return <span className="muted">…</span>;
  const ids = p.data.drives.split(",").map(Number);
  return (
    <span className="cell-chips">
      <b className={`n n${Math.min(ids.length, 3)}`}>{ids.length}</b>
      {ids.map((id) => {
        const d = p.context.drives.get(id);
        return d ? <DriveChip key={id} drive={d} small /> : null;
      })}
    </span>
  );
}

// Long paths are cut at the start, so the end (the useful part) stays visible.
function PathCell(p: ICellRendererParams<FileRow, string>) {
  return p.value ? <span className="tail"><bdi dir="ltr">{p.value}</bdi></span> : null;
}

function CategoryCell(p: ICellRendererParams<FileRow, string | null, GridCtx>) {
  if (!p.data) return null;
  const c = p.value;
  if (!c) return <span className="muted">content</span>;
  const v = p.context.categories[c]?.verdict || "";
  return <span className={`kind v-${v}`} title={p.context.categories[c]?.about}>{c}</span>;
}

function CategoryPicker({ ctx, value, onChange }: { ctx: Ctx; value: string[]; onChange: (v: string[]) => void }) {
  const [open, setOpen] = useState(false);
  const all = [["content", "content", "ordinary files"], ...Object.entries(ctx.state.categories).map(([c, x]) => [c, x.verdict, x.about])];
  const toggle = (c: string) => onChange(value.includes(c) ? value.filter((x) => x !== c) : [...value, c]);
  return (
    <span className="picker">
      <button className={value.length ? "on" : ""} onClick={() => setOpen(!open)}>
        Kind: {value.length ? value.join(", ") : "all"} ▾
      </button>
      {open && (
        <div className="menu" onMouseLeave={() => setOpen(false)}>
          {all.map(([c, v, about]) => (
            <label key={c} title={about}>
              <input type="checkbox" checked={value.includes(c)} onChange={() => toggle(c)} />
              <span className={`kind v-${v}`}>{c}</span>
              <span className="muted small">{v}</span>
            </label>
          ))}
          <div className="menu-f">
            <button className="link small" onClick={() => onChange(["content", "encrypted", "trash", "dashcam", "named-junk"])}>things I may care about</button>
            <button className="link small" onClick={() => onChange([])}>all</button>
          </div>
        </div>
      )}
    </span>
  );
}

function LocationsPanel({ row, drives, onClose }: { row: FileRow; drives: Map<number, Drive>; onClose: () => void }) {
  const [locs, setLocs] = useState<Location[] | null>(null);
  useEffect(() => {
    setLocs(null);
    api.locations(row.id).then(setLocs).catch(() => setLocs([]));
  }, [row.id]);
  return (
    <aside className="panel">
      <div className="panel-h">
        <div>
          <b className="break">{row.name}</b>
          <div className="muted">{bytes(row.size)} · {row.copies} cop{row.copies === 1 ? "y" : "ies"} on {row.ndrives} drive{row.ndrives > 1 ? "s" : ""}</div>
        </div>
        <button className="x" onClick={onClose}>✕</button>
      </div>
      {!locs ? <div className="muted pad">Loading…</div> : (
        <ul className="locs">
          {locs.map((l, i) => {
            const d = drives.get(l.drive_id);
            return (
              <li key={i}>
                <div>{d ? <DriveChip drive={d} small /> : l.drive}{d?.connected ? null : <span className="muted small"> offline</span>}</div>
                <code className="break">{l.path}</code>
                <div className="muted small">modified {l.mtime?.replace("T", " ")}
                  {d?.mount && <> · <button className="link small" onClick={() => navigator.clipboard.writeText(`${d.mount}/${l.path}`)}>copy full path</button></>}
                </div>
              </li>
            );
          })}
        </ul>
      )}
      <p className="muted small pad">"Same file" means same name and size on each drive. Contents aren't compared.</p>
    </aside>
  );
}

import { useEffect, useMemo, useState } from "react";
import { AgGridReact } from "ag-grid-react";
import type { ColDef, ICellRendererParams } from "ag-grid-community";
import { api, type FolderRow } from "./api";
import type { Ctx } from "./App";
import { DriveChip } from "./drives";
import { bytes, count, date } from "./format";
import { go } from "./route";
import { useTheme } from "./theme";

type Row = FolderRow & { path: string };
type FCtx = { drive: number; max: number };

export default function Folders({ ctx, params }: { ctx: Ctx; params: URLSearchParams }) {
  const theme = useTheme();
  const known = ctx.state.drives.filter((d) => d.id != null);
  const driveId = Number(params.get("drive")) || known[0]?.id || 0;
  const parent = params.get("path") || "";
  const drive = known.find((d) => d.id === driveId);
  const [rows, setRows] = useState<FolderRow[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!driveId) return;
    setRows(null);
    api.folders(driveId, parent).then((r) => (setRows(r), setError(""))).catch((e) => setError(String(e)));
  }, [driveId, parent, ctx.version]);

  const data = useMemo<Row[]>(() => (rows || []).map((r) => ({ ...r, path: r.child == null ? parent : parent ? `${parent}/${r.child}` : r.child })), [rows, parent]);
  const max = Math.max(1, ...data.map((r) => r.bytes));
  const total = data.reduce((a, r) => ({ bytes: a.bytes + r.bytes, uniq: a.uniq + r.unique_bytes, care: a.care + r.unique_care_bytes, files: a.files + r.files }), { bytes: 0, uniq: 0, care: 0, files: 0 });

  const open = (path: string) => go("folders", { drive: driveId, path });
  const crumbs = parent ? parent.split("/") : [];

  const columns = useMemo<ColDef<Row>[]>(() => [
    { field: "child", headerName: "Folder", flex: 2, minWidth: 240, cellRenderer: NameCell, comparator: (a, b) => (a ?? "").localeCompare(b ?? "") },
    { field: "bytes", headerName: "Size", width: 210, sort: "desc", cellRenderer: SizeCell },
    { field: "files", headerName: "Files", width: 100, type: "rightAligned", valueFormatter: (v) => count(v.value) },
    { field: "unique_care_bytes", headerName: "Only here (content)", width: 170, type: "rightAligned", cellRenderer: CareCell,
      headerTooltip: "Exists on no other reconciled drive, excluding installers, app bundles, build output, film rips and ROMs" },
    { field: "unique_bytes", headerName: "Only here (all)", width: 140, type: "rightAligned", valueFormatter: (v) => bytes(v.value) },
    { colId: "elsewhere", headerName: "Copied elsewhere", width: 170, cellRenderer: ElsewhereCell,
      valueGetter: (p) => (p.data && p.data.bytes ? 1 - p.data.unique_bytes / p.data.bytes : 1) },
    { field: "newest", headerName: "Newest file", width: 120, valueFormatter: (v) => date(v.value) },
    { colId: "act", headerName: "", width: 170, sortable: false, cellRenderer: ActionsCell },
  ], []);

  if (!known.length) return <div className="empty-hero"><h1>No drives reconciled yet</h1></div>;

  return (
    <div className="folders">
      <div className="toolbar">
        <div className="row">
          <span className="lbl">Drive</span>
          <span className="chips">
            {known.map((d) => <DriveChip key={d.id} drive={d} small active={d.id === driveId} dim={d.id !== driveId} onClick={() => go("folders", { drive: d.id })} />)}
          </span>
        </div>
        <div className="row crumbs">
          <button className="link" onClick={() => open("")}>{drive?.name}</button>
          {crumbs.map((c, i) => (
            <span key={i}><span className="muted"> / </span><button className="link" onClick={() => open(crumbs.slice(0, i + 1).join("/"))}>{c}</button></span>
          ))}
          <span className="grow" />
          {rows && (
            <span className="muted">
              {bytes(total.bytes)} in {count(total.files)} files · <b className={total.care ? "hot" : ""}>{bytes(total.care)}</b> content only here
            </span>
          )}
          {parent && <button className="link" onClick={() => go("files", { drive: driveId, prefix: parent })}>all files here →</button>}
        </div>
        {error && <div className="row err">{error}</div>}
      </div>
      <div className={`grid ${theme === "dark" ? "ag-theme-quartz-dark" : "ag-theme-quartz"}`}>
        <AgGridReact<Row>
          rowData={rows ? data : undefined}
          columnDefs={columns}
          defaultColDef={{ sortable: true, resizable: true, suppressHeaderMenuButton: true }}
          rowHeight={36}
          context={{ drive: driveId, max } satisfies FCtx}
          onRowDoubleClicked={(e) => e.data && e.data.child != null && open(e.data.path)}
          onCellClicked={(e) => e.colDef.field === "child" && e.data && e.data.child != null && open(e.data.path)}
          overlayNoRowsTemplate="This folder is empty"
        />
      </div>
      <p className="muted small hint">Click a folder name to open it. "Only here" means no file with the same name and size exists on any other reconciled drive.</p>
    </div>
  );
}

function NameCell(p: ICellRendererParams<Row>) {
  if (!p.data) return null;
  if (p.data.child == null) return <span className="muted">(files directly in this folder)</span>;
  return <span className="folder-name">{p.data.child}</span>;
}

function SizeCell(p: ICellRendererParams<Row, number, FCtx>) {
  if (!p.data) return null;
  const w = (p.data.bytes / p.context.max) * 100;
  return (
    <span className="size-cell">
      <span className="sbar"><span style={{ width: `${w}%` }} /><span className="u" style={{ width: `${(p.data.unique_bytes / p.context.max) * 100}%` }} /></span>
      <span>{bytes(p.data.bytes)}</span>
    </span>
  );
}

function CareCell(p: ICellRendererParams<Row, number>) {
  if (!p.data) return null;
  return <span className={p.value ? "hot" : "muted"}>{p.value ? bytes(p.value) : "—"}</span>;
}

function ElsewhereCell(p: ICellRendererParams<Row, number>) {
  if (!p.data) return null;
  const v = p.value ?? 0;
  return (
    <span className="pct">
      <span className="pbar"><span style={{ width: `${v * 100}%` }} className={v > 0.99 ? "ok" : v > 0.5 ? "warn" : "bad"} /></span>
      {Math.floor(v * 100)}%
    </span>
  );
}

function ActionsCell(p: ICellRendererParams<Row, unknown, FCtx>) {
  if (!p.data) return null;
  const { path } = p.data;
  const scope = path ? { drive: p.context.drive, prefix: path } : { drives: p.context.drive };
  return (
    <span className="actions">
      <button className="link small" onClick={() => go("files", scope)}>files</button>
      {p.data.unique_bytes > 0 && (
        <button className="link small" onClick={() => go("files", { ...scope, copies: "1", ...(path ? {} : { mode: "only" }) })}>only here</button>
      )}
    </span>
  );
}

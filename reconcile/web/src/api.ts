export type Drive = {
  id: number | null; // null: connected but never reconciled
  stem: string | null;
  name: string;
  uuid: string | null;
  filesystem?: string;
  capacity?: number;
  free?: number;
  free_now?: number;
  files?: number;
  bytes?: number;
  scanned_at?: string;
  errors_total?: number;
  pair_id?: string | null;
  role?: string | null;
  unique_files?: number;
  unique_bytes?: number;
  unique_care_bytes?: number;
  shared_bytes?: number;
  connected: boolean;
  mount?: string | null;
  group: string;
  note: string;
};

export type Job = {
  id: number;
  name: string;
  mount: string;
  uuid: string;
  state: "queued" | "scanning" | "indexing" | "done" | "failed" | "cancelled";
  files: number;
  bytes: number;
  current: string;
  error: string;
  started: number;
  finished: number | null;
  elapsed: number;
  capacity?: number;
  free?: number;
};

export type State = {
  drives: Drive[];
  jobs: Job[];
  index: { rebuilding: boolean };
  categories: Record<string, { verdict: string; about: string }>;
  verdicts: string[];
};

export type Summary = {
  built_at: string | null;
  build_seconds: string | null;
  totals: { files: number; bytes: number };
  copies: { ndrives: number; files: number; bytes: number }[];
  overlap: { a: number; b: number; files: number; bytes: number }[];
  categories: { category: string; files: number; bytes: number }[];
  exts: string[];
};

export type FileRow = {
  id: string;
  name: string;
  size: number;
  ext: string;
  copies: number;
  ndrives: number;
  drives: string;
  newest: string;
  oldest: string;
  sample: string;
  category: string | null;
};

export type FilesPage = { rows: FileRow[]; total?: number; total_bytes?: number };

export type Location = { drive_id: number; drive: string; path: string; size: number; mtime: string };

export type FolderRow = {
  child: string | null;
  files: number;
  bytes: number;
  unique_bytes: number;
  unique_files: number;
  unique_care_bytes: number;
  newest: string;
};

async function get<T>(path: string): Promise<T> {
  const r = await fetch(path);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

export const api = {
  state: (fresh = false) => get<State>(`/api/state${fresh ? "?fresh=1" : ""}`),
  summary: () => get<Summary>("/api/summary"),
  files: (params: URLSearchParams) => get<FilesPage>(`/api/files?${params}`),
  locations: (key: string) => get<Location[]>(`/api/locations?key=${encodeURIComponent(key)}`),
  folders: (drive: number, parent: string) =>
    get<FolderRow[]>(`/api/folders?drive=${drive}&parent=${encodeURIComponent(parent)}`),
  reconcile: (uuids: string[]) => post<{ started: Job[]; not_connected: string[] }>("/api/reconcile", { uuids }),
  cancel: (id: number) => post<{ ok: boolean }>(`/api/jobs/${id}/cancel`, {}),
  label: (stem: string, group: string, note: string) => post("/api/label", { stem, group, note }),
};

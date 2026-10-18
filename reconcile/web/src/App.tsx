import { useCallback, useEffect, useRef, useState } from "react";
import { api, type State, type Summary } from "./api";
import { go, useRoute } from "./route";
import Overview from "./Overview";
import Files from "./Files";
import Folders from "./Folders";
import Reconcile from "./Reconcile";

const ACTIVE = new Set(["queued", "scanning", "indexing"]);

export type Ctx = {
  state: State;
  summary: Summary | null;
  version: number; // bumps whenever the index changes
  refresh: () => void;
  openReconcile: (uuids?: string[]) => void;
};

export default function App() {
  const route = useRoute();
  const [state, setState] = useState<State | null>(null);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  // #/…?reconcile=1 opens the drawer (handy as a bookmark)
  const [drawer, setDrawer] = useState<{ open: boolean; preselect: string[] }>(() => ({ open: route.params.get("reconcile") === "1", preselect: [] }));
  const lastBuilt = useRef<string | null>(null);
  const wasBusy = useRef(false);

  const load = useCallback(async (fresh = false) => {
    try {
      const s = await api.state(fresh);
      setState(s);
      setError("");
      const busy = s.index.rebuilding || s.jobs.some((j) => ACTIVE.has(j.state));
      // Refresh the summary on startup, and whenever work just finished.
      if (lastBuilt.current === null || (wasBusy.current && !busy)) {
        const sum = await api.summary();
        if (sum.built_at !== lastBuilt.current) {
          lastBuilt.current = sum.built_at ?? "";
          setSummary(sum);
          setVersion((v) => v + 1);
        }
      }
      wasBusy.current = busy;
      return busy;
    } catch (e) {
      setError(String(e));
      return false;
    }
  }, []);

  useEffect(() => {
    let alive = true;
    let timer: number;
    const tick = async () => {
      const busy = await load();
      if (alive) timer = window.setTimeout(tick, busy ? 1000 : 4000);
    };
    tick();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [load]);

  if (!state) {
    return <div className="boot">{error ? `Can't reach the server: ${error}` : "Loading…"}</div>;
  }

  const running = state.jobs.filter((j) => ACTIVE.has(j.state));
  const ctx: Ctx = {
    state,
    summary,
    version,
    refresh: () => load(true),
    openReconcile: (uuids = []) => setDrawer({ open: true, preselect: uuids }),
  };
  const tab = (page: typeof route.page, label: string) => (
    <a className={route.page === page ? "on" : ""} href={`#/${page}`}>{label}</a>
  );

  return (
    <div className="app">
      <header className="top">
        <div className="brand" onClick={() => go("overview")}>
          <span className="logo"><i /><i /></span>
          Drive Reconcile
        </div>
        <nav>
          {tab("overview", "Overview")}
          {tab("files", "Files")}
          {tab("folders", "Folders")}
        </nav>
        <div className="right">
          {state.index.rebuilding && <span className="pill busy"><span className="spin" />Updating index</span>}
          {running.length > 0 && (
            <button className="pill busy" onClick={() => ctx.openReconcile()}>
              <span className="spin" />Reconciling {running.length} drive{running.length > 1 ? "s" : ""}
            </button>
          )}
          {error && <span className="pill err" title={error}>Server unreachable</span>}
          <button className="primary" onClick={() => ctx.openReconcile()}>Reconcile drives</button>
        </div>
      </header>
      <main>
        {route.page === "overview" && <Overview ctx={ctx} />}
        {route.page === "files" && <Files ctx={ctx} params={route.params} />}
        {route.page === "folders" && <Folders ctx={ctx} params={route.params} />}
      </main>
      {drawer.open && (
        <Reconcile ctx={ctx} preselect={drawer.preselect} onClose={() => setDrawer({ open: false, preselect: [] })} />
      )}
    </div>
  );
}

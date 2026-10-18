import { useEffect, useState } from "react";

// Location lives in the hash (#/files?drives=3&mode=only) so views are
// linkable, and back/forward work.

export type Page = "overview" | "files" | "folders";

export type Route = { page: Page; params: URLSearchParams };

function parse(): Route {
  const h = window.location.hash.replace(/^#\/?/, "");
  const [p, q] = h.split("?");
  const page = (["overview", "files", "folders"].includes(p) ? p : "overview") as Page;
  return { page, params: new URLSearchParams(q || "") };
}

export function useRoute(): Route {
  const [r, setR] = useState(parse);
  useEffect(() => {
    const on = () => setR(parse());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return r;
}

export function go(page: Page, params?: Record<string, string | number | boolean | null | undefined> | URLSearchParams) {
  const q = params instanceof URLSearchParams ? params : new URLSearchParams();
  if (params && !(params instanceof URLSearchParams)) {
    for (const [k, v] of Object.entries(params)) if (v != null && v !== "" && v !== false) q.set(k, String(v));
  }
  const s = q.toString();
  window.location.hash = `/${page}${s ? "?" + s : ""}`;
}

/** Replaces the current params without adding a history entry per keystroke. */
export function replaceParams(page: Page, q: URLSearchParams) {
  const s = q.toString();
  history.replaceState(null, "", `#/${page}${s ? "?" + s : ""}`);
  window.dispatchEvent(new HashChangeEvent("hashchange"));
}

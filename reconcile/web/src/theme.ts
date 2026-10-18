import { useEffect, useState } from "react";

const q = () => window.matchMedia("(prefers-color-scheme: dark)");

/** Follows the system light/dark setting (the grid needs it as a class). */
export function useTheme(): "light" | "dark" {
  const [dark, setDark] = useState(() => q().matches);
  useEffect(() => {
    const m = q();
    const on = () => setDark(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return dark ? "dark" : "light";
}

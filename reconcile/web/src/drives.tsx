import type { Drive } from "./api";

// Stable, distinguishable colours per drive id.
const PALETTE = [
  "#6d8cff", "#38c9a4", "#f2a541", "#e46f8a", "#a07cf0", "#4cb8e8", "#9bc94a", "#e8845a",
  "#5fd0c8", "#d886d6", "#c9b04a", "#7fa0b8", "#ef6b5b", "#58a86b", "#b78a6a", "#8d95f5",
];

export function driveColor(id: number | null | undefined): string {
  return id == null ? "#8a8f98" : PALETTE[(id - 1) % PALETTE.length];
}

export function DriveChip({ drive, small, onClick, active, dim }: {
  drive: Pick<Drive, "id" | "name"> & Partial<Pick<Drive, "connected">>;
  small?: boolean;
  onClick?: () => void;
  active?: boolean;
  dim?: boolean;
}) {
  const c = driveColor(drive.id);
  return (
    <span
      className={`chip${small ? " small" : ""}${onClick ? " clickable" : ""}${active ? " active" : ""}${dim ? " dim" : ""}`}
      style={{ ["--c" as string]: c }}
      onClick={onClick}
      title={drive.name + (drive.connected ? " · connected" : "")}
    >
      <i />
      {drive.name}
    </span>
  );
}

export function byId(drives: Drive[]): Map<number, Drive> {
  const m = new Map<number, Drive>();
  for (const d of drives) if (d.id != null) m.set(d.id, d);
  return m;
}

import type { CSSProperties } from "react";

export type IconName = "work" | "assets" | "integrations" | "reports" | "settings" | "search" | "sun" | "moon" | "monitor" | "chevron" | "arrow" | "close" | "refresh" | "check" | "info" | "warning" | "lock" | "clock" | "user" | "file" | "layers" | "code" | "grid" | "shield" | "external" | "filter" | "minus";

const paths: Record<IconName, string[]> = {
  work: ["M5 5h14v14H5z", "M8 9h8M8 13h5M8 17h3"],
  assets: ["M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z"],
  integrations: ["M8 7h8v10H8z", "M10 3v4M14 3v4M12 17v4M4 10h4M16 14h4"],
  reports: ["M5 3h10l4 4v14H5z", "M14 3v5h5M9 17v-4M13 17v-7M17 17v-2"],
  settings: ["M4 7h16M4 17h16", "M9 4v6M15 14v6"],
  search: ["M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13", "m16 16 5 5"],
  sun: ["M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8", "M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5 19 19M5 19l1.5-1.5M17.5 6.5 19 5"],
  moon: ["M20 15.2A8.6 8.6 0 0 1 8.8 4a8.6 8.6 0 1 0 11.2 11.2Z"],
  monitor: ["M3 4h18v12H3z", "M8 21h8M12 16v5"],
  chevron: ["m9 5 7 7-7 7"],
  arrow: ["M4 12h15", "m13 6 6 6-6 6"],
  close: ["m6 6 12 12M6 18 18 6"],
  refresh: ["M20 7v5h-5", "M4 17v-5h5", "M19 11a7 7 0 0 0-12-5L4 9M5 13a7 7 0 0 0 12 5l3-3"],
  check: ["m5 12 4 4L19 6"],
  info: ["M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20", "M12 11v6M12 7v.1"],
  warning: ["m12 3 10 18H2Z", "M12 9v5M12 17v.1"],
  lock: ["M5 10h14v11H5z", "M8 10V6a4 4 0 0 1 8 0v4M12 14v3"],
  clock: ["M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20", "M12 6v6l4 2"],
  user: ["M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8", "M4 21a8 8 0 0 1 16 0"],
  file: ["M5 3h10l4 4v14H5z", "M14 3v5h5M8 12h8M8 16h6"],
  layers: ["m12 3 10 5-10 5L2 8Z", "m2 12 10 5 10-5M2 16l10 5 10-5"],
  code: ["m8 7-5 5 5 5M16 7l5 5-5 5M14 4l-4 16"],
  grid: ["M3 3h8v8H3zM15 3h6v6h-6zM3 15h6v6H3zM13 13h8v8h-8z"],
  shield: ["m12 2 8 3v7c0 5-8 10-8 10S4 17 4 12V5Z", "m8 12 3 3 5-6"],
  external: ["M14 3h7v7M21 3 10 14M10 3H3v18h18v-7"],
  filter: ["M3 5h18M6 12h12M10 19h4"],
  minus: ["M5 12h14"],
};

export function Icon({ name, size = 18, className, style }: { name: IconName; size?: number; className?: string; style?: CSSProperties }) {
  return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.65" strokeLinecap="round" strokeLinejoin="round" className={className} style={style}>{paths[name].map((d, index) => <path d={d} key={index} />)}</svg>;
}

import { useSyncExternalStore } from "react";

export type Destination = "work" | "assets" | "integrations" | "reports" | "settings" | "gallery";
export interface Route { destination: Destination; findingId: string | null }

function subscribe(callback: () => void) {
  window.addEventListener("hashchange", callback);
  return () => window.removeEventListener("hashchange", callback);
}

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, () => window.location.hash, () => "");
  const url = new URL(hash.replace(/^#/, "") || "/work", window.location.origin);
  const path = url.pathname.replace(/^\/|\/$/g, "");
  const destination: Destination = path === "assets" || path === "integrations" || path === "reports" || path === "settings" || path === "gallery" ? path : "work";
  return { destination, findingId: destination === "work" ? url.searchParams.get("finding") : null };
}

export function showFinding(id: string | null): void {
  window.location.hash = id ? `/work?finding=${encodeURIComponent(id)}` : "/work";
}

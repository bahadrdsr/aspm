import { createContext, useCallback, useContext, useEffect, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { MotionConfig } from "motion/react";

type Theme = "light" | "dark";
type Preference = Theme | "system";
const key = "aspm.theme";

function subscribe(query: string, callback: () => void): () => void {
  const media = window.matchMedia(query);
  media.addEventListener("change", callback);
  return () => media.removeEventListener("change", callback);
}

export function useMedia(query: string): boolean {
  const onChange = useCallback((callback: () => void) => subscribe(query, callback), [query]);
  return useSyncExternalStore(onChange, () => window.matchMedia(query).matches, () => false);
}

const PreferencesContext = createContext<{
  theme: Theme;
  preference: Preference;
  reducedMotion: boolean;
  warning: string | null;
  chooseTheme: (value: Preference) => void;
} | null>(null);

export function Preferences({ children }: { children: ReactNode }) {
  const dark = useMedia("(prefers-color-scheme: dark)");
  const reducedMotion = useMedia("(prefers-reduced-motion: reduce)");
  const [initial] = useState(() => {
    try {
      const stored = localStorage.getItem(key);
      return { value: stored === "light" || stored === "dark" ? stored : "system", warning: null } as { value: Preference; warning: string | null };
    } catch {
      return { value: "system" as const, warning: "Theme storage is unavailable. Your choice will apply to this page only." };
    }
  });
  const [preference, setPreference] = useState<Preference>(initial.value);
  const [warning, setWarning] = useState(initial.warning);
  const theme = preference === "system" ? (dark ? "dark" : "light") : preference;
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.classList.toggle("dark", theme === "dark");
    document.documentElement.style.colorScheme = theme;
  }, [theme]);
  const chooseTheme = (value: Preference) => {
    setPreference(value);
    try {
      if (value === "system") localStorage.removeItem(key);
      else localStorage.setItem(key, value);
      setWarning(null);
    } catch {
      setWarning("Theme preference could not be saved. It still applies to this page.");
    }
  };
  return (
    <PreferencesContext value={{ theme, preference, reducedMotion, warning, chooseTheme }}>
      <MotionConfig reducedMotion="user" transition={{ duration: 0.18, ease: [0.2, 0, 0, 1] }}>
        {children}
      </MotionConfig>
    </PreferencesContext>
  );
}

export function usePreferences() {
  const value = useContext(PreferencesContext);
  if (!value) throw new Error("Preferences must wrap the application.");
  return value;
}

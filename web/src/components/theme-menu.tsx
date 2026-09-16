import { useEffect, useRef, useState } from "react";
import { usePreferences } from "@/lib/preferences";
import { Button } from "./ui/button";
import { Icon } from "./icon";
import type { IconName } from "./icon";

const choices = [
  { value: "light", label: "Light", icon: "sun" },
  { value: "dark", label: "Dark", icon: "moon" },
  { value: "system", label: "System", icon: "monitor" },
] as const satisfies readonly { value: "light" | "dark" | "system"; label: string; icon: IconName }[];

export function ThemeMenu() {
  const { theme, preference, chooseTheme } = usePreferences();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus();
    const dismiss = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false);
    };
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [open]);
  return <div className="theme-control" ref={root}>
    <Button ref={trigger} variant="ghost" size="icon" className="toolbar-icon" aria-label="Color theme" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      <Icon name={theme === "dark" ? "moon" : "sun"} size={19} />
    </Button>
    {open && <div role="menu" aria-label="Color theme options" className="theme-menu" ref={menu} onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); setOpen(false); trigger.current?.focus(); }
      const items = [...(menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [])];
      const current = items.findIndex((item) => item === document.activeElement);
      const index = event.key === "ArrowDown" ? (current + 1) % items.length
        : event.key === "ArrowUp" ? (current - 1 + items.length) % items.length
          : event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : -1;
      if (index >= 0) { event.preventDefault(); items[index]?.focus(); }
      if (event.key === "Tab") setOpen(false);
    }}>
      <span className="menu-label">Appearance</span>
      {choices.map((item) => <button key={item.value} type="button" role="menuitem" className="theme-option" onClick={() => { chooseTheme(item.value); setOpen(false); trigger.current?.focus(); }}>
        <Icon name={item.icon} /><span>{item.label}</span>{preference === item.value && <Icon name="check" size={16} />}
      </button>)}
    </div>}
  </div>;
}

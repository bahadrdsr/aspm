import { useEffect, useRef, useState } from "react";
import { Icon } from "@/components/icon";
import type { IconName } from "@/components/icon";
import { Button } from "@/components/ui/button";
import { ThemeMenu } from "@/components/theme-menu";
import { EmptyState } from "@/components/states";
import { FindingDialog } from "@/components/finding-dialog";
import { usePreferences } from "@/lib/preferences";
import { showFinding, useRoute } from "@/lib/router";
import type { Destination } from "@/lib/router";
import { WorkPage } from "@/pages/work";
import type { WorkContext } from "@/pages/work";
import { IntegrationsPage } from "@/pages/integrations";
import { AssetsPage } from "@/pages/assets";
import { GalleryPage } from "@/pages/gallery";
import { SessionBoundary, useSession } from "@/lib/session";

const destinations: { id: Destination; name: string; icon: IconName }[] = [
  { id: "work", name: "Work", icon: "work" },
  { id: "assets", name: "Assets", icon: "assets" },
  { id: "integrations", name: "Integrations", icon: "integrations" },
  { id: "reports", name: "Reports", icon: "reports" },
  { id: "settings", name: "Settings", icon: "settings" },
];

function PlaceholderPage({ name }: { name: "Assets" | "Reports" }) {
  return <>
    <header className="page-heading"><div><p className="eyebrow">{name === "Assets" ? "Understand ownership & coverage" : "Make posture explainable"}</p><h1>{name}</h1><p className="page-description">{name === "Assets" ? "A connected inventory starts with trustworthy source identity." : "Reports should explain changes, not confuse visibility with risk."}</p></div><span className="subtle-pill">Foundation preview</span></header>
    <section className="surface placeholder-surface"><EmptyState icon={name === "Assets" ? "assets" : "reports"} title={`${name === "Assets" ? "Asset inventory" : "Reporting"} is not available yet`} description={name === "Assets" ? "Inventory and ownership need the later authenticated, persistent data service. No assets have been invented for this preview." : "Saved reports and exports depend on the later data and authorization services. This preview does not manufacture posture metrics."}><Button asChild variant="outline"><a href="#/integrations">View integration plans<Icon name="arrow" /></a></Button></EmptyState></section>
    <div className="roadmap-note"><Icon name="info" /><p><strong>A deliberate boundary.</strong> M01 establishes the interface and reproducible builds. Persistence, authentication and ingestion are not implemented here.</p></div>
  </>;
}

function SettingsPage() {
  const { reducedMotion, theme } = usePreferences();
  return <>
    <header className="page-heading"><div><p className="eyebrow">Make the workspace your own</p><h1>Settings</h1><p className="page-description">Presentation preferences now. Operational controls when their services exist.</p></div></header>
    <div className="settings-grid">
      <section className="surface settings-card"><span className="settings-icon"><Icon name="grid" size={24} /></span><h2>Design system</h2><p>Explore the real shared controls, both themes, and clearly labeled synthetic states.</p><Button asChild variant="outline"><a href="#/gallery">Component gallery<Icon name="arrow" /></a></Button></section>
      <section className="surface settings-card"><span className="settings-icon"><Icon name="sun" size={24} /></span><h2>Appearance</h2><p>Your explicit theme preference is the only persistent browser setting. It contains no credentials.</p><dl className="settings-values"><div><dt>Current theme</dt><dd>{theme === "dark" ? "Dark" : "Light"}</dd></div><div><dt>Motion</dt><dd>{reducedMotion ? "Reduced by system" : "System default"}</dd></div></dl><p className="muted small">Use the color theme control in the header to change appearance.</p></section>
      <section className="surface settings-card full-width"><div className="section-heading"><h2>Preview boundaries</h2><span className="subtle-pill">M01</span></div><div className="boundary-grid"><div><Icon name="lock" /><h3>No account secrets</h3><p>Identity, SSO and credential management are not implemented. No credential-entry forms are exposed.</p></div><div><Icon name="shield" /><h3>AI &amp; proof are inactive</h3><p>No provider calls, model tools or active verification are started from this interface.</p></div><div><Icon name="layers" /><h3>No hidden backend</h3><p>Unimplemented data endpoints fail visibly. The component gallery is never an operational fallback.</p></div></div></section>
    </div>
  </>;
}

export default function App() {
  return <SessionBoundary><WorkspaceApplication /></SessionBoundary>;
}

function WorkspaceApplication() {
  const { session, workspace, changeWorkspace, signOut } = useSession();
  const route = useRoute();
  const { warning } = usePreferences();
  const [workContext, setWorkContext] = useState<WorkContext>({ query: "", selected: new Set(), page: 0, sort: "source-order" });
  const filterRef = useRef<HTMLInputElement>(null);
  const [focusFilter, setFocusFilter] = useState(false);
  const [opened, setOpened] = useState<{ title: string; trigger: HTMLElement | null }>({ title: "Finding details", trigger: null });
  const primary = route.destination === "gallery" ? "settings" : route.destination;
  const current = destinations.find((item) => item.id === primary)!;
  useEffect(() => {
    document.title = `${route.destination === "gallery" ? "Component gallery" : current.name} - aspm`;
  }, [current.name, route.destination]);
  useEffect(() => {
    if (route.destination === "work" && focusFilter) { filterRef.current?.focus(); setFocusFilter(false); }
  }, [route.destination, focusFilter]);
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k" && !document.querySelector("dialog[open]")) {
        event.preventDefault(); window.location.hash = "/work"; setFocusFilter(true);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);
  return <div className="app-shell">
    <a className="skip-link" href="#main-content" onClick={(event) => {
      event.preventDefault();
      document.getElementById("main-content")?.focus();
    }}>Skip to content</a>
    <aside className="sidebar">
      <a className="brand" href="#/work" aria-label="aspm home"><span className="brand-symbol" aria-hidden="true"><span /><span /><span /></span><span>aspm<span className="brand-caption">Security, in context.</span></span></a>
      <div className="workspace-context"><span className="workspace-monogram">{workspace.name.slice(0, 1)}</span><div><label htmlFor="workspace-selector" className="small muted">Workspace</label><select id="workspace-selector" aria-label="Workspace" value={workspace.id} onChange={(event) => changeWorkspace(event.target.value)}>{session.workspaces.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}</select><span>{workspace.role}</span></div></div>
      <p className="nav-section-label">Workspace</p>
      <nav aria-label="Primary" className="primary-nav">{destinations.map((item) => <a href={`#/${item.id}`} key={item.id} aria-current={primary === item.id ? "page" : undefined}><Icon name={item.icon} size={19} /><span>{item.name}</span>{primary === item.id && <span className="nav-active-mark" />}</a>)}</nav>
      <div className="sidebar-bottom"><Button variant="outline" onClick={signOut}>Sign out</Button><div className="preview-card"><span className="preview-symbol"><Icon name="shield" size={17} /></span><div><strong>{session.user.name}</strong><p>No required hosted AI.<br />No hidden data fallback.</p></div></div><div className="sidebar-meta"><span>Open-source foundation</span><span>Development</span></div></div>
    </aside>
    <div className="app-body">
      <header className="topbar"><div className="breadcrumb"><span>Workspace</span><Icon name="chevron" size={13} /><strong>{current.name}</strong>{route.destination === "gallery" && <><Icon name="chevron" size={13} /><span>Gallery</span></>}</div><div className="topbar-actions"><button type="button" className="quick-search" aria-label="Find findings" onClick={() => { window.location.hash = "/work"; setFocusFilter(true); }}><Icon name="search" size={16} /><span>Find findings</span><kbd>Ctrl K</kbd></button><span className="topbar-divider" /><ThemeMenu /><span className="preview-label">Engineering preview</span></div></header>
      <main id="main-content" tabIndex={-1}>
        {warning && <p role="alert" className="preference-warning"><Icon name="warning" />{warning}</p>}
        {route.destination === "work" && <WorkPage context={workContext} setContext={setWorkContext} filterRef={filterRef} openFinding={(finding, trigger) => { setOpened({ title: finding.title, trigger }); showFinding(finding.id); }} />}
        {route.destination === "assets" && <AssetsPage />}
        {route.destination === "reports" && <PlaceholderPage name="Reports" />}
        {route.destination === "integrations" && <IntegrationsPage />}
        {route.destination === "settings" && <SettingsPage />}
        {route.destination === "gallery" && <GalleryPage />}
      </main>
    </div>
    {route.findingId && <FindingDialog key={route.findingId} id={route.findingId} initialTitle={opened.title} returnFocus={opened.trigger} onClose={() => showFinding(null)} />}
  </div>;
}

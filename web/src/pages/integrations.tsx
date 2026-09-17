import { useState } from "react";
import { api } from "@/api/client";
import type { IntegrationId } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { label } from "@/lib/format";
import { ActionButton } from "@/components/action-button";
import { DataNotice, EmptyState, ErrorState, LoadingState } from "@/components/states";
import { Icon } from "@/components/icon";
import { SlackConnections } from "@/components/slack-connections";

const familyPresentation: Record<IntegrationId, { monogram: string; purpose: string }> = {
  github: { monogram: "GH", purpose: "Repository context" },
  gitlab: { monogram: "GL", purpose: "Repository & pipeline context" },
  "azure-devops": { monogram: "AD", purpose: "Pipeline report intake" },
  aws: { monogram: "AW", purpose: "Cloud posture" },
  azure: { monogram: "AZ", purpose: "Cloud posture" },
  jira: { monogram: "JI", purpose: "Remediation handoff" },
  teams: { monogram: "MT", purpose: "Team notifications" },
  slack: { monogram: "SL", purpose: "Team notifications" },
};

export function IntegrationsPage() {
  const resource = useResource(api.catalog);
  const dataOrigin = resource.data?.dataOrigin;
  const [query, setQuery] = useState("");
  const items = resource.data?.items.filter((item) => `${item.name} ${item.capabilities.join(" ")}`.toLowerCase().includes(query.toLowerCase())) ?? [];
  return <>
    <header className="page-heading"><div><p className="eyebrow">Bring context together</p><h1>Integrations</h1><p className="page-description">Know what each source can do before you connect it.</p></div><div className="heading-actions">{resource.data && <DataNotice origin={resource.data.dataOrigin} />}<ActionButton variant="outline" onClick={resource.reload} disabled={resource.status === "loading"}><Icon name="refresh" />Refresh</ActionButton></div></header>
    <div className="catalog-intro"><div><Icon name="integrations" size={24} /><p><strong>Capabilities first. Credentials later.</strong><span>Support maturity, connection health, and live verification are separate.</span></p></div><span className="subtle-pill">Read-only catalog</span></div>
    <SlackConnections />
    <div className="catalog-toolbar"><h2>Native integrations{resource.data && <span className="count-badge">{resource.data.items.length}</span>}</h2><div className="filter-field"><Icon name="search" size={17} /><input type="search" aria-label="Filter integrations" placeholder="Find a source or capability" value={query} onChange={(event) => setQuery(event.target.value)} /></div></div>
    {resource.error && <ErrorState error={resource.error} retry={resource.reload} stale={resource.data !== null} />}
    {resource.status === "loading" && !resource.data && <div className="surface"><LoadingState label="Loading integration catalog" /></div>}
    {resource.data && items.length === 0 && <div className="surface"><EmptyState icon="integrations" title="No integrations in this view" description="The API returned no matching catalog entries. Try another filter or refresh the catalog." /></div>}
    {resource.data && <ul className="integration-grid" aria-label="Native integrations">{items.map((item) => {
      const presentation = familyPresentation[item.id];
      const verified = dataOrigin === "live" && item.liveVerification.state === "passed" && item.supportMaturity === "supported";
      return <li className="integration-card" key={item.id}>
        <div className="integration-card-top"><span className={`family-mark family-${item.id}`} aria-hidden="true">{presentation.monogram}</span><span className="subtle-pill">{label(item.supportMaturity)}</span></div>
        <h3>{item.name}</h3><p className="integration-purpose">{presentation.purpose}</p>
        <div className="capability-tags">{item.capabilities.map((capability) => <span key={capability}>{label(capability)}</span>)}</div>
        <div className="connection-line"><span>Connection</span><strong>{item.connectionState === "unconfigured" ? "Not connected" : label(item.connectionState)}</strong></div>
        <div className="verification-line"><Icon name={verified ? "check" : "clock"} size={15} /><span>{verified ? "API reports verification passed" : "Not verified"}</span></div>
        <p className="verification-reason">{item.liveVerification.reason}</p>
        <div className="integration-footer"><Icon name="lock" size={14} /><span>{item.id === "slack" ? "Outbound destinations are managed in Connections" : "Setup is not available in this view"}</span></div>
      </li>;
    })}</ul>}
    <p className="view-footnote"><Icon name="info" size={15} />Report import formats do not count as native integrations. Connection configuration never sends a notification or verifies a catalog family.</p>
  </>;
}

import { useCallback, useMemo, useState } from "react";
import {
  notificationPolicyApi,
} from "@/api/notification-policies";
import type {
  NotificationChangeKind, NotificationPolicy, NotificationPolicyConnection,
  NotificationPolicyEvent, NotificationPolicyInput, NotificationPolicyPatch,
} from "@/api/notification-policies";
import { jiraApi } from "@/api/jira";
import { slackApi } from "@/api/slack";
import { teamsApi } from "@/api/teams";
import { webhookApi } from "@/api/generic-webhooks";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import "./notification-policies.css";

const orderedChanges: NotificationChangeKind[] = ["new", "changed", "reopened"];

function profileLabel(profile: NotificationPolicy["connectionProfile"]) {
  if (profile === "jira-cloud-v3") return "Jira";
  if (profile === "teams-workflows-channel") return "Teams";
  if (profile === "generic-webhook-v1") return "Webhook";
  return "Slack";
}

function sameChanges(left: NotificationChangeKind[], right: NotificationChangeKind[]) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function policyConnection(value: {
  id: string; name: string; profile: NotificationPolicyConnection["profile"];
  revision: number; enabled: boolean;
}): NotificationPolicyConnection {
  return { ...value, current: true };
}

function NotificationPolicyEditor({ policy, onClose, onSaved }: {
  policy: NotificationPolicy | null;
  onClose: () => void;
  onSaved: (policy: NotificationPolicy) => void;
}) {
  const action = useScopedAction();
  const loadConnections = useCallback(async (signal: AbortSignal) => {
    if (policy) return [policy.connection];
    const [slack, jira, teams, webhooks] = await Promise.all([
      slackApi.connections(100, null, signal),
      jiraApi.connections(null, signal),
      teamsApi.connections(null, signal),
      webhookApi.connections(null, signal),
    ]);
    return [
      ...slack.items.map(policyConnection),
      ...jira.items.map(policyConnection),
      ...teams.items.map(policyConnection),
      ...webhooks.items.map(policyConnection),
    ];
  }, [policy]);
  const connections = useResource(loadConnections);
  const [name, setName] = useState(policy?.name ?? "");
  const [connectionId, setConnectionId] = useState(policy?.connectionId ?? "");
  const [enabled, setEnabled] = useState(policy?.enabled ?? true);
  const [changeKinds, setChangeKinds] = useState<NotificationChangeKind[]>(policy?.changeKinds ?? ["new"]);
  const [minimumSeverity, setMinimumSeverity] =
    useState<NotificationPolicy["minimumSeverity"]>(policy?.minimumSeverity ?? "high");
  const [rationale, setRationale] = useState(policy?.rationale ?? "");
  const selected = connections.data?.find((item) => item.id === connectionId);
  const available = connections.data ?? [];
  const change = (kind: NotificationChangeKind, checked: boolean) => {
    setChangeKinds((current) => orderedChanges.filter((value) =>
      value === kind ? checked : current.includes(value)));
  };
  const submit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selected || !changeKinds.length) return;
    const input: NotificationPolicyInput = {
      name, connectionId, enabled, changeKinds, minimumSeverity, rationale,
    };
    if (!policy) {
      void action.run((signal) => notificationPolicyApi.create(input, signal),
        (response) => onSaved(response.policy));
      return;
    }
    const patch: NotificationPolicyPatch = { rationale };
    if (name !== policy.name) patch.name = name;
    if (connectionId !== policy.connectionId || !policy.connection.current) patch.connectionId = connectionId;
    if (enabled !== policy.enabled) patch.enabled = enabled;
    if (!sameChanges(changeKinds, policy.changeKinds)) patch.changeKinds = changeKinds;
    if (minimumSeverity !== policy.minimumSeverity) patch.minimumSeverity = minimumSeverity;
    void action.run((signal) => notificationPolicyApi.update(policy, patch, signal),
      (response) => onSaved(response.policy));
  };
  return <form className="notification-policy-editor" aria-label={policy ? "Edit notification policy" : "Add notification policy"}
    onSubmit={submit}>
    <header><h3>{policy ? "Edit notification policy" : "Add notification policy"}</h3>
      <Button type="button" variant="ghost" onClick={onClose}>Close</Button></header>
    <p className="form-help">Saving explicitly authorizes future matching changes to queue through this exact native connection revision.
      A queued outcome is not provider delivery.</p>
    <label>Policy name<input value={name} maxLength={256} required
      onChange={(event) => setName(event.target.value)} /></label>
    <label>Native connection<select aria-label="Native connection" value={connectionId} required
      disabled={connections.status === "loading" || action.pending}
      onChange={(event) => setConnectionId(event.target.value)}>
      <option value="">Select a native connection</option>
      {available.map((item) => <option key={item.id} value={item.id}>
        {item.name} - {profileLabel(item.profile)} - revision {item.revision}{item.enabled ? "" : " - disabled"}
      </option>)}
    </select></label>
    {connections.status === "loading" && <LoadingState label="Loading native connections" />}
    <fieldset><legend>Finding changes</legend>
      <label><input type="checkbox" checked={changeKinds.includes("new")}
        onChange={(event) => change("new", event.target.checked)} />New findings</label>
      <label><input type="checkbox" checked={changeKinds.includes("changed")}
        onChange={(event) => change("changed", event.target.checked)} />Changed findings</label>
      <label><input type="checkbox" checked={changeKinds.includes("reopened")}
        onChange={(event) => change("reopened", event.target.checked)} />Reopened findings</label>
    </fieldset>
    <label>Minimum severity<select aria-label="Minimum severity" value={minimumSeverity}
      onChange={(event) => setMinimumSeverity(event.target.value as NotificationPolicy["minimumSeverity"])}>
      <option value="critical">Critical</option><option value="high">High</option>
      <option value="medium">Medium</option><option value="low">Low</option><option value="info">Info</option>
    </select></label>
    <label><input type="checkbox" checked={enabled}
      onChange={(event) => setEnabled(event.target.checked)} />Enabled</label>
    <label>Rationale<textarea aria-label="Rationale" value={rationale} rows={3} maxLength={8192} required
      onChange={(event) => setRationale(event.target.value)} /></label>
    <FormError error={connections.error ?? action.error} />
    <footer><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || !selected || !changeKinds.length}>
        Save notification policy
      </ActionButton></footer>
  </form>;
}

function eventOutcome(event: NotificationPolicyEvent) {
  switch (event.outcome) {
    case "queued": return "Queued for delivery, not delivered";
    case "duplicate-ticket": return "Duplicate ticket prevented";
    case "connection-stale": return "Connection stale";
    case "invalid-payload": return "Invalid bounded payload";
  }
}

function NotificationPolicyEvents({ policy, onClose }: {
  policy: NotificationPolicy;
  onClose: () => void;
}) {
  const [cursor, setCursor] = useState<string | null>(null);
  const load = useCallback((signal: AbortSignal) =>
    notificationPolicyApi.events(policy.id, cursor, signal), [policy.id, cursor]);
  const resource = useResource(load), page = resource.data;
  return <section className="notification-policy-events" aria-label="Notification policy events">
    <header><div><h3>Notification policy events</h3><p className="form-help">{policy.name}</p></div>
      <Button type="button" variant="ghost" onClick={onClose}>Close events</Button></header>
    <FormError error={resource.error} />
    {resource.status === "loading" && !page && <LoadingState label="Loading notification policy events" />}
    {page && <table aria-label="Notification policy events"><thead><tr>
      <th scope="col">Event</th><th scope="col">Outcome</th><th scope="col">Finding</th>
    </tr></thead><tbody>{page.items.map((event) => <tr key={event.id}>
      <td><code>{event.id}</code><p>Policy revision {event.policyRevision}</p>
        <time dateTime={event.createdAt}>{timestampLabel(event.createdAt)}</time></td>
      <td><strong>{eventOutcome(event)}</strong>
        {event.deliveryId && <p>Delivery: <code>{event.deliveryId}</code></p>}</td>
      <td><code>{event.findingId}</code><p>Change revision {event.findingChangeRevision}</p></td>
    </tr>)}</tbody></table>}
    {page && page.items.length === 0 && <p className="form-help">No matching policy evaluations were recorded.</p>}
    {page?.nextCursor && <ActionButton variant="outline" onClick={() => setCursor(page.nextCursor)}>
      Next notification policy events
    </ActionButton>}
  </section>;
}

export function NotificationPolicies() {
  const { workspace } = useSession();
  const resource = useResource(notificationPolicyApi.policies);
  const [confirmed, setConfirmed] = useState<NotificationPolicy[]>([]);
  const [editor, setEditor] = useState<NotificationPolicy | null | undefined>(undefined);
  const [events, setEvents] = useState<NotificationPolicy | null>(null);
  const rows = useMemo(() => {
    const values = [...(resource.data?.items ?? []), ...confirmed];
    return [...new Map(values.map((item) => [item.id, item])).values()].sort((left, right) => left.id.localeCompare(right.id));
  }, [resource.data, confirmed]);
  const saved = (policy: NotificationPolicy) => {
    setConfirmed((items) => [...items.filter((item) => item.id !== policy.id), policy]);
    setEditor(undefined);
  };
  return <section className="surface notification-policies" aria-label="Notification policies">
    <header><div><h2>Notification policies</h2>
      <p className="form-help">Explicit workspace automation for authoritative new, changed, or reopened findings.
        Policy evaluation queues durable work only. It does not prove provider delivery.</p></div>
      <div className="notification-policy-actions">
        <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={resource.reload}>
          Refresh notification policies
        </ActionButton>
        {workspace.role === "admin" && <ActionButton onClick={() => setEditor(null)}>Add notification policy</ActionButton>}
      </div></header>
    {workspace.role !== "admin" && <p className="form-help">Read only. Only a current administrator may approve or revise automation.</p>}
    <FormError error={resource.error} />
    {resource.status === "loading" && !resource.data && <LoadingState label="Loading notification policies" />}
    <table aria-label="Notification policies"><thead><tr>
      <th scope="col">Policy</th><th scope="col">Trigger</th><th scope="col">Approval</th><th scope="col">Action</th>
    </tr></thead><tbody>{rows.map((policy) => <tr key={policy.id}>
      <td><strong>{policy.name}</strong><p>{profileLabel(policy.connectionProfile)} - {policy.connection.name}</p>
        <p>{policy.connection.current ? "Connection current" : "Connection stale"} - {policy.connection.enabled ? "Enabled" : "Disabled"}</p>
        <p>Policy revision {policy.revision}, connection revision {policy.connectionRevision}</p></td>
      <td><p>{policy.changeKinds.map(label).join(", ")}</p>
        <p>Minimum severity: {label(policy.minimumSeverity)}</p>
        <p>{policy.enabled ? "Automation enabled" : "Automation disabled"}</p></td>
      <td><p>{policy.rationale}</p><p>Approved by {policy.approvedByName}</p>
        <time dateTime={policy.updatedAt}>{timestampLabel(policy.updatedAt)}</time></td>
      <td><div className="notification-policy-actions">
        {workspace.role === "admin" && <Button type="button" variant="outline" size="sm"
          onClick={() => setEditor(policy)}>Edit notification policy</Button>}
        <Button type="button" variant="ghost" size="sm"
          onClick={() => setEvents(policy)}>View notification policy events</Button>
      </div></td>
    </tr>)}</tbody></table>
    {resource.data && rows.length === 0 && <p className="form-help">No notification policies are configured.
      Creating a connection alone never queues automatic work.</p>}
    {editor !== undefined && workspace.role === "admin" &&
      <NotificationPolicyEditor policy={editor} onClose={() => setEditor(undefined)} onSaved={saved} />}
    {events && <NotificationPolicyEvents policy={events} onClose={() => setEvents(null)} />}
  </section>;
}

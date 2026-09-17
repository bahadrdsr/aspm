import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { slackApi } from "@/api/slack";
import type { FindingDeliveryResponse } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";

export interface DeliveryAcknowledgements {
  revision: number;
  items: ReadonlyMap<string, { revision: number; response: FindingDeliveryResponse }>;
}

export function SlackDeliveryHistory({ findingId, acknowledged, onSelect }: {
  findingId: string; acknowledged: DeliveryAcknowledgements; onSelect: (id: string) => void;
}) {
  const [cursor, setCursor] = useState<string | null>(null);
  const currentRevision = useRef(acknowledged.revision);
  useLayoutEffect(() => { currentRevision.current = acknowledged.revision; }, [acknowledged.revision]);
  const load = useCallback(async (signal: AbortSignal) => {
    const revision = currentRevision.current;
    return { response: await slackApi.history(findingId, 100, cursor, signal), revision };
  }, [findingId, cursor]);
  const resource = useResource(load);
  const data = resource.data?.response;
  const rows = data ? [...new Map([
    ...data.items.map((item) => [item.id, item] as const),
    ...[...acknowledged.items.values()].filter((item) => item.revision > (resource.data?.revision ?? 0))
      .map((item) => [item.response.delivery.id, item.response.delivery] as const),
  ]).values()] : [];
  function refresh() {
    if (cursor !== null) setCursor(null);
    else resource.reload();
  }
  return <section className="surface slack-panel" aria-label="Delivery history">
    <header className="slack-panel-heading"><h3>Delivery history</h3>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}><Icon name="refresh" />Refresh delivery history</ActionButton>
    </header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry delivery history</ActionButton>}
      {resource.status === "loading" && !data && <LoadingState label="Loading delivery history" />}
      {resource.status === "loading" && data && <p className="form-help" role="status">Loading delivery history. Showing previously received rows.</p>}
      {data && <>
        <div className="slack-summary"><span>{data.total.toLocaleString("en-US")} deliveries at the last history read; {rows.length.toLocaleString("en-US")} shown</span>
          {data.dataOrigin && <DataNotice origin={data.dataOrigin} />}</div>
        {rows.length === 0 ? <div className="slack-empty"><h3>No deliveries</h3><p className="form-help">No delivery intents were returned for this finding.</p></div> :
          <div className="slack-table-scroll" tabIndex={0} role="region" aria-label="Delivery history results">
            <table className="slack-table" aria-label="Deliveries">
              <thead><tr><th scope="col">Delivery</th><th scope="col">Action</th></tr></thead>
              <tbody>{rows.map((item) => <tr key={item.id}>
                <td><code>{item.id}</code><p>{label(item.state)} · <code>{item.channel}</code></p>
                  <p><time dateTime={item.createdAt}>{timestampLabel(item.createdAt)}</time></p></td>
                <td><Button type="button" variant="outline" size="sm" onClick={() => onSelect(item.id)}>Open delivery</Button></td>
              </tr>)}</tbody>
            </table>
          </div>}
        {data.nextCursor !== null && <div className="slack-actions"><ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(data.nextCursor)}>Next deliveries<Icon name="chevron" /></ActionButton></div>}
        <p className="form-help">Manual reads only. A notification never changes finding workflow, ownership, evidence or verification.</p>
      </>}
    </div>
  </section>;
}

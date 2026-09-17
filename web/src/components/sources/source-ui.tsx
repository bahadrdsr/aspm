import type { APIError } from "@/api/client";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "@/components/action-button";
import { FormError } from "@/components/form-dialog";
import { LoadingState } from "@/components/states";

export function SourceReadState({ error, pending, loaded, retry, subject }: {
  error: APIError | null; pending: boolean; loaded: boolean; retry: () => void; subject: string;
}) {
  return <>
    <FormError error={error} />
    {error && <ActionButton variant="outline" onClick={retry}>Retry {subject}</ActionButton>}
    {pending && !loaded && <LoadingState label={`Loading ${subject}`} />}
    {pending && loaded && <p role="status" className="form-help">Loading {subject}. Showing previously received data.</p>}
  </>;
}
export function SourceTime({ value }: { value: string }) {
  return <time dateTime={value}>{timestampLabel(value)}</time>;
}

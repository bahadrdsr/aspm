import { useRef } from "react";
import type { FormEvent, RefObject } from "react";
import { api, APIError } from "@/api/client";
import type { Asset, AssetFields } from "@/api/types";
import { formText, inputChoice, inputText } from "@/lib/application-input";
import { label } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { Button } from "./ui/button";

const criticalities = ["low", "medium", "high", "critical"] as const;

export function AssetEditor({ asset, returnFocus, savedFocus, onClose, onSaved }: {
  asset: Asset | null; returnFocus: HTMLElement | null; savedFocus: RefObject<HTMLElement | null>;
  onClose: () => void; onSaved: (asset: Asset) => void;
}) {
  const { session, workspace } = useSession();
  const action = useScopedAction();
  const closeFocus = useRef<HTMLElement | null>(returnFocus);
  const title = asset ? "Edit asset" : "Create asset";
  const originalTags = asset?.tags.join(", ") ?? "";
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      const tagsText = formText(data, "tags");
      const tags = asset && tagsText === originalTags ? asset.tags : tagsText.split(",").map((tag) => tag.trim()).filter(Boolean);
      if (tags.length > 64) throw new APIError("An asset can have at most 64 tags.", "invalid-input", false);
      const owner = formText(data, "ownerId");
      const fields: AssetFields = {
        name: inputText(formText(data, "name"), "Asset name", 256),
        kind: inputText(formText(data, "kind"), "Kind", 64),
        environment: inputText(formText(data, "environment"), "Environment", 128, true),
        criticality: inputChoice(formText(data, "criticality"), criticalities, "criticality"),
        tags: tags.map((tag) => inputText(tag, "Each tag", 128)),
        ownerId: owner === "" ? null : inputText(owner, "Owner", 36),
      };
      if (!asset) return (await api.createAsset(fields, signal)).asset;
      const patch: Partial<AssetFields> = {
        ...(fields.name !== asset.name && { name: fields.name }),
        ...(fields.kind !== asset.kind && { kind: fields.kind }),
        ...(fields.environment !== asset.environment && { environment: fields.environment }),
        ...(fields.criticality !== asset.criticality && { criticality: fields.criticality }),
        ...(tagsText !== originalTags && { tags: fields.tags }),
        ...(fields.ownerId !== asset.ownerId && { ownerId: fields.ownerId }),
      };
      return (await api.updateAsset(asset.id, patch, signal)).asset;
    }, (result) => {
      closeFocus.current = savedFocus.current ?? returnFocus;
      onSaved(result);
    });
  };
  return <FormDialog title={title} description={`Inventory and ownership in ${workspace.name}. Changes are saved by the service.`}
    returnFocus={closeFocus} onClose={onClose}>
    <form aria-label={title} className="application-form" onSubmit={submit} aria-busy={action.pending}>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Asset details</legend>
        <label className="full-width">Asset name<input name="name" required maxLength={256} defaultValue={asset?.name ?? ""} autoComplete="off" /></label>
        <div className="form-field"><label htmlFor="asset-kind">Kind</label><select id="asset-kind" name="kind" defaultValue={asset?.kind ?? "repository"}><option value="repository">Repository</option>
          {asset && asset.kind !== "repository" && <option value={asset.kind}>{label(asset.kind)}</option>}
        </select></div>
        <label>Environment<input name="environment" maxLength={128} defaultValue={asset?.environment ?? ""} autoComplete="off" /></label>
        <div className="form-field"><label htmlFor="asset-criticality">Criticality</label><select id="asset-criticality" name="criticality" defaultValue={asset?.criticality ?? "medium"}>
          {criticalities.map((value) => <option value={value} key={value}>{label(value)}</option>)}
        </select></div>
        <div className="form-field"><label htmlFor="asset-owner">Owner</label><select id="asset-owner" name="ownerId" defaultValue={asset?.ownerId ?? ""}>
          <option value="">Unassigned</option><option value={session.user.id}>{session.user.name} (me)</option>
          {asset?.ownerId && asset.ownerId !== session.user.id && <option value={asset.ownerId}>Current owner ({asset.ownerId})</option>}
        </select></div>
        <label className="full-width">Tags<input name="tags" maxLength={8320} defaultValue={originalTags} autoComplete="off" aria-describedby="asset-tags-help" /></label>
        <p id="asset-tags-help" className="form-help full-width">Separate tags with commas. You can assign yourself, leave ownership unassigned, or keep the current owner.</p>
      </fieldset>
      <FormError error={action.error} />
      {action.pending && <p role="status" className="form-help">Saving with the service. Closing this form does not undo a request already sent.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || workspace.role === "viewer"}>{action.pending ? "Saving asset..." : asset ? "Save asset" : "Create asset"}</ActionButton>
      </footer>
    </form>
  </FormDialog>;
}

import { useCallback, useId, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { aiApi } from "@/api/ai";
import { aiFamilies } from "@/api/ai-types";
import type { AIProfile, AIProfileInput, AIProfilePatch } from "@/api/ai-types";
import { formText, inputChoice } from "@/lib/application-input";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "@/components/action-button";
import { Button } from "@/components/ui/button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { AIReadState } from "./ai-ui";
import { useAIMutation } from "./use-ai-data";
import type { AIData } from "./use-ai-data";

interface FormProps {
  profile: AIProfile | null;
  data: AIData;
  canWrite: boolean;
  onClose: () => void;
  onSaved: (profile: AIProfile) => void;
  onDenied: () => void;
}

function ProfileForm({ profile, data, canWrite, onClose, onSaved, onDenied }: FormProps) {
  const action = useAIMutation(data.profiles);
  const [family, setFamily] = useState<string>(profile?.family ?? "");
  const [clearKey, setClearKey] = useState(false);
  const key = useRef<HTMLInputElement>(null), deployment = useRef<HTMLInputElement>(null);
  const keyHelp = useId(), endpointHelp = useId();
  const clearPassword = () => { if (key.current) key.current.value = ""; };
  useLayoutEffect(() => {
    const field = key.current;
    return () => { if (field) field.value = ""; };
  }, []);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (!canWrite) throw new APIError("Only a workspace administrator can change AI profiles.", "forbidden", false);
      const selectedFamily = inputChoice(formText(values, "family"), aiFamilies, "family");
      const replacement = clearKey ? "" : formText(values, "apiKey");
      const input: AIProfileInput = {
        name: formText(values, "name"), family: selectedFamily, endpoint: formText(values, "endpoint"), model: formText(values, "model"),
        deployment: selectedFamily === "azure-foundry" ? formText(values, "deployment") : "",
        enabled: inputChoice(formText(values, "enabled"), ["true", "false"], "enabled state") === "true",
        structuredOutput: inputChoice(formText(values, "structuredOutput"), ["true", "false"], "structured-output review") === "true",
        ...(clearKey && selectedFamily === "local" ? { apiKey: null } : replacement !== "" ? { apiKey: replacement } : {}),
      };
      if (!profile) return aiApi.createProfile(input, signal);
      const patch: AIProfilePatch = {
        ...(input.name !== profile.name && { name: input.name }), ...(input.family !== profile.family && { family: input.family }),
        ...(input.endpoint !== profile.endpoint && { endpoint: input.endpoint }), ...(input.model !== profile.model && { model: input.model }),
        ...(input.deployment !== profile.deployment && { deployment: input.deployment }),
        ...(input.enabled !== profile.enabled && { enabled: input.enabled }),
        ...(input.structuredOutput !== profile.structuredOutput && { structuredOutput: input.structuredOutput }),
        ...(input.apiKey !== undefined && { apiKey: input.apiKey }),
      };
      return aiApi.updateProfile(profile, patch, signal);
    }, (receipt) => { clearPassword(); onSaved(receipt); }, (error) => {
      clearPassword();
      if (error.code === "forbidden") onDenied();
    });
  }
  return <form aria-label={profile ? "Edit AI profile" : "Create AI profile"} className="application-form ai-form"
    aria-busy={action.pending} onSubmit={submit}>
    {profile && <p className="form-help">Revision: {profile.revision}. Credential: {profile.credentialConfigured ? "configured" : "not configured"}.
      {" "}Credential presence does not verify provider access.</p>}
    <fieldset className="form-grid" disabled={action.pending || !canWrite}>
      <legend className="sr-only">AI profile choices</legend>
      <label className="full-width">Name<input name="name" required maxLength={256} defaultValue={profile?.name ?? ""} autoComplete="off" /></label>
      <label className="full-width">Family<select name="family" value={family} required onChange={(event) => {
        setFamily(event.target.value);
        setClearKey(false);
        if (event.target.value !== "azure-foundry" && deployment.current) deployment.current.value = "";
      }}>
        <option value="" disabled>Choose family</option>
        {aiFamilies.map((value) => <option key={value} value={value}>{value}</option>)}
      </select></label>
      <label className="full-width">Endpoint<input name="endpoint" required maxLength={16384} defaultValue={profile?.endpoint ?? ""}
        autoComplete="off" autoCapitalize="none" spellCheck={false} aria-describedby={endpointHelp} /></label>
      <p id={endpointHelp} className="form-help full-width">Choose the exact provider base yourself. Hosted families require HTTPS.
        {" "}Local HTTP requires a literal private or loopback IP, not localhost. No URL is probed or fetched by the browser.</p>
      <label className="full-width">Model<input name="model" required maxLength={256} defaultValue={profile?.model ?? ""} autoComplete="off" /></label>
      <label className="full-width">Deployment<input ref={deployment} name="deployment" required={family === "azure-foundry"} disabled={family !== "azure-foundry"}
        maxLength={256} defaultValue={profile?.deployment ?? ""} autoComplete="off" /></label>
      <p className="form-help full-width">Foundry deployment is a separate operator choice, not a model alias. No model or capability is assumed tested.</p>
      <label className="full-width">API key<input ref={key} name="apiKey" type="password" defaultValue="" maxLength={16384}
        required={family !== "" && family !== "local" && !profile?.credentialConfigured} disabled={clearKey}
        autoComplete="off" autoCapitalize="none" spellCheck={false} aria-describedby={keyHelp} /></label>
      <p id={keyHelp} className="form-help full-width">{profile ? "Leave blank to keep the stored API key. A nonempty replacement changes it. " : ""}
        The key is sent only to this application's configuration API. Drafts clear on close, success, failure or session/workspace loss.
        {" "}Local profiles may be keyless. Hosted keys require server-side encryption configured by the operator.</p>
      {profile && family === "local" && <label className="ai-checkbox full-width"><input type="checkbox" checked={clearKey}
        onChange={(event) => { setClearKey(event.target.checked); if (event.target.checked) clearPassword(); }} />Clear stored API key</label>}
      <label>Enabled<select name="enabled" required defaultValue={profile ? String(profile.enabled) : ""}>
        <option value="" disabled>Choose enabled state</option><option value="true">Enabled</option><option value="false">Disabled</option>
      </select></label>
      <label>Structured output reviewed<select name="structuredOutput" required defaultValue={profile ? String(profile.structuredOutput) : ""}>
        <option value="" disabled>Choose review state</option><option value="true">Reviewed</option><option value="false">Not reviewed</option>
      </select></label>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p role="status" className="form-help">Saving profile with the service. Closing does not undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || !canWrite}>{profile ? "Save profile" : "Create profile"}</ActionButton></footer>
  </form>;
}

function EditProfile({ id, data, ...props }: { id: string; data: AIData } & Omit<FormProps, "profile">) {
  const load = useCallback((signal: AbortSignal) => data.readProfile(id, signal), [data.readProfile, id]);
  const resource = useResource(load);
  const current = data.profiles.get(id);
  const withheld = resource.status === "ready" && current === null;
  return <>
    <AIReadState subject="profile" error={resource.error} pending={resource.status === "loading"} retry={resource.reload} />
    {withheld && <><p role="alert" className="form-error">Profile metadata is withheld. Read its current details again.</p>
      <ActionButton variant="outline" onClick={resource.reload}>Retry profile</ActionButton></>}
    {resource.data && current && <ProfileForm key={current.revision} profile={current} data={data} {...props} />}
  </>;
}

export function ProfileEditor({ id, data, returnFocus, ...props }: {
  id: string | null; data: AIData; returnFocus: HTMLElement;
} & Omit<FormProps, "profile">) {
  return <FormDialog title={id ? "Edit AI profile" : "Create AI profile"} returnFocus={returnFocus} onClose={props.onClose} containFocus
    description="Configuration only for the selected workspace. Credential presence is not healthy or verified provider access. Setup starts no assessment.">
    {id ? <EditProfile id={id} data={data} {...props} /> : <ProfileForm profile={null} data={data} {...props} />}
  </FormDialog>;
}

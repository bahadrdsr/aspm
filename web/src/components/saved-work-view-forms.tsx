import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError, workSearchQuery } from "@/api/client";
import { workViewName, workViewSorts, workViewsApi } from "@/api/work-views";
import type { SavedWorkView, WorkViewFields, WorkViewSort } from "@/api/work-views";
import type { SavedWorkViewsData } from "@/lib/use-saved-work-views";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Button } from "./ui/button";

export interface WorkViewSnapshot { query: string; sort: WorkViewSort }
interface FormProps { data: SavedWorkViewsData; onClose: () => void }
interface CurrentFormProps extends FormProps { view: SavedWorkView; onReload: () => void }
type Operation = ReturnType<SavedWorkViewsData["beginWrite"]>;
type Problems = { name: APIError | null; query: APIError | null };
const noProblems: Problems = { name: null, query: null };

function draftFields(name: string, query: string, sort: WorkViewSort) {
  const problems: Problems = { ...noProblems };
  let nextName = "", nextQuery = "";
  try { nextName = workViewName(name); } catch (cause) { if (cause instanceof APIError) problems.name = cause; else throw cause; }
  try { nextQuery = workSearchQuery(query); } catch (cause) { if (cause instanceof APIError) problems.query = cause; else throw cause; }
  return { fields: { name: nextName, query: nextQuery, sort }, problems };
}

export function SaveWorkViewForm({ snapshot, data, onClose }: FormProps & { snapshot: WorkViewSnapshot }) {
  const action = useScopedAction();
  const [name, setName] = useState("");
  const [problems, setProblems] = useState<Problems>(noProblems);
  const [uncertain, setUncertain] = useState(false);
  const operation = useRef<Operation | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  useEffect(() => { nameRef.current?.focus(); }, []);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (action.pending || uncertain) return;
    const next = draftFields(name, snapshot.query, snapshot.sort);
    setProblems(next.problems);
    if (next.problems.name || next.problems.query) return;
    void action.run(async (signal) => {
      operation.current = data.beginWrite();
      const receipt = await workViewsApi.create(next.fields, signal);
      data.accept(operation.current, receipt, signal);
      return receipt;
    }, onClose, (error) => {
      if (operation.current) data.failedWrite(operation.current, error);
      if (["network", "unavailable", "invalid-response"].includes(error.code)) setUncertain(true);
    });
  }
  const error = problems.name ?? problems.query ?? (uncertain ? new APIError(
    "Creation is uncertain and may have reached the service. Refresh saved views and inspect the list before starting another explicit Save. This request will not be automatically repeated.",
    "unavailable", false,
  ) : action.error);
  return <form aria-label="Save current view" className="application-form saved-view-form" noValidate aria-busy={action.pending} onSubmit={submit}>
    <h3>Save current view</h3>
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Personal view name</legend>
      <label className="full-width">View name<input ref={nameRef} name="name" value={name} autoComplete="off"
        aria-invalid={problems.name !== null || undefined} aria-describedby="saved-view-name-help"
        onChange={(event) => { setName(event.target.value); setProblems(noProblems); }} /></label>
      <p className="form-help full-width" id="saved-view-name-help">Trimmed, NUL-free name, up to 256 UTF-8 bytes. Duplicate names are allowed.</p>
    </fieldset>
    <section aria-label="View snapshot" className="saved-view-snapshot">
      <h4>View snapshot</h4><p>{snapshot.query === "" ? "All findings (empty query)" : snapshot.query}</p>
      <p>Loaded sort: {snapshot.sort}</p>
      <p className="form-help">Confirmed server query, not an unsaved local filter. No findings, selection or access are saved.</p>
    </section>
    <FormError error={error} />
    {action.pending && <p role="status" className="form-help">Saving with the service. Closing cannot undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || uncertain}>Save view</ActionButton></footer>
  </form>;
}

export function EditWorkViewForm({ view, data, onClose, onReload }: CurrentFormProps) {
  const action = useScopedAction();
  const [name, setName] = useState(view.name), [query, setQuery] = useState(view.query), [sort, setSort] = useState(view.sort);
  const [problems, setProblems] = useState<Problems>(noProblems);
  const [conflict, setConflict] = useState(false);
  const operation = useRef<Operation | null>(null), nameRef = useRef<HTMLInputElement>(null);
  useEffect(() => { nameRef.current?.focus(); }, []);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (action.pending || conflict) return;
    const next = draftFields(name, query, sort);
    setProblems(next.problems);
    if (next.problems.name || next.problems.query) return;
    const fields: WorkViewFields = next.fields;
    if (fields.name === view.name && fields.query === view.query && fields.sort === view.sort) { onClose(); return; }
    void action.run(async (signal) => {
      operation.current = data.beginWrite(view.id);
      const receipt = await workViewsApi.update(view, fields, signal);
      data.accept(operation.current, receipt, signal);
      return receipt;
    }, onClose, (error) => {
      if (operation.current) data.failedWrite(operation.current, error);
      if (error.code === "conflict" || error.httpStatus === 409) setConflict(true);
    });
  }
  return <form aria-label="Edit saved view" className="application-form saved-view-form" noValidate aria-busy={action.pending} onSubmit={submit}>
    <h3>Edit saved view</h3><p className="form-help">Current authorized revision: {view.revision}. Editing this template does not change the confirmed Work snapshot.</p>
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Personal saved view preferences</legend>
      <label className="full-width">View name<input ref={nameRef} name="name" value={name} autoComplete="off"
        aria-invalid={problems.name !== null || undefined}
        onChange={(event) => { setName(event.target.value); setProblems((previous) => ({ ...previous, name: null })); }} /></label>
      <label className="full-width">View query<input name="query" value={query} autoComplete="off" spellCheck={false}
        aria-invalid={problems.query !== null || undefined}
        onChange={(event) => { setQuery(event.target.value); setProblems((previous) => ({ ...previous, query: null })); }} /></label>
      <label className="full-width">Loaded sort<select name="sort" value={sort} onChange={(event) => {
        const selected = workViewSorts.find((value) => value === event.target.value);
        if (selected !== undefined) setSort(selected);
      }}>{workViewSorts.map((value) => <option value={value} key={value}>{value}</option>)}</select></label>
      <p className="form-help full-width">Trimmed name: 1-256 UTF-8 bytes. Trimmed query: 0-512 UTF-8 bytes. Both must be NUL-free. Sorting applies only to loaded findings.</p>
    </fieldset>
    <FormError error={problems.name ?? problems.query ?? action.error} />
    {conflict && <ActionButton variant="outline" onClick={onReload}>Reload saved view</ActionButton>}
    {action.pending && <p role="status" className="form-help">Saving changed fields with the service. Closing is not rollback.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || conflict}>Save changes</ActionButton></footer>
  </form>;
}

export function DeleteWorkViewForm({ view, data, onClose, onReload }: CurrentFormProps) {
  const action = useScopedAction();
  const [conflict, setConflict] = useState(false);
  const operation = useRef<Operation | null>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (action.pending || conflict) return;
    void action.run(async (signal) => {
      operation.current = data.beginWrite(view.id);
      await workViewsApi.delete(view, signal);
      data.acceptDelete(operation.current, view, signal);
    }, onClose, (error) => {
      if (operation.current) data.failedWrite(operation.current, error);
      if (error.code === "conflict" || error.httpStatus === 409) setConflict(true);
    });
  }
  return <form aria-label="Delete saved view" className="application-form saved-view-form" aria-busy={action.pending} onSubmit={submit}>
    <h3>Delete saved view</h3><p>Delete "{view.name}" at authorized revision {view.revision}?</p>
    <p className="form-help">This removes only your personal template, not findings or an already confirmed Work query snapshot. Cancel sends no deletion.</p>
    <FormError error={action.error} />
    {conflict && <ActionButton variant="outline" onClick={onReload}>Reload saved view</ActionButton>}
    {action.pending && <p role="status" className="form-help">Awaiting service confirmation. Closing does not undo a deletion already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || conflict}>Confirm delete</ActionButton></footer>
  </form>;
}

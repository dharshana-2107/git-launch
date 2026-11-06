import { FormEvent, useState } from "react";
import { ApiError, ProjectInput } from "../api";

type Props = {
  initial?: ProjectInput;
  submitLabel: string;
  onSubmit: (input: ProjectInput) => Promise<void>;
};

const empty: ProjectInput = { name: "", repoUrl: "", branch: "main", subdomain: "", envVars: {} };

// One form used for both "create" and "edit", so the fields and validation
// display are written once.
export default function ProjectForm({ initial = empty, submitLabel, onSubmit }: Props) {
  const [values, setValues] = useState(initial);
  // Env vars are edited as rows, which is easier in a form than an object.
  const [envRows, setEnvRows] = useState(
    Object.entries(initial.envVars).map(([key, value]) => ({ key, value })),
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);

  const set = (field: keyof ProjectInput) => (e: { target: { value: string } }) =>
    setValues({ ...values, [field]: e.target.value });

  async function handleSubmit(e: FormEvent) {
    e.preventDefault(); // stop the browser's default full-page form post
    setSaving(true);
    setFieldErrors({});
    setFormError("");

    // Turn the rows back into an object, skipping blank keys.
    const envVars: Record<string, string> = {};
    for (const row of envRows) {
      if (row.key.trim()) envVars[row.key.trim()] = row.value;
    }

    try {
      await onSubmit({ ...values, envVars });
    } catch (err) {
      // The API's 422/409 responses carry one message per field.
      if (err instanceof ApiError && Object.keys(err.fields).length > 0) {
        setFieldErrors(err.fields);
      } else {
        setFormError(err instanceof Error ? err.message : "Something went wrong");
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} className="form card">
      <Field label="Project name" error={fieldErrors.name}>
        <input value={values.name} onChange={set("name")} placeholder="My API" required />
      </Field>

      <Field label="GitHub repository URL" error={fieldErrors.repoUrl} hint="Public repo with a Dockerfile in its root">
        <input
          value={values.repoUrl}
          onChange={set("repoUrl")}
          placeholder="https://github.com/owner/repo"
          required
        />
      </Field>

      <div className="row">
        <Field label="Branch" error={fieldErrors.branch}>
          <input value={values.branch} onChange={set("branch")} placeholder="main" />
        </Field>
        <Field label="Subdomain" error={fieldErrors.subdomain} hint={`${values.subdomain || "myapp"}.localhost`}>
          <input value={values.subdomain} onChange={set("subdomain")} placeholder="myapp" required />
        </Field>
      </div>

      <fieldset className="env">
        <legend>Environment variables</legend>
        {envRows.map((row, i) => (
          <div className="row env-row" key={i}>
            <input
              className="mono"
              placeholder="KEY"
              value={row.key}
              onChange={(e) => setEnvRows(envRows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
            />
            <input
              className="mono"
              placeholder="value"
              value={row.value}
              onChange={(e) => setEnvRows(envRows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
            />
            <button type="button" className="button ghost" onClick={() => setEnvRows(envRows.filter((_, j) => j !== i))}>
              Remove
            </button>
          </div>
        ))}
        {fieldErrors.envVars && <p className="error small">{fieldErrors.envVars}</p>}
        <button type="button" className="button ghost" onClick={() => setEnvRows([...envRows, { key: "", value: "" }])}>
          + Add variable
        </button>
      </fieldset>

      {formError && <p className="error">{formError}</p>}
      <button type="submit" className="button primary" disabled={saving}>
        {saving ? "Saving…" : submitLabel}
      </button>
    </form>
  );
}

function Field({ label, error, hint, children }: { label: string; error?: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {error ? <span className="error small">{error}</span> : hint && <span className="muted small">{hint}</span>}
    </label>
  );
}

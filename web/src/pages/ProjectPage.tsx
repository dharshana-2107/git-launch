import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, appUrl, Deployment, Project } from "../api";
import ProjectForm from "../components/ProjectForm";
import Deployments from "../components/Deployments";
import AppLogs from "../components/AppLogs";
import AutoDeploy from "../components/AutoDeploy";

// One project: its details, an edit form and delete.
// The Deployments section below holds the Deploy button and live build logs.
export default function ProjectPage() {
  const { id } = useParams(); // the :id part of /projects/:id
  const navigate = useNavigate();
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);
  const [live, setLive] = useState<Deployment | undefined>();
  const [refreshKey, setRefreshKey] = useState(0); // bump = reload deployments + app logs
  const [notice, setNotice] = useState("");

  // Re-runs whenever id changes (e.g. navigating from one project to another).
  useEffect(() => {
    api
      .getProject(Number(id))
      .then(setProject)
      .catch((e) => setError(e.message));
  }, [id]);

  async function handleDelete() {
    if (!project || !window.confirm(`Delete "${project.name}"? This cannot be undone.`)) return;
    await api.deleteProject(project.id);
    navigate("/");
  }

  if (error) return <p className="error">{error}</p>;
  if (!project) return <p className="muted">Loading…</p>;

  return (
    <>
      <Link to="/" className="muted small">
        ← All projects
      </Link>
      <div className="page-head">
        <h1>{project.name}</h1>
        <div className="actions">
          <button className="button" onClick={() => setEditing(!editing)}>
            {editing ? "Cancel" : "Edit"}
          </button>
          <button className="button danger" onClick={handleDelete}>
            Delete
          </button>
        </div>
      </div>

      {editing ? (
        <ProjectForm
          initial={{
            name: project.name,
            repoUrl: project.repoUrl,
            branch: project.branch,
            subdomain: project.subdomain,
            envVars: project.envVars ?? {},
          }}
          submitLabel="Save changes"
          onSubmit={async (input) => {
            const before = JSON.stringify(project.envVars ?? {});
            const updated = await api.updateProject(project.id, input);
            setProject(updated);
            setEditing(false);

            // Env vars only reach the app when its container restarts, so
            // restart the live image with them (no rebuild needed).
            if (live && JSON.stringify(updated.envVars ?? {}) !== before) {
              try {
                await api.redeploy(live.id);
                setNotice("Environment variables changed: restarting the app with them (no rebuild).");
              } catch (e) {
                setNotice(`Saved. Redeploy to apply the new environment variables (${e instanceof Error ? e.message : e}).`);
              }
              setRefreshKey((k) => k + 1);
            }
          }}
        />
      ) : (
        <dl className="card details">
          <dt>Repository</dt>
          <dd>
            <a href={project.repoUrl} target="_blank" rel="noreferrer">
              {project.repoUrl}
            </a>
          </dd>
          <dt>Branch</dt>
          <dd className="mono">{project.branch}</dd>
          <dt>URL</dt>
          <dd className="mono">
            <a href={appUrl(project.subdomain)} target="_blank" rel="noreferrer">
              {appUrl(project.subdomain)}
            </a>
          </dd>
          <dt>Environment variables</dt>
          <dd className="mono">
            {Object.keys(project.envVars ?? {}).length === 0
              ? "none"
              : Object.keys(project.envVars ?? {}).join(", ")}
          </dd>
          <dt>Created</dt>
          <dd>{new Date(project.createdAt).toLocaleString()}</dd>
        </dl>
      )}

      {notice && <p className="notice small">{notice}</p>}
      <Deployments
        projectId={project.id}
        subdomain={project.subdomain}
        refreshKey={refreshKey}
        onLiveChange={setLive}
      />
      <AppLogs projectId={project.id} refreshKey={live?.id ?? 0} />
      <AutoDeploy repoUrl={project.repoUrl} branch={project.branch} />
    </>
  );
}

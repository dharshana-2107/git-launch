import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, appUrl, Deployment, Project } from "../api";
import ProjectForm from "../components/ProjectForm";
import Deployments from "../components/Deployments";
import AppLogs from "../components/AppLogs";
import StatusPill from "../components/StatusPill";
import CopyButton from "../components/CopyButton";
import { relativeTime } from "../lib/time";

// One project: live URL, deployments and logs on the left; settings on the right.
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
    if (!project || !window.confirm(`Delete "${project.name}"? Its app, route and images are removed too.`)) return;
    await api.deleteProject(project.id);
    navigate("/");
  }

  if (error) return <p className="error">{error}</p>;
  if (!project) return <div className="panel skeleton" aria-busy="true" />;

  const url = appUrl(project.subdomain);
  const envKeys = Object.keys(project.envVars ?? {});

  return (
    <>
      <nav className="crumbs">
        <Link to="/">Projects</Link>
        <span aria-hidden="true">/</span>
        <span>{project.name}</span>
      </nav>

      <div className="page-head">
        <div className="title-line">
          <h1>{project.name}</h1>
          <StatusPill status={live ? "live" : "none"} />
        </div>
        <div className="actions">
          <button className="button" onClick={() => setEditing(!editing)}>
            {editing ? "Cancel" : "Edit settings"}
          </button>
          <button className="button danger" onClick={handleDelete}>
            Delete
          </button>
        </div>
      </div>

      {/* The one loud element on the page: where the app is served. */}
      {live ? (
        <section className="live-banner">
          <div className="live-main">
            <span className="live-label">Serving at</span>
            <a href={url} target="_blank" rel="noreferrer" className="live-url">
              {url.replace("http://", "")}
            </a>
          </div>
          <div className="live-meta">
            <span>
              Commit <code>{live.commitSha}</code>, deployed {relativeTime(live.finishedAt ?? live.createdAt)}
            </span>
            <span className="live-actions">
              <CopyButton text={url} label="Copy URL" />
              <a href={url} target="_blank" rel="noreferrer" className="button small-button">
                Open
              </a>
            </span>
          </div>
        </section>
      ) : (
        <section className="live-banner live-banner-off">
          <div className="live-main">
            <span className="live-label">Will be served at</span>
            <span className="live-url">{url.replace("http://", "")}</span>
          </div>
          <p className="muted small">Not live yet. Deploy to build the latest commit on {project.branch}.</p>
        </section>
      )}

      {notice && <p className="notice">{notice}</p>}

      <div className="project-grid">
        <div className="col-main">
          <Deployments projectId={project.id} refreshKey={refreshKey} onLiveChange={setLive} />
          <AppLogs projectId={project.id} refreshKey={live?.id ?? 0} />
        </div>

        <aside className="col-side">
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
                    setNotice("Environment variables changed. Restarting the app with them, no rebuild needed.");
                  } catch (e) {
                    setNotice(`Saved. Deploy again to apply the new environment variables (${e instanceof Error ? e.message : e}).`);
                  }
                  setRefreshKey((k) => k + 1);
                }
              }}
            />
          ) : (
            <section className="panel">
              <h2>Settings</h2>
              <dl className="details">
                <dt>Repository</dt>
                <dd>
                  <a href={project.repoUrl} target="_blank" rel="noreferrer">
                    {project.repoUrl.replace("https://github.com/", "")}
                  </a>
                </dd>
                <dt>Branch</dt>
                <dd>
                  <code>{project.branch}</code>
                </dd>
                <dt>Subdomain</dt>
                <dd>
                  <code>{project.subdomain}</code>
                </dd>
                <dt>Environment</dt>
                <dd>
                  {envKeys.length === 0 ? (
                    <span className="muted">No variables</span>
                  ) : (
                    <span className="env-keys">
                      {envKeys.map((k) => (
                        <code key={k}>{k}</code>
                      ))}
                    </span>
                  )}
                </dd>
                <dt>Created</dt>
                <dd>{new Date(project.createdAt).toLocaleDateString(undefined, { dateStyle: "medium" })}</dd>
              </dl>
            </section>
          )}
        </aside>
      </div>
    </>
  );
}

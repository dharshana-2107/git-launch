import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, appUrl, Project } from "../api";
import ProjectForm from "../components/ProjectForm";
import Deployments from "../components/Deployments";

// One project: its details, an edit form and delete.
// The Deployments section below holds the Deploy button and live build logs.
export default function ProjectPage() {
  const { id } = useParams(); // the :id part of /projects/:id
  const navigate = useNavigate();
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);

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
            setProject(await api.updateProject(project.id, input));
            setEditing(false);
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
          <dd className="mono">{appUrl(project.subdomain)}</dd>
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

      <Deployments projectId={project.id} />
    </>
  );
}

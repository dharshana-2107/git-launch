import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, appUrl, Project } from "../api";

// Dashboard: lists every project.
export default function ProjectsPage() {
  const [projects, setProjects] = useState<Project[] | null>(null); // null = still loading
  const [error, setError] = useState("");

  // Runs once after the first render ([] = no dependencies) to load the list.
  useEffect(() => {
    api
      .listProjects()
      .then(setProjects)
      .catch((e) => setError(e.message));
  }, []);

  return (
    <>
      <div className="page-head">
        <h1>Projects</h1>
        <Link to="/projects/new" className="button primary">
          New project
        </Link>
      </div>

      {error && <p className="error">Could not load projects: {error}</p>}
      {projects === null && !error && <p className="muted">Loading…</p>}
      {projects?.length === 0 && (
        <div className="empty">
          <p>No projects yet.</p>
          <p className="muted">Create one from a public GitHub repo that has a Dockerfile.</p>
        </div>
      )}

      <ul className="project-list">
        {projects?.map((p) => (
          // key lets React track each row between renders
          <li key={p.id} className="card">
            <Link to={`/projects/${p.id}`} className="project-name">
              {p.name}
            </Link>
            <div className="muted small">
              {p.repoUrl.replace("https://github.com/", "")} · {p.branch}
            </div>
            <div className="small mono">{appUrl(p.subdomain)}</div>
          </li>
        ))}
      </ul>
    </>
  );
}

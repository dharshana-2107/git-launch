import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, appUrl, Deployment, Project } from "../api";
import StatusPill from "../components/StatusPill";
import { relativeTime } from "../lib/time";

type Row = { project: Project; latest?: Deployment; live?: Deployment };

// Dashboard: every project with its current state at a glance.
export default function ProjectsPage() {
  const [rows, setRows] = useState<Row[] | null>(null); // null = still loading
  const [error, setError] = useState("");

  // Load the projects, then each project's recent deployments in parallel
  // (Promise.all), to show whether it is live and when it last deployed.
  useEffect(() => {
    (async () => {
      try {
        const projects = await api.listProjects();
        const lists = await Promise.all(projects.map((p) => api.listDeployments(p.id).catch(() => [])));
        setRows(
          projects.map((project, i) => ({
            project,
            latest: lists[i][0],
            live: lists[i].find((d) => d.status === "live"),
          })),
        );
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
  }, []);

  const liveCount = rows?.filter((r) => r.live).length ?? 0;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Projects</h1>
          {rows && rows.length > 0 && (
            <p className="lede">
              {rows.length} {rows.length === 1 ? "project" : "projects"}, {liveCount} live
            </p>
          )}
        </div>
        <Link to="/projects/new" className="button primary">
          New project
        </Link>
      </div>

      {error && <p className="error">Could not load projects: {error}. Is the API running on port 8080?</p>}
      {rows === null && !error && <div className="panel skeleton" aria-busy="true" />}

      {rows?.length === 0 && (
        <div className="panel empty">
          <h2>Deploy your first app</h2>
          <p className="muted">Create a project from a public GitHub repo that has a Dockerfile in its root.</p>
          <Link to="/projects/new" className="button primary">
            New project
          </Link>
        </div>
      )}

      {rows && rows.length > 0 && (
        <ul className="panel project-rows">
          {rows.map(({ project: p, latest, live }) => (
            <li key={p.id}>
              <Link to={`/projects/${p.id}`} className="project-row">
                <span className="project-main">
                  <span className="project-name">{p.name}</span>
                  <span className="muted small">
                    {p.repoUrl.replace("https://github.com/", "")} on {p.branch}
                  </span>
                </span>
                <span className="project-url mono">{appUrl(p.subdomain).replace("http://", "")}</span>
                <StatusPill status={latest && latest.status !== "stopped" ? latest.status : live ? "live" : "none"} />
                <span className="muted small project-when">
                  {latest ? relativeTime(latest.createdAt) : "never deployed"}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

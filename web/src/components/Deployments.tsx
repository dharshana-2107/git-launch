import { useCallback, useEffect, useState } from "react";
import { api, ApiError, appUrl, Deployment, isFinal } from "../api";
import BuildLogs from "./BuildLogs";

// The Deployments section of a project page: Deploy button, history list,
// and the live logs of the selected deployment.
export default function Deployments({ projectId, subdomain }: { projectId: number; subdomain: string }) {
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);

  // useCallback keeps the same function between renders, so it can safely be
  // a dependency of useEffect and a prop of BuildLogs.
  const refresh = useCallback(async () => {
    try {
      const list = await api.listDeployments(projectId);
      setDeployments(list);
      // Show the newest deployment's logs unless the user picked another.
      setSelectedId((current) => current ?? list[0]?.id ?? null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load deployments");
    }
  }, [projectId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function deploy() {
    setStarting(true);
    setError("");
    try {
      const d = await api.deploy(projectId);
      setSelectedId(d.id); // jump to the new deployment's logs
      await refresh();
    } catch (e) {
      // 409 = a deployment is already running for this project
      setError(e instanceof ApiError ? e.message : "Could not start the deployment");
    } finally {
      setStarting(false);
    }
  }

  const running = deployments.some((d) => !isFinal(d.status));
  const live = deployments.find((d) => d.status === "live");

  return (
    <section className="card deployments">
      <div className="page-head">
        <h2>Deployments</h2>
        <button className="button primary" onClick={deploy} disabled={starting || running}>
          {running ? "Deploying…" : "Deploy"}
        </button>
      </div>
      {error && <p className="error small">{error}</p>}

      {live ? (
        <p className="live-banner">
          <span className="badge badge-live">live</span>
          <a href={appUrl(subdomain)} target="_blank" rel="noreferrer" className="mono">
            {appUrl(subdomain)}
          </a>
          <span className="muted small">
            commit {live.commitSha} · {live.containerName}
          </span>
        </p>
      ) : (
        deployments.length > 0 && !running && <p className="muted small">Not live: no successful deployment yet.</p>
      )}

      {deployments.length === 0 ? (
        <p className="muted small">No deployments yet. Click Deploy to build the latest commit.</p>
      ) : (
        <ul className="deploy-list">
          {deployments.map((d) => (
            <li
              key={d.id}
              className={d.id === selectedId ? "selected" : ""}
              onClick={() => setSelectedId(d.id)}
            >
              <StatusBadge status={d.status} />
              <span className="mono">{d.commitSha || "…"}</span>
              <span className="commit-msg">{d.error || d.commitMessage}</span>
              <span className="muted small">{timing(d)}</span>
            </li>
          ))}
        </ul>
      )}

      {/* key forces a fresh BuildLogs (and a fresh stream) per deployment */}
      {selectedId !== null && <BuildLogs key={selectedId} deploymentId={selectedId} onStatus={refresh} />}
    </section>
  );
}

export function StatusBadge({ status }: { status: Deployment["status"] }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

// "2m 14s" once finished, otherwise how long ago it was created.
function timing(d: Deployment) {
  if (d.startedAt && d.finishedAt) {
    const secs = Math.round((Date.parse(d.finishedAt) - Date.parse(d.startedAt)) / 1000);
    return secs >= 60 ? `${Math.floor(secs / 60)}m ${secs % 60}s` : `${secs}s`;
  }
  return new Date(d.createdAt).toLocaleTimeString();
}

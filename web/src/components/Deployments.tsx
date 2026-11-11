import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, Deployment, isFinal } from "../api";
import BuildLogs from "./BuildLogs";
import StatusPill from "./StatusPill";
import { duration, relativeTime } from "../lib/time";

// The Deployments section of a project page: Deploy button, history list,
// and the live logs of the selected deployment.
type Props = {
  projectId: number;
  refreshKey: number; // bump to force a reload (e.g. after a config change)
  onLiveChange?: (live: Deployment | undefined) => void;
};

export default function Deployments({ projectId, refreshKey, onLiveChange }: Props) {
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
  }, [refresh, refreshKey]);

  // Poll every 5 s, so deployments started by a git push (webhook) show up
  // without reloading the page.
  useEffect(() => {
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
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

  // Tell the page which deployment is live (it needs it for config restarts).
  const onLiveRef = useRef(onLiveChange);
  onLiveRef.current = onLiveChange;
  useEffect(() => {
    onLiveRef.current?.(live);
  }, [live?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  async function rollback(d: Deployment) {
    if (!window.confirm(`Roll back to commit ${d.commitSha} (deployment #${d.id})? No rebuild needed.`)) return;
    setError("");
    try {
      const created = await api.redeploy(d.id);
      setSelectedId(created.id);
      await refresh();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not roll back");
    }
  }

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Deployments</h2>
        <button className="button primary" onClick={deploy} disabled={starting || running}>
          {running ? "Deploying…" : "Deploy"}
        </button>
      </div>
      {error && <p className="error small">{error}</p>}

      {deployments.length === 0 ? (
        <p className="muted small">No deployments yet. Deploy to build the latest commit.</p>
      ) : (
        <ul className="deploy-list">
          {deployments.map((d) => (
            <li
              key={d.id}
              className={d.id === selectedId ? "selected" : ""}
              onClick={() => setSelectedId(d.id)}
              tabIndex={0}
              onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && setSelectedId(d.id)}
              aria-selected={d.id === selectedId}
            >
              <StatusPill status={d.status} />
              <span className="deploy-what">
                <span className="deploy-msg">{d.error || d.commitMessage || "Waiting for the commit…"}</span>
                <span className="deploy-sub">
                  <code>{d.commitSha || "-------"}</code>
                  <span className={`trigger trigger-${d.trigger}`}>{triggerLabel(d)}</span>
                </span>
              </span>
              <span className="row-end">
                {d.status === "stopped" && d.imagePruned && <span className="muted small">image deleted</span>}
                {d.status === "stopped" && d.imageTag && !d.imagePruned && !running && (
                  <button
                    className="button ghost small-button"
                    onClick={(e) => {
                      e.stopPropagation(); // do not also select the row
                      rollback(d);
                    }}
                  >
                    Roll back
                  </button>
                )}
                <span className="deploy-time">
                  <span>{relativeTime(d.createdAt)}</span>
                  <span className="muted">{duration(d.startedAt, d.finishedAt)}</span>
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}

      {/* key forces a fresh BuildLogs (and a fresh stream) per deployment */}
      {selectedId !== null && <BuildLogs key={selectedId} deploymentId={selectedId} onStatus={refresh} />}
    </section>
  );
}

function triggerLabel(d: Deployment) {
  switch (d.trigger) {
    case "push":
      return "git push";
    case "rollback":
      return `rollback to #${d.sourceDeploymentId ?? "?"}`;
    case "config":
      return "env change";
    default:
      return "manual";
  }
}

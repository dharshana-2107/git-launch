import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";

// What the RUNNING app prints (its stdout/stderr), as opposed to build logs.
// Fetched on demand, with optional auto-refresh every 3 seconds.
export default function AppLogs({ projectId, refreshKey }: { projectId: number; refreshKey: number }) {
  const [lines, setLines] = useState<string[]>([]);
  const [container, setContainer] = useState("");
  const [message, setMessage] = useState("");
  const [follow, setFollow] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await api.appLogs(projectId);
      setLines(res.lines);
      setContainer(res.container);
      setMessage(res.lines.length === 0 ? "The app has not printed anything yet." : "");
    } catch (e) {
      setLines([]);
      setMessage(e instanceof ApiError && e.status === 404 ? "The app is not running. Deploy it to see its output here." : String(e));
    }
  }, [projectId]);

  // Reload when the project changes or a new deployment goes live (refreshKey).
  useEffect(() => {
    load();
  }, [load, refreshKey]);

  // setInterval while "follow" is on; the cleanup clears it when turned off.
  useEffect(() => {
    if (!follow) return;
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  }, [follow, load]);

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>App logs</h2>
        <div className="actions">
          <label className="small muted follow">
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Auto-refresh
          </label>
          <button className="button" onClick={load}>
            Refresh
          </button>
        </div>
      </div>
      <div className="logs-wrap">
        <div className="logs-head small muted">
          <span>{container ? `docker logs ${container}` : "app output"}</span>
          <span>{lines.length} {lines.length === 1 ? "line" : "lines"}</span>
        </div>
        <pre className="logs logs-short">
          {message && <span className="muted">{message}</span>}
          {lines.map((l, i) => (
            <div key={i}>{l}</div>
          ))}
        </pre>
      </div>
    </section>
  );
}

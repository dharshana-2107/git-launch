import { useEffect, useRef, useState } from "react";
import { deploymentEventsUrl, DeploymentStatus } from "../api";

type Props = {
  deploymentId: number;
  // Called on every status change, so the page can refresh its list.
  onStatus?: (status: DeploymentStatus) => void;
};

type Line = { line: number; text: string };

// Shows a deployment's build output, live.
//
// EventSource is the browser's built-in SSE client. It opens one long HTTP
// request, calls our listeners for each event, and RECONNECTS by itself if
// the connection drops, sending the last event id it saw (Last-Event-ID),
// so the server resumes from the next line.
export default function BuildLogs({ deploymentId, onStatus }: Props) {
  const [lines, setLines] = useState<Line[]>([]);
  const [connected, setConnected] = useState(false);
  const boxRef = useRef<HTMLPreElement>(null);

  // Keep the latest callback in a ref, so changing it does not reopen the stream.
  const onStatusRef = useRef(onStatus);
  onStatusRef.current = onStatus;

  useEffect(() => {
    setLines([]);
    const source = new EventSource(deploymentEventsUrl(deploymentId));

    source.onopen = () => setConnected(true);

    source.addEventListener("log", (e) => {
      const l: Line = JSON.parse((e as MessageEvent).data);
      // Ignore a line we already have (possible after a reconnect).
      setLines((prev) => (prev.length > 0 && prev[prev.length - 1].line >= l.line ? prev : [...prev, l]));
    });

    source.addEventListener("status", (e) => {
      const { status } = JSON.parse((e as MessageEvent).data);
      onStatusRef.current?.(status);
    });

    // "done" = the build is over. Close, or EventSource would reconnect forever.
    source.addEventListener("done", () => {
      source.close();
      setConnected(false);
    });

    source.onerror = () => setConnected(false); // EventSource retries on its own

    // Cleanup: runs when the component unmounts or deploymentId changes.
    return () => source.close();
  }, [deploymentId]);

  // Auto-scroll to the newest line, unless the user scrolled up to read.
  useEffect(() => {
    const box = boxRef.current;
    if (!box) return;
    const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
    if (nearBottom) box.scrollTop = box.scrollHeight;
  }, [lines]);

  return (
    <div className="logs-wrap">
      <div className="logs-head small muted">
        <span>Build output</span>
        <span className={connected ? "live-dot" : ""}>{connected ? "● Streaming" : `${lines.length} ${lines.length === 1 ? "line" : "lines"}`}</span>
      </div>
      <pre className="logs" ref={boxRef}>
        {lines.length === 0 && <span className="muted">Waiting for output…</span>}
        {lines.map((l) => (
          <div key={l.line} className={lineClass(l.text)}>
            <span className="ln">{l.line}</span>
            {l.text}
          </div>
        ))}
      </pre>
    </div>
  );
}

// Colour our own progress lines: → step, ✓ success, ✗ failure.
function lineClass(text: string) {
  if (text.startsWith("✓")) return "log-ok";
  if (text.startsWith("✗")) return "log-err";
  if (text.startsWith("→")) return "log-step";
  return "";
}

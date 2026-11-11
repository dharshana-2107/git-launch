import { useEffect, useState } from "react";
import { api } from "../api";

// Explains how to turn on deploy-on-push for this project's repo.
export default function AutoDeploy({ repoUrl, branch }: { repoUrl: string; branch: string }) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [path, setPath] = useState("/api/webhooks/github");

  useEffect(() => {
    api
      .config()
      .then((c) => {
        setEnabled(c.webhooksEnabled);
        setPath(c.webhookPath);
      })
      .catch(() => setEnabled(false));
  }, []);

  return (
    <section className="panel">
      <h2>Deploy on git push</h2>
      {enabled === false ? (
        <p className="muted small">
          Off. Start the API with <code className="mono">WEBHOOK_SECRET=&lt;any long random text&gt;</code> to turn it on.
        </p>
      ) : (
        <ol className="small steps">
          <li>
            Start a tunnel so GitHub can reach this machine:{" "}
            <code className="mono">cloudflared tunnel --url http://localhost:8080</code>
          </li>
          <li>
            In{" "}
            <a href={`${repoUrl.replace(/\.git$/, "")}/settings/hooks/new`} target="_blank" rel="noreferrer">
              the repo's webhook settings
            </a>
            : Payload URL <code className="mono">https://&lt;tunnel&gt;.trycloudflare.com{path}</code>, content type{" "}
            <code className="mono">application/json</code>, secret = your <code className="mono">WEBHOOK_SECRET</code>, event:
            just the push event.
          </li>
          <li>
            Push to <code className="mono">{branch}</code>: a deployment starts by itself and appears below.
          </li>
        </ol>
      )}
    </section>
  );
}

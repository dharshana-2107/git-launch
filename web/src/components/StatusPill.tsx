import { DeploymentStatus } from "../api";

// A coloured dot plus the status word. In-progress states pulse (unless the
// user prefers reduced motion), live glows.
const words: Record<DeploymentStatus, string> = {
  queued: "Queued",
  cloning: "Cloning",
  building: "Building",
  deploying: "Deploying",
  live: "Live",
  stopped: "Stopped",
  failed: "Failed",
};

export type PillState = DeploymentStatus | "none";

export default function StatusPill({ status }: { status: PillState }) {
  if (status === "none") {
    return (
      <span className="pill pill-none">
        <span className="dot" />
        Not deployed
      </span>
    );
  }
  return (
    <span className={`pill pill-${status}`}>
      <span className="dot" />
      {words[status]}
    </span>
  );
}

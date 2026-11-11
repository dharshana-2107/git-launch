import { useState } from "react";

// Copies text to the clipboard and confirms with "Copied" for a moment.
export default function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can be blocked (e.g. non-secure context): nothing to do.
    }
  }

  return (
    <button type="button" className="button ghost small-button" onClick={copy} aria-live="polite">
      {copied ? "Copied" : label}
    </button>
  );
}

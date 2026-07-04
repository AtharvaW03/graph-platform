import { useEffect, useState } from "react";
import { api } from "../api";

// Compact feedback bar behind the platform's quality metric ("results rated
// relevant in >= 80% of sessions"). Rendered directly under the query form,
// ABOVE any results, so it's visible without scrolling past a large table.
//
// One submission per rated query: optionally type a note first, then click a
// thumb - a single POST carries both. Resets whenever the rated query
// changes. Empty result sets are rateable too (a thumbs-down on "found
// nothing" is exactly the signal the metric needs).
export function FeedbackWidget({
  endpoint,
  query,
}: {
  endpoint: string;
  query: string;
}) {
  const [note, setNote] = useState("");
  const [state, setState] = useState<"idle" | "sending" | "sent" | "failed">(
    "idle",
  );

  useEffect(() => {
    setState("idle");
    setNote("");
  }, [endpoint, query]);

  const send = async (helpful: boolean) => {
    setState("sending");
    try {
      await api.sendFeedback({
        endpoint,
        query,
        helpful,
        note: note.trim() || undefined,
      });
      setState("sent");
    } catch {
      setState("failed");
    }
  };

  if (state === "sent") {
    return <div className="feedback-bar">Thanks - feedback recorded.</div>;
  }
  return (
    <div className="feedback-bar">
      <span>Helpful?</span>
      <button
        type="button"
        className="feedback-thumb"
        onClick={() => send(true)}
        disabled={state === "sending"}
        aria-label="Results were helpful"
      >
        👍
      </button>
      <button
        type="button"
        className="feedback-thumb"
        onClick={() => send(false)}
        disabled={state === "sending"}
        aria-label="Results were not helpful"
      >
        👎
      </button>
      <input
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="optional note, then click a thumb"
        maxLength={2000}
      />
      {state === "failed" && (
        <span className="feedback-error">couldn't record, try again</span>
      )}
    </div>
  );
}

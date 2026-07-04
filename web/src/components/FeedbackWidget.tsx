import { useEffect, useState } from "react";
import { api } from "../api";

// Compact feedback bar behind the platform's quality metric ("results rated
// relevant in >= 80% of sessions"). Rendered directly under the query form,
// ABOVE any results, so it's visible without scrolling past a large table.
//
// Flow: click a thumb to select a rating (highlighted), optionally add a
// note, then Send submits both in one POST. Nothing is recorded until Send
// is clicked. The bar resets whenever the rated query changes; empty result
// sets are rateable too (a thumbs-down on "found nothing" is exactly the
// signal the metric needs).
export function FeedbackWidget({
  endpoint,
  query,
}: {
  endpoint: string;
  query: string;
}) {
  const [choice, setChoice] = useState<boolean | null>(null);
  const [note, setNote] = useState("");
  const [state, setState] = useState<"idle" | "sending" | "sent" | "failed">(
    "idle",
  );

  useEffect(() => {
    setChoice(null);
    setNote("");
    setState("idle");
  }, [endpoint, query]);

  const send = async () => {
    if (choice === null) return;
    setState("sending");
    try {
      await api.sendFeedback({
        endpoint,
        query,
        helpful: choice,
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
        className={
          choice === true ? "feedback-thumb selected" : "feedback-thumb"
        }
        onClick={() => setChoice(true)}
        aria-pressed={choice === true}
        aria-label="Results were helpful"
      >
        👍
      </button>
      <button
        type="button"
        className={
          choice === false ? "feedback-thumb selected" : "feedback-thumb"
        }
        onClick={() => setChoice(false)}
        aria-pressed={choice === false}
        aria-label="Results were not helpful"
      >
        👎
      </button>
      <input
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="optional note"
        maxLength={2000}
      />
      <button
        type="button"
        className="feedback-send"
        onClick={send}
        disabled={choice === null || state === "sending"}
        title={choice === null ? "pick a thumb first" : "send feedback"}
      >
        Send
      </button>
      {state === "failed" && (
        <span className="feedback-error">couldn't record, try again</span>
      )}
    </div>
  );
}

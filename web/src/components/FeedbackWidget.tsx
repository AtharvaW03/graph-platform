import { useEffect, useState } from "react";
import { api } from "../api";
import { Button } from "./ui";
import "./FeedbackWidget.css";

// One rating per browser session is enough for the quality metric (the
// brief measures "sessions rated relevant", not queries), so after the
// first submission the bar collapses to a small "Rate these results" link
// for the rest of the session instead of nagging on every query.
const SESSION_KEY = "feedback-sent";

function sentThisSession(): boolean {
  try {
    return sessionStorage.getItem(SESSION_KEY) === "1";
  } catch {
    return false;
  }
}

// Compact feedback bar rendered directly under the query form, ABOVE any
// results, so it's visible without scrolling past a large table.
//
// Flow: click a thumb to select a rating (highlighted), optionally add a
// note, then Send submits both in one POST. Nothing is recorded until Send
// is clicked. The bar resets whenever the rated query changes.
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
  const [collapsed, setCollapsed] = useState(sentThisSession);

  useEffect(() => {
    setChoice(null);
    setNote("");
    setState("idle");
    setCollapsed(sentThisSession());
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
      try {
        sessionStorage.setItem(SESSION_KEY, "1");
      } catch {
        // private-mode storage failures just mean the bar shows again
      }
    } catch {
      setState("failed");
    }
  };

  if (state === "sent") {
    return <div className="feedback-bar">Thanks - feedback recorded.</div>;
  }

  if (collapsed) {
    return (
      <button
        type="button"
        className="feedback-collapsed"
        onClick={() => setCollapsed(false)}
      >
        Rate these results
      </button>
    );
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
        className="feedback-note"
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="optional note"
        aria-label="Feedback note"
        maxLength={2000}
      />
      <Button
        size="sm"
        onClick={send}
        disabled={choice === null || state === "sending"}
        loading={state === "sending"}
        title={choice === null ? "pick a thumb first" : "send feedback"}
      >
        Send
      </Button>
      {state === "failed" && (
        <span className="feedback-error">couldn't record, try again</span>
      )}
    </div>
  );
}

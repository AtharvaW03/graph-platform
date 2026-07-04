import { useEffect, useState } from "react";
import { api } from "../api";

// FeedbackWidget captures the thumbs up/down behind the platform's quality
// metric ("results rated relevant in >= 80% of sessions"). Render it next to
// a result set; it resets whenever the rated query changes.
export function FeedbackWidget({
  endpoint,
  query,
}: {
  endpoint: string;
  query: string;
}) {
  const [state, setState] = useState<"idle" | "sending" | "sent" | "failed">(
    "idle",
  );

  useEffect(() => {
    setState("idle");
  }, [endpoint, query]);

  const send = async (helpful: boolean) => {
    setState("sending");
    try {
      await api.sendFeedback({ endpoint, query, helpful });
      setState("sent");
    } catch {
      setState("failed");
    }
  };

  if (state === "sent") {
    return <p className="hint feedback">Thanks — feedback recorded.</p>;
  }
  return (
    <p className="hint feedback">
      Were these results helpful?{" "}
      <button
        type="button"
        onClick={() => send(true)}
        disabled={state === "sending"}
        aria-label="Results were helpful"
      >
        👍
      </button>{" "}
      <button
        type="button"
        onClick={() => send(false)}
        disabled={state === "sending"}
        aria-label="Results were not helpful"
      >
        👎
      </button>
      {state === "failed" && " — couldn't record, try again"}
    </p>
  );
}

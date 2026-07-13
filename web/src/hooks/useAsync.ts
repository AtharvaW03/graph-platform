import { useCallback, useRef, useState } from "react";
import { ApiError } from "../api";

interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

// run(fn) executes fn, tracking loading/error/data. Pages call run() from a
// form submit handler rather than auto-fetching on mount, since every query
// here needs user input (a symbol, a repo name, a topic) first.
//
// Each run() invalidates any still-in-flight predecessor: a slow first
// request resolving after a quick second one must not overwrite the newer
// result (or clobber it with a stale error).
export function useAsync<T>() {
  const [state, setState] = useState<AsyncState<T>>({
    data: null,
    error: null,
    loading: false,
  });
  const seq = useRef(0);

  const run = useCallback(async (fn: () => Promise<T>) => {
    const id = ++seq.current;
    setState({ data: null, error: null, loading: true });
    try {
      const data = await fn();
      if (id !== seq.current) return;
      setState({ data, error: null, loading: false });
    } catch (err) {
      if (id !== seq.current) return;
      const message =
        err instanceof ApiError
          ? `${err.status}: ${err.message}`
          : err instanceof Error
            ? err.message
            : String(err);
      setState({ data: null, error: message, loading: false });
    }
  }, []);

  return { ...state, run };
}

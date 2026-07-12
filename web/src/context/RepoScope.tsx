import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { api } from "../api";
import type { RepoInfo } from "../types";

// Global repository scope: which repos the whole UI queries against. Empty
// selection means "all repos". Selection persists in localStorage so the
// scope survives reloads and page switches - matching the scope-selector
// convention of tools like Datadog/Grafana.
interface RepoScope {
  // available is the list of indexed repos (from GET /repos).
  available: RepoInfo[];
  loading: boolean;
  error: string | null;
  refresh: () => void;
  // selected repo names; empty = no scoping.
  selected: string[];
  setSelected: (repos: string[]) => void;
}

const Ctx = createContext<RepoScope>({
  available: [],
  loading: false,
  error: null,
  refresh: () => {},
  selected: [],
  setSelected: () => {},
});

// eslint-disable-next-line react-refresh/only-export-components
export function useRepoScope(): RepoScope {
  return useContext(Ctx);
}

const STORAGE_KEY = "repo-scope";

export function RepoScopeProvider({ children }: { children: ReactNode }) {
  const [available, setAvailable] = useState<RepoInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelectedState] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed)
        ? parsed.filter((x): x is string => typeof x === "string")
        : [];
    } catch {
      return [];
    }
  });

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    api
      .listRepos()
      .then((repos) => {
        setAvailable(repos);
        setLoading(false);
      })
      .catch((err: unknown) => {
        setAvailable([]);
        setError(err instanceof Error ? err.message : "Failed to load repos");
        setLoading(false);
      });
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const setSelected = (repos: string[]) => {
    setSelectedState(repos);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(repos));
  };

  return (
    <Ctx.Provider
      value={{ available, loading, error, refresh, selected, setSelected }}
    >
      {children}
    </Ctx.Provider>
  );
}

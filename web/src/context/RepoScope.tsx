import {
  createContext,
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
  // selected repo names; empty = no scoping.
  selected: string[];
  setSelected: (repos: string[]) => void;
}

const Ctx = createContext<RepoScope>({
  available: [],
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

  useEffect(() => {
    api
      .listRepos()
      .then(setAvailable)
      .catch(() => setAvailable([]));
  }, []);

  const setSelected = (repos: string[]) => {
    setSelectedState(repos);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(repos));
  };

  return (
    <Ctx.Provider value={{ available, selected, setSelected }}>
      {children}
    </Ctx.Provider>
  );
}

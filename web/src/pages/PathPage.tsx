import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { useRepoScope } from "../context/RepoScope";
import type { PathNode } from "../types";

export function PathPage() {
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected } = useRepoScope();
  const { data, error, loading, run } = useAsync<PathNode[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (source.trim() && target.trim()) {
      setRatedQuery(`${source.trim()} -> ${target.trim()}`);
      run(() => api.shortestPath(source.trim(), target.trim(), selected));
    }
  };

  return (
    <section>
      <h1>Shortest Path</h1>
      <p className="hint">
        One shortest undirected path between two symbols, up to 15 hops.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="source symbol"
          autoFocus
        />
        <input
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder="target symbol"
        />
        <button type="submit">Trace</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && <FeedbackWidget endpoint="path" query={ratedQuery} />}
      {data && data.length > 0 && (
        <ol className="path-trail">
          {data.map((node, i) => (
            <li key={`${node.repo}:${node.path}:${i}`}>
              {node.relationship && (
                <span className="rel-arrow">--[{node.relationship}]--&gt;</span>
              )}
              <strong>{node.name}</strong>
              <span className="dim">
                {" "}
                ({node.labels.filter((l) => l !== "Entity").join(", ")}) -{" "}
                {node.repo} - {node.path}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

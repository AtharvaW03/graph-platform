import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { DataTable, joinList } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { RepoPicker } from "../components/RepoPicker";
import { Badge, Button, Card, PageHeader, Stat } from "../components/ui";
import type { HTTPRoute } from "../types";

// Backed by the same /routes data Explore's "HTTP routes" kind uses, filtered
// and summarized for a security review: which of the org's HTTP surface is
// documented (has a committed OpenAPI spec) versus inferred from code, and
// how much of it is business-facing versus internal/infra. There's no
// separate backend endpoint for this - Source/Documented/Classification/Tags
// are fields FindRoutes already returns.
export function Security() {
  const [undocumentedOnly, setUndocumentedOnly] = useState(false);
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected, setSelected } = useRepoScope();
  const { data, error, loading, run } = useAsync<HTTPRoute[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery(selected.length > 0 ? selected.join(",") : "org-wide");
    run(() => api.findRoutes(undefined, undefined, selected));
  };

  const routes = data ?? [];
  const shown = undocumentedOnly ? routes.filter((r) => !r.documented) : routes;
  const documentedCount = routes.filter((r) => r.documented).length;
  const businessCount = routes.filter((r) => r.classification === "business").length;

  return (
    <>
      <PageHeader
        title="Security"
        description="API surface review: which HTTP routes are documented in a committed spec, and how they're classified."
      />

      <Card>
        <form onSubmit={onSubmit}>
          <div className="form-row">
            <RepoPicker label="Scope" value={selected} onChange={setSelected} hint="Empty = every indexed repo." />
          </div>
          <div className="form-row">
            <label className="field__label" style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}>
              <input
                type="checkbox"
                checked={undocumentedOnly}
                onChange={(e) => setUndocumentedOnly(e.target.checked)}
              />
              Show undocumented routes only
            </label>
          </div>
          <div className="form-actions">
            <Button type="submit" loading={loading}>
              {selected.length > 0 ? "Review scoped repos" : "Review org-wide"}
            </Button>
          </div>
        </form>
      </Card>

      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox
          loading={loading}
          error={error}
          empty={data?.length === 0}
          emptyText="No routes indexed yet - run the indexer first."
        />
        {data && <FeedbackWidget endpoint="security" query={ratedQuery} />}

        {data && data.length > 0 && (
          <>
            <div className="grid-stats">
              <Stat label="Total routes" value={routes.length} />
              <Stat label="Documented" value={documentedCount} />
              <Stat label="Undocumented" value={routes.length - documentedCount} />
              <Stat label="Business-classified" value={businessCount} />
            </div>

            {shown.length === 0 ? (
              <p className="dim">No undocumented routes in this scope.</p>
            ) : (
              <DataTable
                rows={shown}
                keyFn={(r, i) => `${r.repo}:${r.method}:${r.path}:${i}`}
                columns={[
                  { header: "Method", render: (r) => r.method },
                  { header: "Path", render: (r) => r.path },
                  { header: "Repo", render: (r) => r.repo },
                  {
                    header: "Documented",
                    render: (r) => (
                      <Badge tone={r.documented ? "success" : "danger"}>
                        {r.documented ? "yes" : "no"}
                      </Badge>
                    ),
                  },
                  {
                    header: "Classification",
                    render: (r) => (
                      <Badge tone={r.classification === "business" ? "brand" : "neutral"}>
                        {r.classification || "unclassified"}
                      </Badge>
                    ),
                  },
                  { header: "Source", render: (r) => r.source || "-" },
                  { header: "Tags", render: (r) => joinList(r.tags) },
                  { header: "File", render: (r) => r.file },
                ]}
              />
            )}
          </>
        )}
      </div>
    </>
  );
}

import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useRepoScope } from "../context/RepoScope";
import { Badge, Button, Card, Input, PageHeader, Stat } from "../components/ui";

export function Home() {
  const [q, setQ] = useState("");
  const navigate = useNavigate();
  const { available } = useRepoScope();

  const totalNodes = available.reduce((sum, r) => sum + r.nodes, 0);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    navigate(query ? `/search?q=${encodeURIComponent(query)}` : "/search");
  };

  return (
    <>
      <PageHeader
        title="graph-platform"
        description="A knowledge graph over the org's codebases - symbols, call edges, HTTP routes, Kafka topics, SQL objects, and cross-repo dependencies."
      />

      <div className="grid-stats">
        <Stat label="Nodes" value={totalNodes.toLocaleString()} />
        <Stat label="Repositories" value={available.length} />
      </div>

      <Card>
        <form onSubmit={onSubmit} className="quick-search">
          <Input
            label="Quick search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="e.g. ProcessPayment"
            autoFocus
          />
          <Button type="submit">Search</Button>
        </form>
      </Card>

      <div className="home-grid" style={{ marginTop: "var(--space-6)" }}>
        <PersonaCard
          to="/search"
          badge="Engineer"
          tone="brand"
          title="Search"
          description="Find any symbol, file, or route by name across every indexed repo."
        />
        <PersonaCard
          to="/impact"
          badge="Engineer"
          tone="brand"
          title="Impact"
          description="See what calls a function, what it calls, and what breaks if it changes."
        />
        <PersonaCard
          to="/security"
          badge="Security"
          tone="warning"
          title="Security"
          description="Review the HTTP surface: which routes are documented and which aren't."
        />
        <PersonaCard
          to="/explore"
          badge="Anyone"
          tone="info"
          title="Explore"
          description="Browse HTTP routes, Kafka topics, SQL objects, Glue jobs, and dependencies."
        />
      </div>
    </>
  );
}

function PersonaCard({
  to,
  badge,
  tone,
  title,
  description,
}: {
  to: string;
  badge: string;
  tone: "brand" | "warning" | "info";
  title: string;
  description: string;
}) {
  return (
    <Link to={to} className="persona-card">
      <Card>
        <div className="persona-card__tag">
          <Badge tone={tone}>{badge}</Badge>
        </div>
        <h3>{title}</h3>
        <p>{description}</p>
      </Card>
    </Link>
  );
}

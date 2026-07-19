import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useRepoScope } from "../context/RepoScope";
import { Button, Card } from "../components/ui";
import type { RepoInfo } from "../types";

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
      <section className="hero">
        <p className="eyebrow">A1 Knowledge Graph</p>
        <h1 className="hero__title">Ask the codebase.</h1>
        <p className="hero__sub">
          Every service, API, function, and connection across the org - mapped,
          searchable, and always current.
        </p>

        <form onSubmit={onSubmit} className="hero__ask">
          <input
            className="field__input hero__input"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search a function, endpoint, topic, or service…"
            aria-label="Search the codebase"
            autoFocus
          />
          <Button type="submit" size="lg">
            Search
          </Button>
        </form>
      </section>

      {available.length > 0 && <ServicesPanel repos={available} totalNodes={totalNodes} />}

      <section className="q-section">
        <h2 className="q-section__title">What do you want to know?</h2>
        <div className="q-grid">
          <QuestionCard
            to="/search"
            group="Find"
            title="Where is this defined?"
            description="Look up any function, file, endpoint, or topic by name."
          />
          <QuestionCard
            to="/repos"
            group="Understand"
            title="What does this service do?"
            description="A guided overview per service: its APIs, structure, and connections."
          />
          <QuestionCard
            to="/impact"
            group="Understand"
            title="What breaks if we change it?"
            description="See everything that depends on a piece of code before touching it."
          />
          <QuestionCard
            to="/security"
            group="Review"
            title="Which endpoints are undocumented?"
            description="The org's whole HTTP surface, checked against committed API specs."
          />
          <QuestionCard
            to="/explore"
            group="Find"
            title="Who talks to this topic or table?"
            description="Browse Kafka topics, SQL objects, Glue jobs, and dependencies."
          />
          <QuestionCard
            to="/hotspots"
            group="Review"
            title="What does everyone depend on?"
            description="The riskiest code to change, ranked by how much depends on it."
          />
        </div>
      </section>
    </>
  );
}

// ServicesPanel lists indexed services, largest first, each row with a
// relative size bar and a link to the service overview. Shows the top 20;
// the remainder is summarized as a count. The count text is the readable
// value; the bar is a visual aid hidden from assistive tech.
function ServicesPanel({ repos, totalNodes }: { repos: RepoInfo[]; totalNodes: number }) {
  const sorted = [...repos].sort((a, b) => b.nodes - a.nodes);
  const shown = sorted.slice(0, 20);
  const max = shown[0]?.nodes ?? 1;
  const rest = repos.length - shown.length;

  return (
    <Card as="section">
      <div className="panel__head">
        <h3>Services by size</h3>
        <p className="small">
          {repos.length} services · {totalNodes.toLocaleString()} code elements indexed
        </p>
      </div>
      <div className="svc-grid">
        {shown.map((r) => (
          <Link
            key={r.name}
            className="svc-row"
            to={`/repos?repo=${encodeURIComponent(r.name)}`}
            title={`${r.name} - open overview`}
          >
            <span className="svc-row__name mono">{r.name}</span>
            <span className="svc-row__bar" aria-hidden>
              <span
                className="svc-row__fill"
                style={{ width: `${Math.max(2, (r.nodes / max) * 100)}%` }}
              />
            </span>
            <span className="svc-row__count">{r.nodes.toLocaleString()}</span>
          </Link>
        ))}
      </div>
      {rest > 0 && (
        <Link to="/repos" className="small svc-more">
          +{rest} more services →
        </Link>
      )}
    </Card>
  );
}

function QuestionCard({
  to,
  group,
  title,
  description,
}: {
  to: string;
  group: string;
  title: string;
  description: string;
}) {
  return (
    <Link to={to} className="q-card">
      <Card>
        <p className="eyebrow">{group}</p>
        <h3>{title}</h3>
        <p>{description}</p>
      </Card>
    </Link>
  );
}

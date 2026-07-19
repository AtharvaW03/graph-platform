import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { useRepoScope } from "../context/RepoScope";
import { formatAge } from "../lib/time";
import { Button, Card } from "../components/ui";
import type { FreshnessRepo } from "../types";

export function Home() {
  const [q, setQ] = useState("");
  const navigate = useNavigate();
  const { available } = useRepoScope();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    navigate(query ? `/search?q=${encodeURIComponent(query)}` : "/search");
  };

  return (
    <>
      <section className="hero">
        <p className="eyebrow">A1 Knowledge Graph</p>
        <h1 className="hero__title">Search the codebase.</h1>

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

      <RecentPanel serviceCount={available.length} />

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

// RecentPanel lists the most recently indexed services with their age, so
// the panel reflects current activity at any repo count. Falls back to a
// count-and-link card when no freshness data exists yet.
function RecentPanel({ serviceCount }: { serviceCount: number }) {
  const [recent, setRecent] = useState<FreshnessRepo[]>([]);

  useEffect(() => {
    let cancelled = false;
    api
      .freshness()
      .then((f) => {
        if (cancelled) return;
        const rows = [...f.repositories]
          .filter((r) => r.last_indexed_at)
          .sort((a, b) => (b.last_indexed_at ?? "").localeCompare(a.last_indexed_at ?? ""));
        setRecent(rows.slice(0, 8));
      })
      .catch(() => {
        if (!cancelled) setRecent([]);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (serviceCount === 0) return null;

  return (
    <Card as="section">
      <div className="panel__head">
        <h3>Recently updated</h3>
        <Link to="/repos" className="small">
          All {serviceCount} services →
        </Link>
      </div>
      {recent.length === 0 ? (
        <p className="dim">
          {serviceCount} services indexed. Open{" "}
          <Link to="/repos">Services</Link> for the full list.
        </p>
      ) : (
        <ul className="recent-list">
          {recent.map((r) => (
            <li key={r.repo}>
              <Link className="recent-row" to={`/repos?repo=${encodeURIComponent(r.repo)}`}>
                <span className="mono recent-row__name">{r.repo}</span>
                <span className="recent-row__age">
                  indexed {formatAge(ageSeconds(r.last_indexed_at))} ago
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function ageSeconds(iso?: string): number {
  if (!iso) return 0;
  return Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
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

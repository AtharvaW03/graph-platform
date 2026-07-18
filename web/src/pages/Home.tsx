import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useRepoScope } from "../context/RepoScope";
import { Button, Card } from "../components/ui";
import { Constellation } from "../components/Constellation";

// Example searches for the ask box - concrete things people actually look
// up, so the empty input teaches by example instead of by instruction.
const EXAMPLES = ["payment", "order", "notification", "/v1/deposit"];

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
        <div className="hero__examples">
          <span className="small">Try:</span>
          {EXAMPLES.map((ex) => (
            <button
              key={ex}
              type="button"
              className="chip chip--action"
              onClick={() => navigate(`/search?q=${encodeURIComponent(ex)}`)}
            >
              {ex}
            </button>
          ))}
        </div>
      </section>

      {available.length > 0 && (
        <Card as="section" className="org-map">
          <div className="org-map__head">
            <h3>The org, mapped</h3>
            <p className="small">
              {available.length} services · {totalNodes.toLocaleString()} code
              elements. Each dot is a service - select one for its overview.
            </p>
          </div>
          <Constellation repos={available} />
        </Card>
      )}

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

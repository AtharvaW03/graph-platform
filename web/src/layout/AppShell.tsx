import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { api } from "../api";
import "./shell.css";

// Navigation grouped by task; group names match the page-header eyebrows.
const NAV_GROUPS: {
  group: string | null;
  items: { to: string; label: string; end?: boolean }[];
}[] = [
  { group: null, items: [{ to: "/", label: "Home", end: true }] },
  {
    group: "Find",
    items: [
      { to: "/search", label: "Search" },
      { to: "/explore", label: "Catalog" },
    ],
  },
  {
    group: "Understand",
    items: [
      { to: "/repos", label: "Services" },
      { to: "/impact", label: "Change impact" },
    ],
  },
  {
    group: "Review",
    items: [
      { to: "/security", label: "API surface" },
      { to: "/hotspots", label: "Risk hotspots" },
    ],
  },
];

export function AppShell() {
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const stored = localStorage.getItem("graph-platform-theme");
    if (stored === "dark" || stored === "light") return stored;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  });
  const [navOpen, setNavOpen] = useState(false);
  const [online, setOnline] = useState(false);
  const loc = useLocation();

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem("graph-platform-theme", theme);
  }, [theme]);

  useEffect(() => {
    setNavOpen(false);
  }, [loc.pathname]);

  // /ready is unauthenticated and pings Neo4j with a short server-side
  // timeout (see internal/api/server.go). /health would answer "ok" with the
  // database down, which is not what "Graph online" should mean.
  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      try {
        const h = await api.ready();
        if (!cancelled) setOnline(h.status === "ready");
      } catch {
        if (!cancelled) setOnline(false);
      }
    };
    tick();
    const id = setInterval(tick, 15000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return (
    <div className="shell">
      <a className="skip-link" href="#main">
        Skip to content
      </a>

      <aside
        id="sidebar"
        className={`shell__sidebar ${navOpen ? "is-open" : ""}`}
        aria-label="Primary"
      >
        <div className="shell__brand">
          <div className="shell__logo" aria-hidden>
            ◈
          </div>
          <div className="shell__brand-name">A1 Knowledge Graph</div>
        </div>

        <nav className="shell__nav" aria-label="Main">
          {NAV_GROUPS.map((g) => (
            <div className="shell__nav-group" key={g.group ?? "top"}>
              {g.group && <p className="shell__nav-label">{g.group}</p>}
              {g.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end ?? false}
                  className={({ isActive }) =>
                    `shell__nav-link ${isActive ? "is-active" : ""}`
                  }
                >
                  {item.label}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="shell__sidebar-foot">
          <div className="shell__status" role="status" aria-live="polite">
            <span
              className={`shell__dot ${online ? "is-ok" : "is-bad"}`}
              aria-hidden
            />
            <span>{online ? "Graph online" : "Graph offline"}</span>
          </div>
        </div>
      </aside>

      {navOpen && (
        <button
          type="button"
          className="shell__backdrop"
          aria-label="Close menu"
          onClick={() => setNavOpen(false)}
        />
      )}

      <div className="shell__main">
        <header className="shell__top">
          <button
            type="button"
            className="shell__menu-btn btn btn--ghost btn--sm"
            aria-expanded={navOpen}
            aria-controls="sidebar"
            onClick={() => setNavOpen((v) => !v)}
          >
            Menu
          </button>
          <div className="shell__top-spacer" />
          <button
            type="button"
            className="btn btn--ghost btn--sm"
            onClick={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
            aria-label={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"}
          >
            {theme === "dark" ? "Light" : "Dark"}
          </button>
        </header>

        <main id="main" className="shell__content" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}

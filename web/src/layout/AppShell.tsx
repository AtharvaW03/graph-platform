import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { api } from "../api";
import "./shell.css";

const NAV = [
  { to: "/", label: "Home", end: true },
  { to: "/search", label: "Search" },
  { to: "/explore", label: "Explore" },
  { to: "/impact", label: "Impact" },
  { to: "/hotspots", label: "Hotspots" },
  { to: "/security", label: "Security" },
  { to: "/repos", label: "Repos" },
] as const;

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

  // /health is unauthenticated and cheap (no Neo4j round trip - see
  // internal/api/server.go), so polling it is safe to run continuously.
  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      try {
        const h = await api.health();
        if (!cancelled) setOnline(h.status === "ok");
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

      <aside className={`shell__sidebar ${navOpen ? "is-open" : ""}`} aria-label="Primary">
        <div className="shell__brand">
          <div className="shell__logo" aria-hidden>
            ◈
          </div>
          <div className="shell__brand-name">graph-platform</div>
        </div>

        <nav className="shell__nav" aria-label="Main">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={"end" in item ? item.end : false}
              className={({ isActive }) =>
                `shell__nav-link ${isActive ? "is-active" : ""}`
              }
            >
              {item.label}
            </NavLink>
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

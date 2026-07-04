import { NavLink, Route, Routes, Navigate } from "react-router-dom";
import { SearchPage } from "./pages/SearchPage";
import { SymbolPage } from "./pages/SymbolPage";
import { PathPage } from "./pages/PathPage";
import { OverviewPage } from "./pages/OverviewPage";
import { DependenciesPage } from "./pages/DependenciesPage";
import { RoutesPage } from "./pages/RoutesPage";
import { KafkaPage } from "./pages/KafkaPage";
import { SqlPage } from "./pages/SqlPage";
import { GluePage } from "./pages/GluePage";
import { HotspotsPage } from "./pages/HotspotsPage";
import { RepoScopeProvider } from "./context/RepoScope";

// Navigation grouped by what the entities are: "Code" pages query repo-owned
// symbols (and support the repo scope picker); "Platform" pages query
// org-global inventories or pick their own single repo.
const navGroups = [
  {
    title: "Code",
    items: [
      { to: "/overview", label: "Repository Overview" },
      { to: "/search", label: "Search" },
      { to: "/symbol", label: "Symbol Explorer" },
      { to: "/path", label: "Shortest Path" },
      { to: "/hotspots", label: "Hotspots" },
    ],
  },
  {
    title: "Platform",
    items: [
      { to: "/dependencies", label: "Dependencies" },
      { to: "/routes", label: "HTTP Routes" },
      { to: "/kafka", label: "Kafka Topics" },
      { to: "/sql", label: "SQL Objects" },
      { to: "/glue", label: "Glue Jobs" },
    ],
  },
];

export default function App() {
  return (
    <RepoScopeProvider>
      <div className="layout">
        <nav className="sidebar" aria-label="Main navigation">
          <div className="brand">graph-platform</div>
          {navGroups.map((g) => (
            <div key={g.title} className="nav-section">
              <div className="nav-group">{g.title}</div>
              {g.items.map((n) => (
                <NavLink
                  key={n.to}
                  to={n.to}
                  className={({ isActive }) =>
                    isActive ? "nav-link active" : "nav-link"
                  }
                >
                  {n.label}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <main className="content">
          <Routes>
            <Route path="/" element={<Navigate to="/overview" replace />} />
            <Route path="/overview" element={<OverviewPage />} />
            <Route path="/search" element={<SearchPage />} />
            <Route path="/symbol" element={<SymbolPage />} />
            <Route path="/path" element={<PathPage />} />
            <Route path="/dependencies" element={<DependenciesPage />} />
            <Route path="/routes" element={<RoutesPage />} />
            <Route path="/kafka" element={<KafkaPage />} />
            <Route path="/sql" element={<SqlPage />} />
            <Route path="/glue" element={<GluePage />} />
            <Route path="/hotspots" element={<HotspotsPage />} />
          </Routes>
        </main>
      </div>
    </RepoScopeProvider>
  );
}

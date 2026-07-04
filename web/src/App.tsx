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
import { ScopeBar } from "./components/ScopeBar";

const nav = [
  { to: "/overview", label: "Repository Overview" },
  { to: "/search", label: "Search" },
  { to: "/symbol", label: "Symbol Explorer" },
  { to: "/path", label: "Shortest Path" },
  { to: "/dependencies", label: "Dependencies" },
  { to: "/routes", label: "HTTP Routes" },
  { to: "/kafka", label: "Kafka Topics" },
  { to: "/sql", label: "SQL Objects" },
  { to: "/glue", label: "Glue Jobs" },
  { to: "/hotspots", label: "Hotspots" },
];

export default function App() {
  return (
    <RepoScopeProvider>
      <div className="layout">
        <nav className="sidebar">
          <div className="brand">graph-platform</div>
          {nav.map((n) => (
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
        </nav>
        <main className="content">
          <ScopeBar />
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

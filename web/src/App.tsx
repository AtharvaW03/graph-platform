import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "./layout/AppShell";
import { RepoScopeProvider } from "./context/RepoScope";
import { Home } from "./pages/Home";
import { Search } from "./pages/Search";
import { Explore } from "./pages/Explore";
import { Impact } from "./pages/Impact";
import { Hotspots } from "./pages/Hotspots";
import { Security } from "./pages/Security";
import { Repos } from "./pages/Repos";

export default function App() {
  return (
    <RepoScopeProvider>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Home />} />
          <Route path="/search" element={<Search />} />
          <Route path="/explore" element={<Explore />} />
          <Route path="/impact" element={<Impact />} />
          <Route path="/hotspots" element={<Hotspots />} />
          <Route path="/security" element={<Security />} />
          <Route path="/repos" element={<Repos />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </RepoScopeProvider>
  );
}

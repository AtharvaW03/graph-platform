import { useNavigate } from "react-router-dom";
import type { RepoInfo } from "../types";
import "./Constellation.css";

// Constellation draws the org as a small map: every indexed repository is a
// node sized by how much code it contains, connected to a central hub. It is
// real data rendered as a picture - the same fact as "841,230 code elements"
// but legible to someone who has never heard the word "node". Clicking a
// repo opens its service overview.
//
// Layout is a deterministic golden-angle spiral with name-hash jitter, so
// the map is stable across reloads (same repos = same picture) without any
// physics simulation.

const W = 520;
const H = 300;
const CX = W / 2;
const CY = H / 2;

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// Density thresholds: the map degrades gracefully instead of drowning.
// Above LABELS_MAX, names appear on hover/focus only; above EDGES_MAX the
// hub spokes go too (hundreds of spokes read as a gray disk, not a map);
// above DOTS_MAX only the largest repos are drawn and the caller is told
// how many more exist (see overflow()).
const LABELS_MAX = 24;
const EDGES_MAX = 80;
const DOTS_MAX = 300;

interface Placed {
  repo: RepoInfo;
  x: number;
  y: number;
  r: number;
  angle: number;
}

function layout(repos: RepoInfo[]): Placed[] {
  if (repos.length === 0) return [];
  const maxNodes = Math.max(...repos.map((r) => r.nodes), 1);
  // Largest repos closest to the hub: order by size descending so the
  // spiral radius encodes something true (bigger = more central). The same
  // ordering makes the DOTS_MAX cut keep the most significant repos.
  const ordered = [...repos].sort((a, b) => b.nodes - a.nodes).slice(0, DOTS_MAX);
  const golden = 137.508 * (Math.PI / 180);
  return ordered.map((repo, i) => {
    const jitter = ((hash(repo.name) % 1000) / 1000 - 0.5) * 0.5;
    const angle = i * golden + jitter;
    const spread = Math.sqrt((i + 1.6) / (ordered.length + 1.6));
    // Horizontal margin leaves room for radial labels, which extend outward
    // from the extreme dots and would otherwise clip at the viewBox edge.
    const labelRoom = ordered.length <= LABELS_MAX ? 96 : 46;
    const rx = spread * (W / 2 - labelRoom);
    const ry = spread * (H / 2 - 36);
    const r = 4 + 9 * Math.sqrt(repo.nodes / maxNodes);
    return {
      repo,
      x: CX + Math.cos(angle) * rx,
      y: CY + Math.sin(angle) * ry,
      r,
      angle,
    };
  });
}

// overflow reports how many repos the map is NOT drawing, so the caption
// can say "+N more" instead of pretending the picture is complete.
export function constellationOverflow(count: number): number {
  return Math.max(0, count - DOTS_MAX);
}

// radialLabel places a name outward along its own spoke - away from the
// hub, where by construction only this repo's empty direction lies - so
// labels stop crossing other repos' edges.
function radialLabel(p: Placed): { x: number; y: number; anchor: "start" | "middle" | "end" } {
  const cos = Math.cos(p.angle);
  const sin = Math.sin(p.angle);
  const gap = p.r + 5;
  const x = p.x + cos * gap;
  const y = p.y + sin * gap + (sin > 0.35 ? 8 : sin < -0.35 ? -3 : 3);
  const anchor = cos > 0.35 ? "start" : cos < -0.35 ? "end" : "middle";
  return { x, y, anchor };
}

export function Constellation({ repos }: { repos: RepoInfo[] }) {
  const navigate = useNavigate();
  const placed = layout(repos);
  if (placed.length === 0) return null;
  const showLabels = placed.length <= LABELS_MAX;
  const showEdges = placed.length <= EDGES_MAX;

  return (
    <svg
      className="constellation"
      viewBox={`0 0 ${W} ${H}`}
      role="group"
      aria-label={`Map of ${repos.length} indexed repositories. Each dot is a repository sized by how much code it contains; select one to open its overview.`}
    >
      {showEdges &&
        placed.map((p) => (
          <line
            key={`e-${p.repo.name}`}
            className="constellation__edge"
            x1={CX}
            y1={CY}
            x2={p.x}
            y2={p.y}
          />
        ))}
      {showEdges && <circle className="constellation__hub" cx={CX} cy={CY} r={3.5} />}
      {placed.map((p) => {
        const label = radialLabel(p);
        return (
          <g
            key={p.repo.name}
            className="constellation__node"
            tabIndex={0}
            role="link"
            aria-label={`${p.repo.name}: ${p.repo.nodes.toLocaleString()} code elements. Open overview.`}
            onClick={() => navigate(`/repos?repo=${encodeURIComponent(p.repo.name)}`)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                navigate(`/repos?repo=${encodeURIComponent(p.repo.name)}`);
              }
            }}
          >
            <circle className="constellation__dot" cx={p.x} cy={p.y} r={p.r} />
            <text
              className={`constellation__label ${showLabels ? "" : "constellation__label--hover"}`}
              x={label.x}
              y={label.y}
              textAnchor={label.anchor}
            >
              {p.repo.name.length > 26 ? p.repo.name.slice(0, 25) + "…" : p.repo.name}
            </text>
            <title>
              {p.repo.name} - {p.repo.nodes.toLocaleString()} code elements
            </title>
          </g>
        );
      })}
    </svg>
  );
}

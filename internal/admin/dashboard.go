package admin

import (
	"net/http"
)

// dashboard serves the operator console. It renders as a static shell and
// fetches every screen from /admin/api/* on load, so the same data backs
// both the UI and any scripted access, with no server-side templating of
// untrusted values.
//
// Structure follows what an operator actually comes here to do: a health
// answer and the pause control (the routine visit), then per-screen
// lookups for repositories and access (the prompted visit - "X says their
// repo is missing", "Y is leaving"), then the audit trail. Lists are
// searchable because all three of them grow without bound.
func (h *Handler) dashboard(w http.ResponseWriter, _ *http.Request, _ string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>A1 Knowledge Graph - admin</title>
<style>
  :root {
    color-scheme: light dark;
    --bg: #fbfbfa; --panel: #fff; --ink: #16181d; --muted: #6b7280;
    --line: #e5e7eb; --accent: #2563eb; --warn: #b45309; --bad: #b91c1c; --good: #15803d;
    --hover: #f3f4f6;
  }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #0f1115; --panel: #171a21; --ink: #e6e8ec; --muted: #9aa3b2;
            --line: #262b35; --accent: #60a5fa; --warn: #d97706; --bad: #ef4444; --good: #4ade80;
            --hover: #1e222b; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--ink);
         font-family: system-ui, -apple-system, "Segoe UI", sans-serif; line-height: 1.45; }

  header { padding: .9rem 1.5rem; border-bottom: 1px solid var(--line); }
  .titlebar { display: flex; align-items: center; gap: 1rem; flex-wrap: wrap; }
  h1 { font-size: 1.05rem; margin: 0; }
  .muted { color: var(--muted); font-size: .85rem; }
  .grow { margin-left: auto; }

  /* Health strip: states the answer instead of making you derive it. */
  .health { display: flex; align-items: center; gap: .6rem; margin-top: .7rem; flex-wrap: wrap; font-size: .9rem; }
  .dot { width: .6rem; height: .6rem; border-radius: 50%; display: inline-block; flex: none; }
  .dot.good { background: var(--good); } .dot.warn { background: var(--warn); } .dot.bad { background: var(--bad); }

  nav { display: flex; gap: .25rem; padding: 0 1.5rem; border-bottom: 1px solid var(--line); flex-wrap: wrap; }
  nav button { font: inherit; background: none; border: none; border-bottom: 2px solid transparent;
               padding: .6rem .8rem; cursor: pointer; color: var(--muted); }
  nav button:hover { color: var(--ink); }
  nav button.active { color: var(--ink); border-bottom-color: var(--accent); font-weight: 600; }

  main { padding: 1.25rem 1.5rem 3rem; max-width: 78rem; }
  .screen { display: none; }
  .screen.active { display: block; }
  .cards { display: grid; gap: 1.1rem; grid-template-columns: repeat(auto-fit, minmax(20rem, 1fr)); }
  section { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 1rem 1.1rem; }
  section h2 { font-size: .78rem; text-transform: uppercase; letter-spacing: .07em;
               color: var(--muted); margin: 0 0 .8rem; font-weight: 600; }

  table { width: 100%; border-collapse: collapse; font-size: .9rem; }
  th { text-align: left; font-size: .75rem; text-transform: uppercase; letter-spacing: .05em;
       color: var(--muted); font-weight: 600; padding: .3rem .5rem .3rem 0; cursor: pointer; user-select: none;
       border-bottom: 1px solid var(--line); white-space: nowrap; }
  th.n, td.n { text-align: right; }
  th:hover { color: var(--ink); }
  td { padding: .4rem .5rem .4rem 0; vertical-align: middle; border-bottom: 1px solid var(--line); }
  tr:last-child td { border-bottom: none; }
  tbody tr.clickable { cursor: pointer; }
  tbody tr.clickable:hover { background: var(--hover); }
  td.n { font-variant-numeric: tabular-nums; color: var(--muted); white-space: nowrap; }

  .big { font-size: 1.9rem; font-weight: 600; font-variant-numeric: tabular-nums; }
  .row { display: flex; gap: 1.75rem; flex-wrap: wrap; }
  .pill { display: inline-block; padding: .12rem .55rem; border-radius: 999px; font-size: .75rem;
          border: 1px solid var(--line); white-space: nowrap; }
  .pill.good { color: var(--good); border-color: currentColor; }
  .pill.warn { color: var(--warn); border-color: currentColor; }
  .pill.bad  { color: var(--bad);  border-color: currentColor; }
  .pill.quiet { color: var(--muted); }

  button.act { font: inherit; padding: .35rem .8rem; border-radius: 6px; cursor: pointer;
               border: 1px solid var(--line); background: transparent; color: inherit; }
  button.primary { border-color: var(--accent); color: var(--accent); }
  button.danger { border-color: var(--bad); color: var(--bad); }
  input, select { font: inherit; padding: .35rem .55rem; border-radius: 6px;
                  border: 1px solid var(--line); background: var(--panel); color: inherit; }
  .controls { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; }
  .toolbar { display: flex; gap: .6rem; align-items: center; margin-bottom: .9rem; flex-wrap: wrap; }
  .toolbar input[type=search] { min-width: 14rem; flex: 1; max-width: 26rem; }

  .bar { height: 5px; background: var(--accent); border-radius: 3px; opacity: .7; margin-top: .25rem; }
  code { font-family: ui-monospace, Consolas, monospace; font-size: .85em; }
  .empty { color: var(--muted); font-size: .875rem; padding: .9rem 0; }
  .empty b { color: var(--ink); }
  .spark { display: flex; align-items: flex-end; gap: 2px; height: 46px; margin-top: .5rem; }
  .spark div { flex: 1; background: var(--accent); opacity: .65; border-radius: 2px 2px 0 0; min-height: 2px; }
  .spark-axis { display: flex; justify-content: space-between; font-size: .72rem; color: var(--muted); margin-top: .2rem; }
  .note { font-size: .8rem; color: var(--warn); margin-top: .2rem; }
  .summary-strip { display: flex; gap: 1.25rem; margin-bottom: .8rem; font-size: .85rem; color: var(--muted); }
  .summary-strip b { color: var(--ink); font-variant-numeric: tabular-nums; }

  /* Drill-down drawer */
  .drawer-bg { position: fixed; inset: 0; background: rgba(0,0,0,.35); display: none; }
  .drawer-bg.open { display: block; }
  .drawer { position: fixed; top: 0; right: 0; height: 100%; width: min(30rem, 100%);
            background: var(--panel); border-left: 1px solid var(--line); padding: 1.2rem 1.3rem;
            overflow-y: auto; transform: translateX(100%); transition: transform .15s ease; }
  .drawer.open { transform: translateX(0); }
  .drawer h3 { margin: 0 0 .2rem; font-size: 1rem; word-break: break-all; }
  .drawer .close { position: absolute; top: .8rem; right: 1rem; }
  .kv { display: grid; grid-template-columns: auto 1fr; gap: .3rem 1rem; font-size: .9rem; margin: .9rem 0; }
  .kv dt { color: var(--muted); } .kv dd { margin: 0; }
</style>
</head>
<body>
<header>
  <div class="titlebar">
    <h1>A1 Knowledge Graph - admin</h1>
    <span class="muted" id="who"></span>
    <span class="grow controls">
      <span class="muted" id="updated"></span>
      <button class="act" id="refreshBtn">Refresh</button>
    </span>
  </div>
  <div class="health" id="health"><span class="muted">loading...</span></div>
</header>

<nav>
  <button data-screen="overview" class="active">Overview</button>
  <button data-screen="repos">Repositories</button>
  <button data-screen="access">Access</button>
  <button data-screen="activity">Activity</button>
</nav>

<main>
  <!-- OVERVIEW: the routine visit. Health is answered above; this screen
       carries the one routine action and the scale numbers. -->
  <div class="screen active" id="screen-overview">
    <div class="cards">
      <section>
        <h2>Indexing</h2>
        <div id="indexing"><div class="empty">loading...</div></div>
        <div class="controls" style="margin-top:.7rem">
          <select id="pauseFor">
            <option value="0">indefinitely</option>
            <option value="60">for 1 hour</option>
            <option value="180">for 3 hours</option>
            <option value="480">for 8 hours</option>
          </select>
          <input id="pauseReason" placeholder="reason (optional)" style="flex:1;min-width:8rem">
          <button class="act danger" id="pauseBtn">Pause</button>
          <button class="act primary" id="resumeBtn">Resume</button>
        </div>
      </section>

      <section>
        <h2>Graph</h2>
        <div class="row">
          <div><div class="big" id="totalNodes">-</div><div class="muted">code elements</div></div>
          <div><div class="big" id="repoCount">-</div><div class="muted">repositories</div></div>
        </div>
      </section>

      <section>
        <h2>Usage <span class="muted" id="usageWindow"></span></h2>
        <div class="row" style="align-items:baseline">
          <div><div class="big" id="totalReq">-</div><div class="muted">requests</div></div>
          <select id="usageDays" class="grow">
            <option value="7">7 days</option>
            <option value="30" selected>30 days</option>
            <option value="90">90 days</option>
          </select>
        </div>
        <div class="spark" id="spark"></div>
        <div class="spark-axis"><span id="sparkFrom"></span><span id="sparkPeak"></span><span id="sparkTo"></span></div>
      </section>

      <section>
        <h2>Most-used features</h2>
        <table><tbody id="topEndpoints"></tbody></table>
      </section>
    </div>
  </div>

  <!-- REPOSITORIES: "X says their repo isn't showing up" -->
  <div class="screen" id="screen-repos">
    <div class="toolbar">
      <input type="search" id="repoSearch" placeholder="Search repositories...">
      <span class="muted" id="repoCountLabel"></span>
    </div>
    <section>
      <table>
        <thead><tr>
          <th data-sort="name">Repository</th>
          <th data-sort="nodes" class="n">Elements</th>
          <th data-sort="age" class="n">Last indexed</th>
          <th data-sort="requests" class="n">Requests</th>
        </tr></thead>
        <tbody id="repoRows"></tbody>
      </table>
      <div id="repoEmpty"></div>
    </section>
  </div>

  <!-- ACCESS: "Y is leaving - kill their access" -->
  <div class="screen" id="screen-access">
    <div class="toolbar">
      <input type="search" id="keySearch" placeholder="Search by person...">
      <label class="muted"><input type="checkbox" id="activeOnly" checked> Active only</label>
    </div>
    <section>
      <div class="summary-strip" id="keysSummary"></div>
      <table>
        <thead><tr>
          <th data-ksort="owner">Person</th>
          <th data-ksort="state">Status</th>
          <th data-ksort="used" class="n">Last used</th>
          <th class="n"></th>
        </tr></thead>
        <tbody id="keyRows"></tbody>
      </table>
      <div id="keysEmpty"></div>
    </section>
  </div>

  <!-- ACTIVITY: "what changed / who did that" -->
  <div class="screen" id="screen-activity">
    <div class="toolbar">
      <input type="search" id="auditSearch" placeholder="Search actions, people, details...">
      <span class="muted" id="auditCountLabel"></span>
    </div>
    <section>
      <table>
        <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Detail</th></tr></thead>
        <tbody id="auditRows"></tbody>
      </table>
      <div id="auditEmpty"></div>
    </section>
  </div>
</main>

<div class="drawer-bg" id="drawerBg"></div>
<aside class="drawer" id="drawer" aria-live="polite">
  <button class="act close" id="drawerClose">Close</button>
  <div id="drawerBody"></div>
</aside>

<script>
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? "" : s).replace(/[&<>"']/g, c =>
  ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));

// Display names for identities that aren't a person. Stored values stay
// stable (they're database keys and audit content); only the label differs.
function actorLabel(a) {
  if (a === "internal") return "Shared / API access";
  if (a === "token-admin") return "Admin token";
  return a;
}

async function api(path, opts) {
  const res = await fetch(path, Object.assign({headers: {"Accept": "application/json"}}, opts || {}));
  if (!res.ok) throw new Error(path + " -> " + res.status);
  return res.json();
}

function ago(sec) {
  if (sec == null) return "-";
  if (sec < 60) return sec + "s ago";
  if (sec < 3600) return Math.round(sec / 60) + "m ago";
  if (sec < 86400) return Math.round(sec / 3600) + "h ago";
  return Math.round(sec / 86400) + "d ago";
}

// ---- state, held so screens can re-render on search/sort without refetching
let S = { repos: [], freshness: {}, repoReq: {}, keys: [], audit: [], anomalies: {}, indexing: {} };
let repoSort = {key: "name", dir: 1}, keySort = {key: "owner", dir: 1};

// ---- navigation
document.querySelectorAll("nav button").forEach(b => b.addEventListener("click", () => {
  document.querySelectorAll("nav button").forEach(x => x.classList.toggle("active", x === b));
  document.querySelectorAll(".screen").forEach(s =>
    s.classList.toggle("active", s.id === "screen-" + b.dataset.screen));
}));

// ---- health strip: says the answer rather than listing numbers
function renderHealth() {
  const st = S.indexing || {};
  const stale = (S.repos || []).filter(r => (S.freshness[r.name] || {}).stale).length;
  const parts = [];
  if (st.paused) {
    parts.push({tone: "warn", text: "Indexing paused" + (st.actor ? " by " + actorLabel(st.actor) : "")});
  } else {
    parts.push({tone: "good", text: "Indexing running"});
  }
  if (stale > 0) parts.push({tone: "warn", text: stale + " repositor" + (stale === 1 ? "y" : "ies") + " stale"});
  const expiredSoon = (S.keys || []).filter(k => !k.revoked_at && new Date(k.expires_at) > new Date()).length;
  if (expiredSoon) parts.push({tone: "good", text: expiredSoon + " active key" + (expiredSoon === 1 ? "" : "s")});

  const allGood = !st.paused && stale === 0;
  $("health").innerHTML =
    (allGood ? '<span class="dot good"></span><b>Everything looks healthy.</b> ' : '') +
    parts.map(p => '<span><span class="dot ' + p.tone + '"></span>' + esc(p.text) + '</span>').join('<span class="muted">·</span>');
}

// ---- overview
async function loadOverview() {
  const d = await api("/admin/api/overview");
  S.repos = d.repos || [];
  S.indexing = d.indexing || {};
  S.freshness = {};
  (d.freshness || []).forEach(f => S.freshness[f.repo] = f);

  $("totalNodes").textContent = (d.total_nodes || 0).toLocaleString();
  $("repoCount").textContent = S.repos.length;

  const st = S.indexing;
  $("indexing").innerHTML =
    (st.paused ? '<span class="pill bad">paused</span>' : '<span class="pill good">running</span>') +
    (st.paused
      ? '<div class="muted" style="margin-top:.4rem">by ' + esc(actorLabel(st.actor || "?")) +
        (st.paused_until ? ', until ' + esc(new Date(st.paused_until).toLocaleString()) : ' (indefinitely)') +
        (st.reason ? '<br>reason: ' + esc(st.reason) : '') + '</div>'
      : '<div class="muted" style="margin-top:.4rem">Running on schedule. Pausing stops work at the next repository boundary, never mid-import.</div>');

  $("updated").textContent = "updated " + new Date().toLocaleTimeString();
}

async function loadUsage() {
  const days = parseInt($("usageDays").value, 10) || 30;
  const d = await api("/admin/api/usage?days=" + days);
  $("usageWindow").textContent = "last " + d.days + " days";
  $("totalReq").textContent = (d.total_requests || 0).toLocaleString();

  S.repoReq = {};
  (d.top_repos || []).forEach(r => S.repoReq[r.name] = r.count);

  const eps = d.top_endpoints || [];
  const emax = Math.max(1, ...eps.map(i => i.count));
  $("topEndpoints").innerHTML = eps.length
    ? eps.map(i => '<tr><td>' + esc(i.name) + '<div class="bar" style="width:' +
        Math.round(i.count / emax * 100) + '%"></div></td><td class="n">' + i.count + '</td></tr>').join("")
    : '<tr><td class="empty">No queries recorded yet.</td></tr>';

  const t = d.traffic || [];
  const tmax = Math.max(1, ...t.map(x => x.count));
  $("spark").innerHTML = t.map(x =>
    '<div style="height:' + Math.max(2, Math.round(x.count / tmax * 46)) + 'px" title="' +
    esc(x.day) + ': ' + x.count + ' requests"></div>').join("");
  $("sparkFrom").textContent = t.length ? t[0].day : "";
  $("sparkPeak").textContent = t.length ? "peak " + tmax + "/day" : "";
  $("sparkTo").textContent = t.length ? t[t.length - 1].day : "";

  renderRepos(); // request counts feed the repositories table
}

// ---- repositories screen
function renderRepos() {
  const q = $("repoSearch").value.trim().toLowerCase();
  let list = S.repos.filter(r => !q || r.name.toLowerCase().includes(q));

  const val = r => ({
    name: r.name.toLowerCase(),
    nodes: r.nodes,
    age: (S.freshness[r.name] || {}).age_seconds ?? -1,
    requests: S.repoReq[r.name] || 0
  })[repoSort.key];
  list.sort((a, b) => {
    const x = val(a), y = val(b);
    return (x < y ? -1 : x > y ? 1 : 0) * repoSort.dir;
  });

  $("repoCountLabel").textContent = list.length + " of " + S.repos.length;
  $("repoRows").innerHTML = list.map(r => {
    const f = S.freshness[r.name];
    const pill = f
      ? '<span class="pill ' + (f.stale ? "warn" : "good") + '">' + ago(f.age_seconds) + '</span>'
      : '<span class="pill quiet">never</span>';
    return '<tr class="clickable" data-repo="' + esc(r.name) + '"><td>' + esc(r.name) + '</td>' +
      '<td class="n">' + r.nodes.toLocaleString() + '</td>' +
      '<td class="n">' + pill + '</td>' +
      '<td class="n">' + (S.repoReq[r.name] || 0).toLocaleString() + '</td></tr>';
  }).join("");
  $("repoEmpty").innerHTML = list.length ? "" :
    (S.repos.length
      ? '<div class="empty">No repository matches <b>' + esc($("repoSearch").value) + '</b>.</div>'
      : '<div class="empty">No repositories indexed yet. Run the indexer to populate the graph.</div>');
}

$("repoSearch").addEventListener("input", renderRepos);
document.querySelectorAll("th[data-sort]").forEach(th => th.addEventListener("click", () => {
  const k = th.dataset.sort;
  repoSort = {key: k, dir: repoSort.key === k ? -repoSort.dir : 1};
  renderRepos();
}));
$("repoRows").addEventListener("click", e => {
  const tr = e.target.closest("tr[data-repo]");
  if (tr) openRepo(tr.dataset.repo);
});

// ---- access screen
function keyState(k) {
  if (k.revoked_at) return "revoked";
  return new Date(k.expires_at).getTime() < Date.now() ? "expired" : "active";
}

function renderKeys() {
  const q = $("keySearch").value.trim().toLowerCase();
  const activeOnly = $("activeOnly").checked;
  let list = (S.keys || []).filter(k =>
    (!q || k.owner.toLowerCase().includes(q)) && (!activeOnly || keyState(k) === "active"));

  const val = k => ({
    owner: k.owner.toLowerCase(),
    state: keyState(k),
    used: k.last_used_at ? new Date(k.last_used_at).getTime() : 0
  })[keySort.key];
  list.sort((a, b) => {
    const x = val(a), y = val(b);
    return (x < y ? -1 : x > y ? 1 : 0) * keySort.dir;
  });

  let active = 0, expired = 0, revoked = 0, neverUsed = 0;
  (S.keys || []).forEach(k => {
    const s = keyState(k);
    if (s === "active") active++; else if (s === "expired") expired++; else revoked++;
    if (!k.last_used_at && s === "active") neverUsed++;
  });
  $("keysSummary").innerHTML =
    '<span><b>' + active + '</b> active</span><span><b>' + expired + '</b> expired</span>' +
    '<span><b>' + revoked + '</b> revoked</span>' +
    (neverUsed ? '<span><b>' + neverUsed + '</b> never used</span>' : '');

  $("keyRows").innerHTML = list.map(k => {
    const s = keyState(k);
    const pill = s === "active" ? '<span class="pill good">active</span>'
               : s === "expired" ? '<span class="pill warn">expired</span>'
               : '<span class="pill quiet">revoked</span>';
    const note = S.anomalies[k.owner]
      ? '<div class="note">Used ' + S.anomalies[k.owner].repo_count + ' repositories recently - open to review.</div>' : '';
    return '<tr class="clickable" data-actor="' + esc(k.owner) + '">' +
      '<td>' + esc(k.owner) + note + '</td><td>' + pill + '</td>' +
      '<td class="n">' + (k.last_used_at ? new Date(k.last_used_at).toLocaleDateString() : "never") + '</td>' +
      '<td class="n">' + (s === "active"
        ? '<button class="act danger" data-action="revoke" data-owner="' + esc(k.owner) + '">Revoke</button>' : '') +
      '</td></tr>';
  }).join("");
  $("keysEmpty").innerHTML = list.length ? "" :
    ((S.keys || []).length
      ? '<div class="empty">No key matches the current filter.</div>'
      : '<div class="empty">No keys yet. Engineers mint their own at <b>/portal</b> after signing in.</div>');
}

$("keySearch").addEventListener("input", renderKeys);
$("activeOnly").addEventListener("change", renderKeys);
document.querySelectorAll("th[data-ksort]").forEach(th => th.addEventListener("click", () => {
  const k = th.dataset.ksort;
  keySort = {key: k, dir: keySort.key === k ? -keySort.dir : 1};
  renderKeys();
}));

// Event delegation reading data-* through dataset: the value is never
// re-parsed as JS or HTML source, so it is safe regardless of content -
// unlike an inline onclick, where the browser HTML-decodes the attribute
// before treating it as source.
$("keyRows").addEventListener("click", e => {
  const btn = e.target.closest("button[data-action='revoke']");
  if (btn) { e.stopPropagation(); revoke(btn.dataset.owner); return; }
  const tr = e.target.closest("tr[data-actor]");
  if (tr) openActor(tr.dataset.actor);
});

async function loadKeys() {
  const d = await api("/admin/api/keys");
  S.keys = d.keys || [];
  renderKeys();
}

async function loadAnomalies() {
  try {
    const d = await api("/admin/api/anomalies");
    S.anomalies = {};
    (d.anomalies || []).forEach(a => S.anomalies[a.actor] = a);
  } catch (e) { S.anomalies = {}; }
}

// ---- activity screen
function renderAudit() {
  const q = $("auditSearch").value.trim().toLowerCase();
  const list = (S.audit || []).filter(a => !q ||
    (a.actor + " " + a.action + " " + (a.detail || "")).toLowerCase().includes(q));
  $("auditCountLabel").textContent = list.length + " of " + (S.audit || []).length;
  $("auditRows").innerHTML = list.map(a =>
    '<tr><td class="n">' + esc(new Date(a.at).toLocaleString()) + '</td><td>' + esc(actorLabel(a.actor)) +
    '</td><td><code>' + esc(a.action) + '</code></td><td class="muted">' + esc(a.detail || "") + '</td></tr>').join("");
  $("auditEmpty").innerHTML = list.length ? "" :
    ((S.audit || []).length
      ? '<div class="empty">Nothing matches that search.</div>'
      : '<div class="empty">No administrative actions recorded yet. Pausing indexing or revoking a key will appear here.</div>');
}
$("auditSearch").addEventListener("input", renderAudit);

async function loadAudit() {
  const d = await api("/admin/api/audit?limit=100");
  S.audit = d.audit || [];
  renderAudit();
}

// ---- drill-down drawer
function openDrawer(html) {
  $("drawerBody").innerHTML = html;
  $("drawer").classList.add("open");
  $("drawerBg").classList.add("open");
}
function closeDrawer() {
  $("drawer").classList.remove("open");
  $("drawerBg").classList.remove("open");
}
$("drawerClose").addEventListener("click", closeDrawer);
$("drawerBg").addEventListener("click", closeDrawer);
document.addEventListener("keydown", e => { if (e.key === "Escape") closeDrawer(); });

async function openRepo(name) {
  openDrawer('<h3>' + esc(name) + '</h3><div class="empty">loading...</div>');
  try {
    const d = await api("/admin/api/repos/" + encodeURIComponent(name));
    const q = (d.queriers || []);
    const qmax = Math.max(1, ...q.map(i => i.count));
    openDrawer(
      '<h3>' + esc(d.name) + '</h3>' +
      '<div class="muted">Repository</div>' +
      '<dl class="kv">' +
        '<dt>Indexed</dt><dd>' + (d.indexed ? "yes" : "<b>not in the graph</b>") + '</dd>' +
        '<dt>Elements</dt><dd>' + d.nodes.toLocaleString() + '</dd>' +
        '<dt>Last indexed</dt><dd>' + (d.age_seconds ? ago(d.age_seconds) + (d.stale ? ' <span class="pill warn">stale</span>' : '') : "never") + '</dd>' +
        '<dt>Requests</dt><dd>' + d.requests.toLocaleString() + ' <span class="muted">(last ' + d.days + ' days)</span></dd>' +
      '</dl>' +
      '<h2>Who queries it</h2>' +
      (q.length
        ? '<table><tbody>' + q.map(i => '<tr><td>' + esc(actorLabel(i.name)) +
            '<div class="bar" style="width:' + Math.round(i.count / qmax * 100) + '%"></div></td>' +
            '<td class="n">' + i.count + '</td></tr>').join("") + '</tbody></table>'
        : '<div class="empty">No recorded queries in this window.</div>'));
  } catch (e) {
    openDrawer('<h3>' + esc(name) + '</h3><div class="empty">Could not load: ' + esc(e.message) + '</div>');
  }
}

async function openActor(actor) {
  openDrawer('<h3>' + esc(actor) + '</h3><div class="empty">loading...</div>');
  try {
    const d = await api("/admin/api/actors/" + encodeURIComponent(actor));
    const repos = d.repos || [];
    const rmax = Math.max(1, ...repos.map(i => i.count));
    const anom = S.anomalies[actor];
    openDrawer(
      '<h3>' + esc(actorLabel(d.actor)) + '</h3>' +
      '<div class="muted">Access</div>' +
      (anom ? '<div class="note">Used ' + anom.repo_count + ' repositories in the recent window (' +
              anom.requests + ' requests). Shown so the access decision has context.</div>' : '') +
      '<dl class="kv"><dt>Requests</dt><dd>' + d.requests.toLocaleString() +
        ' <span class="muted">(last ' + d.days + ' days)</span></dd></dl>' +
      '<h2>Keys</h2>' +
      (d.keys.length
        ? '<table><tbody>' + d.keys.map(k => {
            const s = keyState(k);
            return '<tr><td><code>' + esc(k.id) + '</code><div class="muted">expires ' +
              esc(new Date(k.expires_at).toLocaleDateString()) + '</div></td>' +
              '<td class="n"><span class="pill ' + (s === "active" ? "good" : s === "expired" ? "warn" : "quiet") + '">' + s + '</span></td>' +
              '<td class="n">' + (s === "active"
                ? '<button class="act danger" data-action="revoke" data-owner="' + esc(k.owner) + '">Revoke</button>' : '') + '</td></tr>';
          }).join("") + '</tbody></table>'
        : '<div class="empty">No keys - this identity is service or shared access, not a portal user.</div>') +
      '<h2 style="margin-top:1rem">Repositories used</h2>' +
      (repos.length
        ? '<table><tbody>' + repos.map(i => '<tr><td>' + esc(i.name) +
            '<div class="bar" style="width:' + Math.round(i.count / rmax * 100) + '%"></div></td>' +
            '<td class="n">' + i.count + '</td></tr>').join("") + '</tbody></table>'
        : '<div class="empty">No recorded activity in this window.</div>'));
  } catch (e) {
    openDrawer('<h3>' + esc(actor) + '</h3><div class="empty">Could not load: ' + esc(e.message) + '</div>');
  }
}

$("drawerBody").addEventListener("click", e => {
  const btn = e.target.closest("button[data-action='revoke']");
  if (btn) revoke(btn.dataset.owner);
});

// ---- actions
async function revoke(owner) {
  if (!confirm("Revoke the API key for " + owner + "?\n\nThey will lose access within a minute and must sign in again to mint a new one.")) return;
  await api("/admin/api/keys/revoke", {
    method: "POST",
    headers: {"Content-Type": "application/json", "Accept": "application/json"},
    body: JSON.stringify({owner: owner})
  });
  closeDrawer();
  await Promise.all([loadKeys(), loadAudit()]);
}

async function pause() {
  const minutes = parseInt($("pauseFor").value, 10) || 0;
  await api("/admin/api/indexing/pause", {
    method: "POST",
    headers: {"Content-Type": "application/json", "Accept": "application/json"},
    body: JSON.stringify({minutes: minutes, reason: $("pauseReason").value})
  });
  $("pauseReason").value = "";
  await Promise.all([loadOverview(), loadAudit()]);
  renderHealth();
}

async function resume() {
  await api("/admin/api/indexing/resume", {method: "POST", headers: {"Accept": "application/json"}});
  await Promise.all([loadOverview(), loadAudit()]);
  renderHealth();
}

$("pauseBtn").addEventListener("click", pause);
$("resumeBtn").addEventListener("click", resume);
$("usageDays").addEventListener("change", () => loadUsage().catch(reportError));

// ---- refresh
// loadOverview must finish before loadUsage: usage renders the repositories
// table, which reads freshness that overview populates. Running them
// concurrently made freshness intermittently missing on first paint.
function reportError(e) {
  $("health").innerHTML = '<span class="dot bad"></span>Could not load: ' + esc(e.message);
}

async function refresh() {
  try {
    await loadAnomalies();
    await loadOverview();
    await Promise.all([loadUsage(), loadKeys(), loadAudit()]);
    renderHealth();
  } catch (e) {
    reportError(e);
  }
}
$("refreshBtn").addEventListener("click", refresh);
refresh();
setInterval(refresh, 30000);
</script>
</body>
</html>`

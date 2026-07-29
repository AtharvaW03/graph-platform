package admin

import (
	"net/http"
)

// dashboard serves the single-page operator view. It renders as a static
// shell and fetches every panel from /admin/api/* on load, so the same data
// backs both the UI and any scripted access, with no server-side templating
// of untrusted values.
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
  }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #0f1115; --panel: #171a21; --ink: #e6e8ec; --muted: #9aa3b2;
            --line: #262b35; --accent: #60a5fa; --warn: #d97706; --bad: #ef4444; --good: #4ade80; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--ink);
         font-family: system-ui, -apple-system, "Segoe UI", sans-serif; line-height: 1.45; }
  header { padding: 1.2rem 1.5rem; border-bottom: 1px solid var(--line);
           display: flex; align-items: baseline; gap: 1rem; flex-wrap: wrap; }
  h1 { font-size: 1.1rem; margin: 0; letter-spacing: .01em; }
  .muted { color: var(--muted); font-size: .85rem; }
  main { padding: 1.25rem; display: grid; gap: 1.25rem;
         grid-template-columns: repeat(auto-fit, minmax(21rem, 1fr)); max-width: 100rem; }
  section { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 1rem 1.1rem; }
  section h2 { font-size: .78rem; text-transform: uppercase; letter-spacing: .07em;
               color: var(--muted); margin: 0 0 .8rem; font-weight: 600; }
  table { width: 100%; border-collapse: collapse; font-size: .9rem; }
  td { padding: .3rem 0; vertical-align: top; }
  td.n { text-align: right; font-variant-numeric: tabular-nums; color: var(--muted); white-space: nowrap; }
  .big { font-size: 1.9rem; font-weight: 600; font-variant-numeric: tabular-nums; }
  .row { display: flex; gap: 1.75rem; flex-wrap: wrap; }
  .pill { display: inline-block; padding: .12rem .55rem; border-radius: 999px;
          font-size: .75rem; border: 1px solid var(--line); }
  .pill.good { color: var(--good); border-color: currentColor; }
  .pill.warn { color: var(--warn); border-color: currentColor; }
  .pill.bad  { color: var(--bad);  border-color: currentColor; }
  button { font: inherit; padding: .35rem .8rem; border-radius: 6px; cursor: pointer;
           border: 1px solid var(--line); background: transparent; color: inherit; }
  button.primary { border-color: var(--accent); color: var(--accent); }
  button.danger { border-color: var(--bad); color: var(--bad); }
  input, select { font: inherit; padding: .3rem .5rem; border-radius: 6px;
                  border: 1px solid var(--line); background: transparent; color: inherit; }
  .controls { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; margin-top: .6rem; }
  .bar { height: 6px; background: var(--accent); border-radius: 3px; opacity: .75; }
  code { font-family: ui-monospace, Consolas, monospace; font-size: .85em; }
  .scroll { max-height: 17rem; overflow-y: auto; }
  .empty { color: var(--muted); font-size: .875rem; padding: .4rem 0; }
  .spark { display: flex; align-items: flex-end; gap: 2px; height: 44px; margin-top: .3rem; }
  .spark div { flex: 1; background: var(--accent); opacity: .7; border-radius: 2px 2px 0 0; min-height: 2px; }
</style>
</head>
<body>
<header>
  <h1>A1 Knowledge Graph - admin</h1>
  <span class="muted" id="who"></span>
  <span class="muted" id="updated" style="margin-left:auto"></span>
</header>
<main>
  <section>
    <h2>Indexing</h2>
    <div id="indexing"><div class="empty">loading...</div></div>
    <div class="controls">
      <select id="pauseFor">
        <option value="0">indefinitely</option>
        <option value="60">for 1 hour</option>
        <option value="180">for 3 hours</option>
        <option value="480">for 8 hours</option>
      </select>
      <input id="pauseReason" placeholder="reason (optional)" style="flex:1;min-width:9rem">
      <button class="danger" onclick="pause()">Pause</button>
      <button class="primary" onclick="resume()">Resume</button>
    </div>
  </section>

  <section>
    <h2>Graph</h2>
    <div class="row">
      <div><div class="big" id="totalNodes">-</div><div class="muted">code elements</div></div>
      <div><div class="big" id="repoCount">-</div><div class="muted">repositories</div></div>
      <div><div class="big" id="staleCount">-</div><div class="muted">stale</div></div>
    </div>
    <div class="scroll" style="margin-top:.7rem"><table id="repos"></table></div>
  </section>

  <section>
    <h2>Usage <span class="muted" id="usageWindow"></span></h2>
    <div class="row">
      <div><div class="big" id="totalReq">-</div><div class="muted">requests</div></div>
    </div>
    <div class="spark" id="spark"></div>
    <div style="margin-top:.8rem"><strong class="muted">Top users</strong><table id="topUsers"></table></div>
  </section>

  <section>
    <h2>Most-queried repositories</h2>
    <table id="topRepos"></table>
    <div style="margin-top:.9rem"><strong class="muted">Endpoints</strong><table id="topEndpoints"></table></div>
  </section>

  <section>
    <h2>Access anomalies</h2>
    <div class="muted" id="anomalyHint"></div>
    <div id="anomalies"><div class="empty">loading...</div></div>
  </section>

  <section>
    <h2>API keys</h2>
    <div class="scroll"><table id="keys"></table></div>
  </section>

  <section style="grid-column: 1 / -1">
    <h2>Admin audit trail</h2>
    <div class="scroll"><table id="audit"></table></div>
  </section>
</main>

<script>
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? "" : s).replace(/[&<>"']/g, c =>
  ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));

async function api(path, opts) {
  const res = await fetch(path, Object.assign({headers: {"Accept": "application/json"}}, opts || {}));
  if (!res.ok) throw new Error(path + " -> " + res.status);
  return res.json();
}

function rows(el, items, render) {
  if (!items || !items.length) { el.innerHTML = '<tr><td class="empty">no data yet</td></tr>'; return; }
  el.innerHTML = items.map(render).join("");
}

function ranked(el, items) {
  const max = Math.max(1, ...items.map(i => i.count));
  rows(el, items, i =>
    '<tr><td>' + esc(i.name) + '<div class="bar" style="width:' +
    Math.round(i.count / max * 100) + '%"></div></td><td class="n">' + i.count + '</td></tr>');
}

function ago(sec) {
  if (sec == null) return "-";
  if (sec < 60) return sec + "s";
  if (sec < 3600) return Math.round(sec / 60) + "m";
  return Math.round(sec / 3600) + "h";
}

async function loadOverview() {
  const d = await api("/admin/api/overview");
  $("totalNodes").textContent = d.total_nodes.toLocaleString();
  $("repoCount").textContent = (d.repos || []).length;
  $("staleCount").textContent = d.stale_repos;

  const ageBy = {};
  (d.freshness || []).forEach(f => ageBy[f.repo] = f);
  rows($("repos"), d.repos, r => {
    const f = ageBy[r.name];
    const pill = f ? '<span class="pill ' + (f.stale ? "warn" : "good") + '">' + ago(f.age_seconds) + '</span>' : "";
    return '<tr><td>' + esc(r.name) + '</td><td class="n">' + r.nodes.toLocaleString() +
           '</td><td class="n">' + pill + '</td></tr>';
  });

  const st = d.indexing || {};
  const paused = st.paused;
  $("indexing").innerHTML =
    '<div class="big">' + (paused ? '<span class="pill bad">paused</span>' : '<span class="pill good">running</span>') + '</div>' +
    (paused
      ? '<div class="muted" style="margin-top:.4rem">by ' + esc(st.actor || "?") +
        (st.paused_until ? ' until ' + esc(new Date(st.paused_until).toLocaleString()) : ' (indefinitely)') +
        (st.reason ? '<br>reason: ' + esc(st.reason) : '') + '</div>'
      : '<div class="muted" style="margin-top:.4rem">indexing on schedule</div>');

  rows($("audit"), d.recent_audit, a =>
    '<tr><td class="n">' + esc(new Date(a.at).toLocaleString()) + '</td><td>' + esc(a.actor) +
    '</td><td><code>' + esc(a.action) + '</code></td><td class="muted">' + esc(a.detail || "") + '</td></tr>');

  $("updated").textContent = "updated " + new Date().toLocaleTimeString();
}

async function loadUsage() {
  const d = await api("/admin/api/usage?days=30");
  $("usageWindow").textContent = "last " + d.days + " days";
  $("totalReq").textContent = (d.total_requests || 0).toLocaleString();
  ranked($("topUsers"), d.top_users || []);
  ranked($("topRepos"), d.top_repos || []);
  ranked($("topEndpoints"), d.top_endpoints || []);

  const t = d.traffic || [];
  const max = Math.max(1, ...t.map(x => x.count));
  $("spark").innerHTML = t.map(x =>
    '<div style="height:' + Math.max(2, Math.round(x.count / max * 44)) + 'px" title="' +
    esc(x.day) + ': ' + x.count + '"></div>').join("");
}

async function loadAnomalies() {
  const d = await api("/admin/api/anomalies");
  $("anomalyHint").textContent =
    "more than " + d.threshold + " repositories touched by one identity within " + d.window_minutes + " minutes";
  const list = d.anomalies || [];
  if (!list.length) {
    $("anomalies").innerHTML = '<div class="empty">nothing unusual</div>';
    return;
  }
  $("anomalies").innerHTML = list.map(a =>
    '<div style="margin-top:.6rem"><span class="pill bad">' + a.repo_count + ' repos</span> ' +
    esc(a.actor) + '<div class="muted">' + a.requests + ' requests, ' +
    esc(new Date(a.first_seen).toLocaleTimeString()) + ' - ' +
    esc(new Date(a.last_seen).toLocaleTimeString()) + '</div>' +
    '<div class="muted"><code>' + a.repos.slice(0, 12).map(esc).join(", ") +
    (a.repos.length > 12 ? ", +" + (a.repos.length - 12) + " more" : "") + '</code></div>' +
    '<button class="danger" style="margin-top:.3rem" onclick="revoke(\'' + esc(a.actor) + '\')">Revoke key</button></div>').join("");
}

async function loadKeys() {
  const d = await api("/admin/api/keys");
  const now = Date.now();
  rows($("keys"), d.keys, k => {
    const expired = new Date(k.expires_at).getTime() < now;
    const state = k.revoked_at ? '<span class="pill">revoked</span>'
                : expired ? '<span class="pill warn">expired</span>'
                : '<span class="pill good">active</span>';
    const used = k.last_used_at ? new Date(k.last_used_at).toLocaleDateString() : "never";
    return '<tr><td>' + esc(k.owner) + '<div class="muted">used ' + used + '</div></td>' +
      '<td class="n">' + state + '</td><td class="n">' +
      (k.revoked_at || expired ? "" :
        '<button class="danger" onclick="revoke(\'' + esc(k.owner) + '\')">Revoke</button>') + '</td></tr>';
  });
}

async function revoke(owner) {
  if (!confirm("Revoke the API key for " + owner + "?")) return;
  await api("/admin/api/keys/revoke", {
    method: "POST",
    headers: {"Content-Type": "application/json", "Accept": "application/json"},
    body: JSON.stringify({owner: owner})
  });
  await Promise.all([loadKeys(), loadOverview()]);
}

async function pause() {
  const minutes = parseInt($("pauseFor").value, 10) || 0;
  await api("/admin/api/indexing/pause", {
    method: "POST",
    headers: {"Content-Type": "application/json", "Accept": "application/json"},
    body: JSON.stringify({minutes: minutes, reason: $("pauseReason").value})
  });
  $("pauseReason").value = "";
  loadOverview();
}

async function resume() {
  await api("/admin/api/indexing/resume", {
    method: "POST", headers: {"Accept": "application/json"}
  });
  loadOverview();
}

function refresh() {
  loadOverview().catch(e => $("indexing").innerHTML = '<div class="empty">' + esc(e.message) + '</div>');
  loadUsage().catch(() => {});
  loadAnomalies().catch(() => {});
  loadKeys().catch(() => {});
}
refresh();
setInterval(refresh, 30000);
</script>
</body>
</html>`

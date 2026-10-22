// Pure helpers for the hub app: no DOM, so node --test can check them
// (lib.test.js).

// route is the app's view for a path: {name, ...params}.
export function route(path) {
  const parts = path.replace(/\/+$/, '').split('/').filter(Boolean).map(decodeURIComponent);
  if (parts.length === 0) return { name: 'home' };
  if (parts.length === 1 && parts[0] === 'approvals') return { name: 'approvals' };
  if (parts.length === 1 && parts[0] === 'admin') return { name: 'admin' };
  if (parts[0] === 'e' && parts.length >= 2) {
    const id = parts[1];
    if (parts.length === 2) return { name: 'runs', id };
    if (parts.length === 3 && ['runs', 'lanes', 'queue'].includes(parts[2])) return { name: parts[2], id };
    if (parts[2] === 'runs' && parts.length === 4) return { name: 'run', id, run: parts[3] };
    if (parts[2] === 'questions' && parts.length === 4) return { name: 'question', id, req: parts[3] };
    if (parts[2] === 'runs' && parts.length === 6 && parts[4] === 'jobs' && /^\d+$/.test(parts[5])) {
      return { name: 'job', id, run: parts[3], lane: Number(parts[5]) };
    }
  }
  return { name: 'notfound' };
}

// href builds an app path from its parts, each escaped.
export function href(...parts) {
  return '/' + parts.map((p) => encodeURIComponent(String(p))).join('/');
}

// api builds an API path from its parts, each escaped.
export function api(...parts) {
  return '/api/' + parts.map((p) => encodeURIComponent(String(p))).join('/');
}

// stateClass is a state's color: ok, bad, run, wait or dim (as the
// classic pages' stateClass in pages.go).
export function stateClass(s) {
  s = String(s || '').toLowerCase();
  if (s.startsWith('pass') || s === 'done' || s === 'approved') return 'ok';
  if (s.startsWith('fail') || s.startsWith('interrupt') || s === 'orphaned' || s === 'error' || s === 'blocked') return 'bad';
  if (s.startsWith('running')) return 'run';
  if (s.startsWith('skip') || ['waiting', 'queued', 'locked', 'held', 'scheduled', 'paused', 'drift', 'warn', 'stop'].includes(s)) return 'wait';
  return 'dim';
}

// fmtDuration writes seconds for people: 0.4s, 12s, 3m04s, 1h02m.
export function fmtDuration(sec) {
  if (sec == null || !isFinite(sec) || sec < 0) return '';
  if (sec < 10) return (Math.round(sec * 10) / 10) + 's';
  if (sec < 60) return Math.round(sec) + 's';
  const m = Math.floor(sec / 60);
  if (m < 60) return m + 'm' + String(Math.round(sec % 60)).padStart(2, '0') + 's';
  return Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0') + 'm';
}

// between is the seconds from one time to another ('' if either is missing).
export function between(from, to) {
  const a = Date.parse(from), b = Date.parse(to);
  if (!isFinite(a) || !isFinite(b) || a <= 0 || b <= 0) return null;
  return (b - a) / 1000;
}

// ago writes how long ago t was: 12s ago, 5m ago, 3h ago, Oct 2.
export function ago(t, now = Date.now()) {
  const ms = Date.parse(t);
  if (!isFinite(ms) || ms <= 0) return '';
  const d = Math.max(0, (now - ms) / 1000);
  if (d < 60) return Math.floor(d) + 's ago';
  if (d < 3600) return Math.floor(d / 60) + 'm ago';
  if (d < 86400) return Math.floor(d / 3600) + 'h ago';
  return new Date(ms).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// until writes how long until t: in 25m, in 1h05m, expired.
export function until(t, now = Date.now()) {
  const ms = Date.parse(t);
  if (!isFinite(ms)) return '';
  const d = (ms - now) / 1000;
  if (d <= 0) return 'expired';
  if (d < 60) return 'in ' + Math.ceil(d) + 's';
  const m = Math.floor(d / 60);
  return m < 60 ? 'in ' + m + 'm' : 'in ' + Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0') + 'm';
}

// utf8Length is how many bytes s is in UTF-8 (a log's offset).
export function utf8Length(s) {
  return new TextEncoder().encode(s).length;
}

// isOperator: me may send commands (an operator, or an admin, who is one too).
export function isOperator(me) { return !!me && (me.role === 'operator' || me.role === 'admin'); }

// isAdmin: me manages users, approvals and grants.
export function isAdmin(me) { return !!me && me.role === 'admin'; }

// can reports whether the logged-in user me may send command kind to
// enrollment e and e takes it (as the worker's swim.yml headless.allow
// says; the worker checks again): a log, or an answer, takes a viewer
// (the worker counts an answer's signature; a button answer still needs
// an operator, which the hub checks); everything else an operator.
export function can(me, e, kind) {
  if (!me || !me.user || !e || !e.headless) return false;
  if (kind !== 'log' && kind !== 'answer' && !isOperator(me)) return false;
  if (kind === 'log' && e.headless.logs_off) return false;
  return (e.headless.allow || []).includes(kind);
}

// layoutGraph places a run's lanes in columns by dependency depth: a lane
// sits one column right of the deepest lane it waits for in the run.
// lanes: [{lane, after: [n]}]. Returns nodes {lane, col, row, x, y},
// edges {from, to} and the drawing's size.
export function layoutGraph(lanes, size = { w: 200, h: 54, gx: 56, gy: 14 }) {
  const by = new Map(lanes.map((l) => [l.lane, l]));
  const depth = new Map();
  const visiting = new Set();
  const deepest = (n) => {
    if (depth.has(n)) return depth.get(n);
    if (visiting.has(n)) return 0; // a cycle: the run record shouldn't have one
    visiting.add(n);
    let d = 0;
    for (const a of by.get(n).after || []) if (by.has(a) && a !== n) d = Math.max(d, deepest(a) + 1);
    visiting.delete(n);
    depth.set(n, d);
    return d;
  };
  const cols = [];
  for (const l of [...lanes].sort((a, b) => a.lane - b.lane)) {
    const c = deepest(l.lane);
    (cols[c] ||= []).push(l.lane);
  }
  const nodes = [];
  cols.forEach((col, c) => col.forEach((lane, r) => nodes.push({
    lane, col: c, row: r, x: c * (size.w + size.gx), y: r * (size.h + size.gy),
  })));
  const edges = [];
  for (const l of lanes) for (const a of l.after || []) if (by.has(a) && a !== l.lane) edges.push({ from: a, to: l.lane });
  const rows = Math.max(1, ...cols.map((c) => (c ? c.length : 0)));
  return {
    nodes, edges, size,
    width: Math.max(1, cols.length) * (size.w + size.gx) - size.gx,
    height: rows * (size.h + size.gy) - size.gy,
  };
}

// steps groups a round's results by stage, in order, numbering every line
// shown (the command, each output line, each message) for #L links.
// Returns [{stage, failed, items: [{...result, i, lines: [{n, text, kind}]}]}].
export function steps(results) {
  const out = [];
  let n = 0;
  (results || []).forEach((r, i) => {
    const stage = r.stage || '';
    let g = out[out.length - 1];
    if (!g || g.stage !== stage) out.push(g = { stage, failed: false, items: [] });
    const lines = [];
    if (r.step && r.command) lines.push({ n: ++n, text: '$ ' + r.command, kind: 'cmd' });
    for (const t of r.output || []) lines.push({ n: ++n, text: t, kind: '' });
    for (const t of r.messages || []) lines.push({ n: ++n, text: '» ' + t, kind: 'msg' });
    if (stateClass(r.result) === 'bad') g.failed = true;
    g.items.push({ ...r, i, lines });
  });
  return out;
}

// hasOutput reports whether any step came with output (a repo without
// hub.logs: stream sends none).
export function hasOutput(results) {
  return (results || []).some((r) => (r.output || []).length > 0);
}

// lineMatches reports whether a line holds the query, ignoring case.
export function lineMatches(text, query) {
  return query !== '' && String(text).toLowerCase().includes(query.toLowerCase());
}

// byNamespace groups enrollments by namespace (those with none last).
export function byNamespace(enrollments) {
  const m = new Map();
  for (const e of enrollments) {
    const k = e.namespace || '';
    if (!m.has(k)) m.set(k, []);
    m.get(k).push(e);
  }
  return [...m.entries()].sort(([a], [b]) => (a === '') - (b === '') || a.localeCompare(b));
}

// enrollmentState is an enrollment's headline state: stale, running,
// waiting (for an answer or approval), or its activity.
export function enrollmentState(e) {
  if (e.stale) return { label: 'stale', cls: 'dim' };
  if ((e.pending || []).length > 0) return { label: 'waiting', cls: 'wait' };
  if ((e.lanes || []).some((l) => String(l.state).startsWith('running'))) return { label: 'running', cls: 'run' };
  if ((e.lanes || []).some((l) => stateClass(l.state) === 'bad')) return { label: 'failed', cls: 'bad' };
  return { label: 'idle', cls: 'ok' };
}

// matchEnrollment reports whether an enrollment matches the switcher's filter.
export function matchEnrollment(e, q) {
  q = q.trim().toLowerCase();
  if (!q) return true;
  return [e.name, e.host, e.namespace, e.root, e.id].some((f) => String(f || '').toLowerCase().includes(q));
}

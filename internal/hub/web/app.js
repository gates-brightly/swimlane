// The swim hub app: a top bar (enrollment switcher, tabs, live state,
// user) over one view per route (lib.js route). It reads the hub's read
// API and event stream (api.js) and renders everything through htm, which
// escapes text: nothing a worker sends is ever parsed as HTML.
import { html, render, useEffect, useMemo, useRef, useState } from './vendor/preact-htm.module.js';
import { adminCall, get, getText, onLog, post, send, start, useFetch, useStore } from './api.js';
import {
  ago, api, between, byNamespace, can, enrollmentState, fmtDuration, hasOutput, href, isAdmin, isOperator,
  badSchedules, successRate, triggerText, exitClass, laneState, scheduleClass, lastRun, layoutGraph, problems, lineMatches, matchEnrollment, repoHref, repoSummary, route, stateClass, steps, until, utf8Length,
} from './lib.js';

// ---- navigation ------------------------------------------------------

const listeners = new Set();
export function navigate(to, replace = false) {
  if (replace) history.replaceState(null, '', to);
  else history.pushState(null, '', to);
  for (const f of listeners) f();
  if (!to.includes('#')) window.scrollTo(0, 0);
}
function usePath() {
  const [p, set] = useState(location.pathname);
  useEffect(() => {
    const f = () => set(location.pathname);
    listeners.add(f);
    addEventListener('popstate', f);
    return () => { listeners.delete(f); removeEventListener('popstate', f); };
  }, []);
  return p;
}
// The app's own links navigate in place; login, the API and old /w/
// links (a redirect) load as pages.
document.addEventListener('click', (e) => {
  const a = e.target.closest && e.target.closest('a');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (a.target || a.hasAttribute('download') || a.origin !== location.origin) return;
  const p = a.pathname;
  if (p.startsWith('/api/') || p.startsWith('/w/') || p === '/login' || p.startsWith('/assets/')) return;
  e.preventDefault();
  navigate(p + a.search + a.hash);
});

function go(to) { return (e) => { if (!e.target.closest('a')) navigate(to); }; }

// ---- small pieces ----------------------------------------------------

const icons = {
  ok: html`<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7" fill="currentColor"/><path d="M4.8 8.3l2.1 2.1 4.3-4.6" stroke="#fff" stroke-width="1.7" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>`,
  bad: html`<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7" fill="currentColor"/><path d="M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/></svg>`,
  run: html`<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" opacity=".3"/><path d="M8 2a6 6 0 0 1 6 6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><animateTransform attributeName="transform" type="rotate" from="0 8 8" to="360 8 8" dur="1s" repeatCount="indefinite"/></path></svg>`,
  wait: html`<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M8 4.6V8l2.3 1.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`,
  dim: html`<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M5 8h6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`,
};
function Icon({ state }) {
  const c = stateClass(state);
  return html`<span class="ic ${c}" title=${state}>${icons[c]}</span>`;
}
function Pill({ state, label }) {
  const c = stateClass(state);
  return html`<span class="pill ${c}">${label || state}</span>`;
}
function Loading() { return html`<div class="empty">Loading…</div>`; }
function Problem({ r, what }) {
  if (r.status === 401) {
    return html`<div class="panel"><div class="empty">${what}: only a hub user can see this. <a href="/login">Log in</a></div></div>`;
  }
  if (r.status === 404) return html`<div class="panel"><div class="empty">${what} isn't on the hub.</div></div>`;
  return html`<div class="err">${r.error || 'Something went wrong.'}</div>`;
}

function Ago({ t }) {
  const [, tick] = useState(0);
  useEffect(() => { const i = setInterval(() => tick((x) => x + 1), 15000); return () => clearInterval(i); }, []);
  return html`<span title=${t}>${ago(t)}</span>`;
}

// When is a run's time, prominent: how long ago, then the date and time.
function When({ t, label, big }) {
  const [, tick] = useState(0);
  useEffect(() => { const i = setInterval(() => tick((x) => x + 1), 15000); return () => clearInterval(i); }, []);
  if (!t || !ago(t)) return '';
  return html`<span class="when ${big ? 'big' : ''}" title=${t}>${label ? label + ' ' : ''}<b>${ago(t)}</b> <span class="abs">${when(t)}</span></span>`;
}

// when writes a time for people, in local time ('—' if it isn't one).
function when(t) {
  const ms = Date.parse(t);
  return isFinite(ms) ? new Date(ms).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }) : (t || '—');
}

function useTitle(t) { useEffect(() => { document.title = t ? t + ' · swim hub' : 'swim hub'; }, [t]); }

// Confirm asks before a command: c is {title, body, label, danger, run}.
function Confirm({ c, close }) {
  const ok = useRef(null);
  useEffect(() => {
    const k = (e) => { if (e.key === 'Escape') close(); };
    addEventListener('keydown', k);
    if (ok.current) ok.current.focus();
    return () => removeEventListener('keydown', k);
  }, []);
  return html`<div class="modal-bg" onClick=${(e) => { if (e.target === e.currentTarget) close(); }}>
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="modal-t">
      <h2 id="modal-t">${c.title}</h2>
      <div class="modal-b">${c.body}</div>
      <div class="modal-f"><button class="btn" onClick=${close}>Cancel</button>
        <button ref=${ok} class="btn ${c.danger ? 'danger' : 'primary'}" onClick=${() => { close(); c.run(); }}>${c.label}</button></div>
    </div></div>`;
}

// useCommand sends enrollment e commands, asking first when there's a
// confirm, and says how the sending went: {run, dialog, note}.
function useCommand(e) {
  const [ask, setAsk] = useState(null);
  const [note, setNote] = useState(null);
  const go = async (cmd, what, link) => {
    setNote({ cls: 'wait', text: what + ': sending…' });
    const r = await send(e.id, cmd);
    setNote(r.ok
      ? { cls: 'ok', text: what + ': sent. The worker takes it at its next poll; its outcome shows under Commands.', link }
      : { cls: 'bad', text: what + ': ' + r.error });
  };
  return {
    run: (cmd, what, confirm, link) => (confirm ? setAsk({ ...confirm, run: () => go(cmd, what, link) }) : go(cmd, what, link)),
    dialog: ask && html`<${Confirm} c=${ask} close=${() => setAsk(null)}/>`,
    note: note && html`<div class="note ${note.cls}" role="status">${note.text}${note.link ? html` <a href=${note.link.href}>${note.link.text}</a>` : ''}</div>`,
  };
}

function short(h) { return String(h || '').slice(0, 8); }

// ---- top bar ----------------------------------------------------------

function Switcher({ store, current }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const [sel, setSel] = useState(0);
  const box = useRef(null);
  const input = useRef(null);
  const list = (store.enrollments || []).filter((e) => matchEnrollment(e, q));
  const groups = byNamespace(list);
  const flat = groups.flatMap(([, es]) => es);

  useEffect(() => {
    const key = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setOpen((o) => !o); }
      else if (e.key === 'Escape') setOpen(false);
    };
    const click = (e) => { if (box.current && !box.current.contains(e.target)) setOpen(false); };
    addEventListener('keydown', key);
    addEventListener('mousedown', click);
    return () => { removeEventListener('keydown', key); removeEventListener('mousedown', click); };
  }, []);
  useEffect(() => { if (open) { setQ(''); setSel(0); setTimeout(() => input.current && input.current.focus()); } }, [open]);

  const pick = (e) => { setOpen(false); navigate(href('e', e.id, 'lanes')); };
  const keys = (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(flat.length - 1, s + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === 'Enter' && flat[sel]) pick(flat[sel]);
  };
  const cur = current && (store.enrollments || []).find((e) => e.id === current);
  const st = cur && enrollmentState(cur);
  return html`<div class="switch" ref=${box}>
    <button class="switch-btn" onClick=${() => setOpen(!open)} aria-haspopup="listbox" aria-expanded=${open}>
      ${cur ? html`<span class="dot ${st.cls}"></span><span class="name">${cur.name}</span>` : html`<span class="name">All repos</span>`}
      <span class="kbd">⌘K</span>
    </button>
    ${open && html`<div class="pop" role="listbox">
      <input ref=${input} value=${q} placeholder="Find a runner…" onInput=${(e) => { setQ(e.target.value); setSel(0); }} onKeyDown=${keys} aria-label="Find a runner"/>
      <div class="pop-list">
        <a class="pop-item" href="/" onClick=${() => setOpen(false)}>All repos</a>
        ${flat.length === 0 && html`<div class="pop-empty">No runner matches.</div>`}
        ${groups.map(([ns, es]) => html`
          <div class="pop-ns">${ns || 'no namespace'}</div>
          ${es.map((e) => {
            const s = enrollmentState(e);
            return html`<a class="pop-item ${flat[sel] === e ? 'sel' : ''}" href=${href('e', e.id, 'lanes')} onClick=${() => setOpen(false)}>
              <span class="dot ${s.cls}" title=${s.label}></span><span>${e.name}</span><span class="sub">${e.host}</span></a>`;
          })}`)}
      </div></div>`}
  </div>`;
}

function User({ me }) {
  if (!me) return null;
  if (!me.user) return html`<div class="user"><a class="btn sm" href="/login">Log in</a></div>`;
  const out = async () => { await post('/logout', me.csrf); location.assign('/'); };
  return html`<div class="user"><span><b>${me.user}</b> <span class="muted role">${me.role}</span></span>
    <button class="btn sm" onClick=${out}>Log out</button></div>`;
}

function TopBar({ r, store }) {
  const id = r.id;
  const waiting = (store.enrollments || []).reduce((n, e) => n + (e.pending || []).length, 0);
  const tab = (name, label, to) => html`<a class=${r.name === name || (name === 'lanes' && r.name === 'history') ? 'on' : ''} href=${to}>${label}</a>`;
  const live = { live: ['ok', 'Live'], connecting: ['wait', 'Connecting'], offline: ['bad', 'Offline'] }[store.live];
  return html`<header class="top"><div class="top-in">
    <a class="brand" href="/"><img src="/assets/favicon.svg" alt=""/><span>swim hub</span></a>
    <${Switcher} store=${store} current=${id}/>
    <nav class="tabs">
      <a class=${['home', 'repo'].includes(r.name) ? 'on' : ''} href="/">Repos</a>
      ${id && tab('lanes', 'Lanes', href('e', id, 'lanes'))}
      ${id && tab('queue', 'Queue', href('e', id, 'queue'))}
      ${id && tab('runner', 'Runner', href('e', id, 'runner'))}
      <a class=${r.name === 'activity' ? 'on' : ''} href="/activity">Activity</a>
      <a class=${r.name === 'jobs' ? 'on' : ''} href="/jobs">Jobs</a>
      <a class=${r.name === 'schedules' ? 'on' : ''} href="/schedules">Schedules${(() => { const n = badSchedules(store.enrollments || []).filter((x) => scheduleClass(x.s.status) === 'bad').length; return n ? html` <span class="count hot">${n}</span>` : ''; })()}</a>
      <a class=${r.name === 'approvals' ? 'on' : ''} href="/approvals">Approvals <span class="count ${waiting ? 'hot' : ''}">${waiting}</span></a>
      ${isAdmin(store.me) && html`<a class=${r.name === 'admin' ? 'on' : ''} href="/admin">Admin</a>`}
    </nav>
    <span class="spacer"></span>
    <span class="live" title="Updates from the hub as they happen"><span class="dot ${live[0]}"></span><span class="t">${live[1]}</span></span>
    <${User} me=${store.me}/>
  </div></header>`;
}

// ---- views ------------------------------------------------------------

// Home is the repos: every namespace the hub's runners report under (a
// repo's remote, or HOST:IP for a runner outside git), with its runners.
function Home({ store }) {
  useTitle('');
  if (store.enrollments === null) return store.error ? html`<div class="err">${store.error}</div>` : html`<${Loading}/>`;
  if (store.enrollments.length === 0) {
    return html`<div class="panel"><div class="empty">No runners yet. In a repo:
      <code>swim hub enroll ${location.origin}</code>, then <code>swim watch</code>.</div></div>`;
  }
  const repos = byNamespace(store.enrollments);
  return html`<div class="head"><h1>Repos</h1><span class="muted">${repos.length}</span></div>
    <div class="cards">${repos.map(([ns, es]) => {
      const n = repoSummary(es);
      const { cls, label } = n;
      return html`<a class="card" href=${repoHref(ns)}>
        <div class="card-t"><span class="dot ${cls}"></span><span class="ell" title=${ns}>${ns || 'no repo'}</span><span class="pill ${cls}">${label}</span></div>
        <div class="card-s">${n.runners} runner${n.runners === 1 ? '' : 's'} · ${n.lanes} lane${n.lanes === 1 ? '' : 's'}${n.stale ? ' · ' + n.stale + ' offline' : ''}</div>
        ${lastRun(es) ? html`<div class="card-w"><${When} t=${lastRun(es)} label="last run"/></div>` : ''}
        ${badSchedules(es).slice(0, 2).map(({ e, s }) => html`<div class="card-p ${scheduleClass(s.status)}"><span class="dot ${scheduleClass(s.status)}"></span><span class="lbl">schedule ${s.status} · lane${s.lanes.length > 1 ? 's' : ''} ${s.lanes.join(', ')}</span><span class="sub">${s.why || s.cadence}</span></div>`)}
        ${problems(es).slice(0, 3).map((p) => html`<div class="card-p ${p.cls}"><span class="dot ${p.cls}"></span><span class="lbl">${es.length > 1 ? p.e.name + ' · ' : ''}lane ${p.l.lane} ${p.state}${p.l.finished ? ' ' + ago(p.l.finished) : ''}</span><span class="sub">${p.l.round || ''}</span></div>`)}
        <div class="card-l">${es.map((e) => html`<span class="chip ${e.stale ? 'dim' : enrollmentState(e).cls}" title=${e.host + ' · ' + e.activity}>${e.name}</span>`)}</div>
      </a>`;
    })}</div>`;
}

// Repo is one repo's runners, with what the hub knows of each.
function Repo({ r, store }) {
  useTitle(r.ns || 'no repo');
  if (store.enrollments === null) return html`<${Loading}/>`;
  const es = store.enrollments.filter((e) => (e.namespace || '') === r.ns);
  const head = html`<div class="crumbs"><a href="/">Repos</a><span>/</span><span>${r.ns || 'no repo'}</span></div>`;
  if (es.length === 0) return html`${head}<div class="panel"><div class="empty">No runner reports under ${r.ns || 'no repo'}.</div></div>`;
  return html`${head}
    <div class="head"><h1 class="ell">${r.ns || 'no repo'}</h1><span class="muted">${es.length} runner${es.length === 1 ? '' : 's'}</span></div>
    <div class="panel"><div class="tw"><table>
      <thead><tr><th class="i"></th><th>Runner</th><th>Address</th><th>State</th><th>Lanes</th><th>Last run</th><th>Runs</th><th>First connected</th><th>Last report</th></tr></thead>
      <tbody>${es.map((e) => {
        const st = enrollmentState(e);
        return html`<tr class="link" onClick=${go(href('e', e.id, 'lanes'))}>
          <td class="i"><span class="dot ${st.cls}"></span></td>
          <td><a href=${href('e', e.id, 'lanes')}>${e.name}</a><div class="sub mono">${e.root}</div></td>
          <td class="nowrap">${e.host}<div class="sub mono">${(e.runner || {}).addr || '—'}</div></td>
          <td><span class="pill ${st.cls}">${st.label}</span><div class="sub">${e.activity}</div></td>
          <td><div class="lanes-cell">${(e.lanes || []).map((l) => html`<span class="chip ${stateClass(l.state)}" title=${'lane ' + l.lane + ' ' + l.state}>${l.lane}</span>`)}</div></td>
          <td class="nowrap">${(e.runner || {}).last_run ? html`<${When} t=${e.runner.last_run}/>` : '—'}</td>
          <td class="nowrap">${(e.runner || {}).runs || 0}</td>
          <td class="nowrap">${(e.runner || {}).enrolled_at ? html`<span title=${when(e.runner.enrolled_at)}>${ago(e.runner.enrolled_at)}</span>` : '—'}</td>
          <td class="nowrap"><${Ago} t=${e.seen}/></td></tr>`;
      })}</tbody></table></div></div>`;
}

function Crumbs({ e, children }) {
  return html`<div class="crumbs"><a href="/">Repos</a><span>/</span>
    ${e ? html`<a href=${repoHref(e.namespace)}>${e.namespace || 'no repo'}</a><span>/</span><a href=${href('e', e.id, 'lanes')}>${e.name}</a>` : html`<span>…</span>`}${children}</div>`;
}

function useEnrollment(store, id) {
  return (store.enrollments || []).find((e) => e.id === id);
}

function Graph({ run, jobs, to, sel }) {
  const lanes = run.lanes.map((l) => ({ lane: l.lane, after: l.after || [] }));
  const g = useMemo(() => layoutGraph(lanes), [run.run]);
  const info = new Map(run.lanes.map((l) => [l.lane, l]));
  const { w, h } = g.size;
  const pos = new Map(g.nodes.map((n) => [n.lane, n]));
  return html`<div class="graph"><svg width=${g.width + 2} height=${g.height + 2} viewBox="-1 -1 ${g.width + 2} ${g.height + 2}" role="img" aria-label="The run's lanes and what each waited for">
    ${g.edges.map((e) => {
      const a = pos.get(e.from), b = pos.get(e.to);
      const x1 = a.x + w, y1 = a.y + h / 2, x2 = b.x, y2 = b.y + h / 2, mx = (x1 + x2) / 2;
      return html`<path class="edge" d="M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}"/>`;
    })}
    ${g.nodes.map((n) => {
      const l = info.get(n.lane);
      const c = stateClass(l.state);
      const dur = l.start != null && l.end != null ? fmtDuration(l.end - l.start) : '';
      const label = (l.round || 'lane ' + n.lane);
      return html`<a href=${to(n.lane)}><g class="node ${sel === n.lane ? 'sel' : ''}" transform="translate(${n.x},${n.y})">
        <title>lane ${n.lane}: ${l.round || ''} (${l.state})</title>
        <rect width=${w} height=${h} rx="8"/>
        <rect class="bar ${c}" width="4" height=${h - 16} x="0" y="8" rx="2"/>
        <circle class=${c} cx="18" cy=${h / 2 - 8} r="4.5"/>
        <text x="30" y=${h / 2 - 4}>${label.length > 24 ? label.slice(0, 23) + '…' : label}</text>
        <text class="sub" x="30" y=${h / 2 + 13}>${n.lane} · ${l.state}${dur ? ' · ' + dur : ''}</text></g></a>`;
    })}
  </svg></div>`;
}

// History is a lane's runs (r.lane) or a runner's (none), newest first, in
// a side bar with a dot for how each ended, and the chosen run: its lanes,
// and the chosen lane's full log under them. Batches still waiting for the
// lane head the list: a queued batch becomes a run once it runs.
function History({ r, store }) {
  const e = useEnrollment(store, r.id);
  const arrived = store.runs[r.id] || 0; // a run arriving may belong here
  const lane = r.lane != null ? r.lane : null;
  const laneQ = lane != null ? '&lane=' + lane : '';
  const list = useFetch(api('enrollments', r.id, 'runs') + '?limit=50' + laneQ, [arrived]);
  const [older, setOlder] = useState([]);
  const [next, setNext] = useState(null);
  useEffect(() => { setOlder([]); setNext(null); }, [r.id, lane]);
  const runs = list.data ? [...list.data.runs, ...older] : [];
  const more = next === null ? list.data && list.data.next : next;
  const loadMore = async () => {
    const p = await get(api('enrollments', r.id, 'runs') + '?limit=50&before=' + encodeURIComponent(more) + laneQ);
    if (p.data) { setOlder([...older, ...p.data.runs]); setNext(p.data.next || ''); }
  };
  const now = lane != null && e ? (e.lanes || []).find((l) => l.lane === lane) : null;
  // A run going on now isn't on the hub until it ends: it heads the list.
  const liveRun = now && now.run && now.state === 'running' && !runs.some((x) => x.run === now.run) ? now.run : '';
  const runId = r.run || liveRun || (runs[0] && runs[0].run) || '';
  const jobLane = r.job != null ? r.job : lane;
  const to = (run, job) => (lane != null
    ? href('e', r.id, 'lanes', lane, 'runs', run, ...(job != null ? ['jobs', job] : []))
    : href('e', r.id, 'runs', run, ...(job != null ? ['jobs', job] : [])));
  const queued = lane != null && e ? (e.queue || []).filter((b) => b.seq && ['queued', 'scheduled', 'held'].includes(b.state) && (b.lanes || []).includes(lane)) : [];
  useTitle((lane != null ? 'lane ' + lane : 'runs') + (e ? ' · ' + e.name : ''));
  const head = html`<${Crumbs} e=${e}><span>/</span>${lane != null
    ? html`<a href=${href('e', r.id, 'lanes')}>lanes</a><span>/</span><span>${lane}${now && now.round ? ' · ' + now.round : ''}</span>`
    : html`<span>runs</span>`}</${Crumbs}>`;
  return html`${head}
    <div class="job">
      <nav class="panel jobs-nav runs-nav" aria-label=${lane != null ? 'Runs of lane ' + lane : 'Runs'}>
        <div class="panel-h"><h2>${lane != null ? 'Lane ' + lane : 'Runs'}</h2><span class="muted">${runs.length}${more ? '+' : ''} run${runs.length === 1 ? '' : 's'}</span></div>
        ${queued.map((b) => html`<a href=${href('e', r.id, 'queue')} title=${'batch ' + b.id}>
          <span class="dot wait"></span><span class="t"><b>${b.state} #${b.seq}</b><span class="sub">${b.queued ? 'queued ' + ago(b.queued) : b.at || ''}</span></span><span class="d">next</span></a>`)}
        ${liveRun && html`<a class=${runId === liveRun ? 'on' : ''} href=${to(liveRun, jobLane)} title=${liveRun}>
          <span class="dot run"></span><span class="t"><b>running now</b><span class="sub mono">${liveRun}</span></span><span class="d">${now.step || ''}</span></a>`}
        ${runs.map((x) => {
          const lr = lane != null ? (x.lane_results || []).find((l) => l.lane === lane) : null;
          const lst = lr && laneState(lr.state, lr.dry_run);
          const anyDry = (x.lane_results || []).some((l) => laneState(l.state, l.dry_run) === 'dry run');
          const cls = lr ? exitClass(lst, lr.exit) : exitClass(x.result === 'passed' && anyDry ? 'dry run' : x.result);
          const t = x.finished && !String(x.finished).startsWith('0001') ? x.finished : x.started;
          return html`<a class=${x.run === runId ? 'on' : ''} href=${to(x.run, lane)} title=${x.run + ' · ' + when(t)}>
            <span class="dot ${cls}"></span><span class="t"><b>${ago(t)}</b><span class="sub mono">${x.run}</span></span>
            <span class="d ${cls}">${lr ? (lst === 'dry run' ? 'dry run' : lr.exit != null ? 'exit ' + lr.exit : lr.state) : x.result}</span></a>`;
        })}
        ${list.loading && !list.data ? html`<div class="empty">…</div>` : !list.data ? html`<${Problem} r=${list} what="These runs"/>`
          : runs.length === 0 && !liveRun ? html`<div class="empty">No runs ${lane != null ? 'of this lane ' : ''}on the hub yet.</div>` : ''}
        ${more ? html`<div class="panel-b"><button class="btn sm" onClick=${loadMore}>Older runs</button></div>` : ''}
      </nav>
      <div>${runId
        ? html`<${RunView} key=${runId} id=${r.id} run=${runId} e=${e} store=${store} jobLane=${jobLane} to=${to}/>`
        : list.data ? html`<div class="panel"><div class="empty">${lane != null ? 'Lane ' + lane + " hasn't run on the hub yet." : 'Nothing has run on the hub yet.'}
          A run shows here once <code>swim watch</code> has run a batch.</div></div>` : ''}</div>
    </div>`;
}

// RunView is one run: when, how it ended, its lanes as a graph and a
// table (each opens its log), and the chosen lane's log under them.
function RunView({ id, run: runId, e, store, jobLane, to }) {
  const me = store.me;
  const cmd = useCommand(e || { id });
  const arrived = store.runs[id] || 0;
  const res = useFetch(api('enrollments', id, 'runs', runId), [arrived]);
  if (res.loading && !res.data) return html`<${Loading}/>`;
  if (!res.data) {
    // Not on the hub (yet): the lane's log as it streams or once fetched.
    if (res.status === 404 && jobLane != null) return html`<${LiveLog} r=${{ id, run: runId, lane: jobLane }} store=${store}/>`;
    return html`<${Problem} r=${res} what="This run"/>`;
  }
  const { run, jobs } = res.data;
  const byLane = new Map(jobs.map((j) => [j.lane, j]));
  const result = res.data.result === 'passed' && jobs.some((j) => (j.dry_run || []).length) ? 'dry run' : res.data.result;
  // Its failed lanes, queued again as one batch, pinned to the scripts they
  // hold now (the worker reports them).
  const failed = run.lanes.filter((l) => failedState(l.state));
  const now = new Map(((e && e.lanes) || []).map((l) => [l.lane, l]));
  const ready = failed.map((l) => ({ was: l, now: now.get(l.lane) })).filter((x) => x.now && x.now.script_sha256 && x.now.manifest_sha256 && x.now.state !== 'running');
  const changed = ready.filter((x) => x.was.job && x.now.job && x.was.job !== x.now.job);
  const rerun = e && failed.length > 0 && can(me, e, 'queue') && ready.length > 0 && html`<button class="btn sm primary" onClick=${() => cmd.run(
    { kind: 'queue', lanes: ready.map((x) => ({ lane: x.now.lane, sha256: x.now.script_sha256, manifest: x.now.manifest_sha256 })) },
    'Re-run lanes ' + ready.map((x) => x.now.lane).join(', '), {
      title: 'Re-run ' + ready.length + ' failed lane' + (ready.length > 1 ? 's' : '') + '?', label: 'Re-run',
      body: html`<p>Lanes ${ready.map((x) => x.now.lane).join(', ')} as one new batch, with the scripts they hold now. It's unsigned and carries no approvals: a guard in it waits for an answer, or dry-runs, as the worker's swim.yml says.</p>
        ${changed.length ? html`<p class="bad">Lanes ${changed.map((x) => x.now.lane).join(', ')} hold${changed.length === 1 ? 's' : ''} a different job than in this run: the re-run runs the new one.</p>` : ''}
        ${ready.length < failed.length ? html`<p class="muted">Not re-run (running now, or no pending round to queue): lanes ${failed.filter((l) => !ready.some((x) => x.was === l)).map((l) => l.lane).join(', ')}.</p>` : ''}`,
    }, { href: href('e', id, 'queue'), text: 'See the queue' })}>Re-run ${ready.length} failed</button>`;
  const ended = run.finished && !String(run.finished).startsWith('0001');
  return html`
    <div class="head"><${Icon} state=${result}/><h1 class="mono">${run.run}</h1><${Pill} state=${result}/>
      <div class="meta"><span>${fmtDuration(between(run.started, run.finished))}</span>
      <span>${run.lanes.length} lane${run.lanes.length === 1 ? '' : 's'}</span>${res.data.trigger ? html`<span>${triggerText(res.data.trigger)}</span>` : ''}${run.max_parallel ? html`<span>at most ${run.max_parallel} at once</span>` : ''}</div>
      <span class="spacer"></span>${rerun}</div>
    <div class="when-row"><${When} big t=${ended ? run.finished : run.started} label=${ended ? 'Finished' : 'Started'}/></div>
    ${cmd.note}${cmd.dialog}
    ${failed.length > 0 && e ? html`<${WhyNoRun} me=${me} e=${e}/>` : ''}
    <div class="panel">${run.lanes.length > 1 ? html`<${Graph} run=${{ ...run, lanes: run.lanes.map((l) => ({ ...l, state: laneState(l.state, (byLane.get(l.lane) || {}).dry_run) })) }} jobs=${jobs} to=${(n) => to(run.run, n)} sel=${jobLane}/>` : ''}
      <div class="tw"><table>
      <thead><tr><th class="i"></th><th>Lane</th><th>Result</th><th>Steps</th><th>Duration</th><th>Failed steps</th></tr></thead>
      <tbody>${run.lanes.map((l) => {
        const j = byLane.get(l.lane);
        const dest = to(run.run, l.lane);
        const st = laneState(l.state, j && j.dry_run);
        return html`<tr class="link ${jobLane === l.lane ? 'sel' : ''}" onClick=${go(dest)}>
          <td class="i"><${Icon} state=${st}/></td>
          <td><a href=${dest}>${l.lane}</a><div class="sub">${l.round || ''}</div>
            ${l.reason ? html`<div class="sub">${l.reason}</div>` : ''}
            ${st === 'dry run' ? html`<div class="sub wait">its change didn't run: ${j.dry_run.join('; ')}</div>` : ''}</td>
          <td><${Pill} state=${st}/>${l.exit != null && l.exit !== 0 ? html` <span class="mono muted">exit ${l.exit}</span>` : ''}</td>
          <td class="nowrap">${j ? html`<span class="ok">${j.counts.pass} passed</span>${j.counts.fail ? html` · <span class="bad">${j.counts.fail} failed</span>` : ''}${j.counts.skip ? html` · <span class="wait">${j.counts.skip} skipped</span>` : ''}` : html`<span class="muted">no round</span>`}</td>
          <td class="nowrap mono">${l.start != null && l.end != null ? fmtDuration(l.end - l.start) : ''}</td>
          <td>${j && j.failed_steps.length ? j.failed_steps.map((f) => html`<div class="mono bad">${f}</div>`) : ''}</td></tr>`;
      })}</tbody></table></div></div>
    ${jobLane != null && run.lanes.some((l) => l.lane === jobLane)
      ? (byLane.has(jobLane)
        ? html`<${JobLog} key=${run.run + '/' + jobLane} id=${id} run=${run.run} lane=${jobLane} e=${e} store=${store}/>`
        : html`<div class="panel"><div class="empty">Lane ${jobLane} ran no round in this run (${(run.lanes.find((l) => l.lane === jobLane) || {}).state || 'skipped'}).</div></div>`)
      : ''}`;
}

// asked are the logs this page has asked runners for, so a lane opened
// again doesn't ask twice.
const asked = new Set();

// JobLog is one lane's round in a run: its steps and their output. When
// the run came without step output (hub.logs: request), it asks the runner
// for the lane's log at once (where the runner takes log requests) and
// fills in as it arrives; if the steps can't be matched to it, the full
// log shows under them.
function JobLog({ id, run, lane, e, store }) {
  const [fetched, setFetched] = useState(0);
  const job = useFetch(api('enrollments', id, 'runs', run, 'jobs', lane), [fetched]);
  const cmd = useCommand(e || { id });
  const [raw, setRaw] = useState('');
  const [query, setQuery] = useState('');
  const [all, setAll] = useState(null); // null: failed steps open
  const logPath = api('enrollments', id, 'runs', run, 'jobs', lane, 'log');
  useEffect(() => onLog((l) => {
    if (l.enrollment === id && l.run === run && l.lane === lane) setFetched((x) => x + 1);
  }), [id, run, lane]);
  const d = job.data && job.data.job;
  const hidden = !!(job.data && job.data.output_hidden); // not logged in: steps and exits, no output
  const bare = !!d && !hidden && !hasOutput(d.results);
  const mayAsk = !!e && can(store.me, e, 'log');
  useEffect(() => {
    if (!bare) return;
    getText(logPath).then((res) => { if (res.status === 200) setRaw(res.text); });
    const key = id + '/' + run + '/' + lane;
    if (mayAsk && !asked.has(key)) {
      asked.add(key);
      cmd.run({ kind: 'log', lane, pin: { run } }, 'Asking ' + e.name + ' for the full log of lane ' + lane);
    }
  }, [bare, fetched]);
  const groups = useMemo(() => (d ? steps(d.results) : []), [job.data]);
  if (job.loading && !job.data) return html`<${Loading}/>`;
  if (!d) return html`<${Problem} r=${job} what="This lane's log"/>`;
  let hits = 0;
  if (query) for (const g of groups) for (const s of g.items) for (const l of s.lines) if (lineMatches(l.text, query)) hits++;
  const isOpen = (s) => {
    if (all !== null) return all;
    if (query) return s.lines.some((l) => lineMatches(l.text, query));
    return stateClass(s.result) === 'bad';
  };
  const dry = (d.results || []).filter((x) => x.result === 'SKIP' && String(x.detail || '').startsWith('dry run')).map((x) => x.label + ': ' + x.detail);
  const dstate = laneState(d.result, dry);
  const rawLines = bare && raw ? raw.replace(/\n$/, '').split('\n') : [];
  return html`<div class="panel joblog">
    <div class="panel-h"><${Icon} state=${dstate}/><h2>Lane ${lane}${d.round ? ' · ' + d.round : ''}</h2><${Pill} state=${dstate}/>
      <span class="muted">${[d.exit != null ? 'exit ' + d.exit : '', d.duration || ''].filter(Boolean).join(' · ')}</span>
      <span class="spacer"></span><${When} t=${d.started_at} label="started"/></div>
    <div class="toolbar">
      <input class="search" type="search" placeholder="Search the output…" value=${query} onInput=${(ev) => { setQuery(ev.target.value); setAll(null); }} aria-label="Search the output"/>
      ${query && html`<span class="muted">${hits} line${hits === 1 ? '' : 's'}</span>`}
      <button class="btn sm" onClick=${() => setAll(true)}>Expand all</button>
      <button class="btn sm" onClick=${() => setAll(false)}>Collapse all</button>
      <a class="btn sm" href=${logPath} target="_blank" rel="noopener">Raw log</a>
    </div>
    ${hidden ? html`<div class="panel-b"><div class="note">The steps, their commands and exit codes are below; their output needs a hub user. <a href="/login">Log in</a></div></div>` : ''}
    ${bare ? html`<div class="panel-b"><div class="note">${raw
        ? 'The steps show no output of their own; the full log the runner sent is under them.'
        : mayAsk ? html`Asking ${e.name} for this lane's log: it fills in here when it arrives.`
        : html`No output on the hub for this lane, and ${store.me && store.me.user ? "this runner doesn't take log requests from the hub" : html`<a href="/login">log in</a> to ask the runner for it`}. <code>hub: {logs: stream}</code> in its swim.yml sends output with every run.`}</div>${cmd.note}</div>`
      : job.data.output_from === 'log' ? html`<div class="panel-b"><div class="note">The output is from the log fetched from the runner.</div></div>` : ''}
    <div class="log">${groups.map((g) => html`
      <div class="stage-h"><span class=${g.failed ? 'bad' : ''}>${g.stage || 'steps'}</span>${d.stages && d.stages[g.stage] ? html`<span>· ${d.stages[g.stage]}</span>` : ''}</div>
      ${g.items.map((s) => html`<${Step} key=${s.i} s=${s} open=${isOpen(s)} query=${query}/>`)}`)}
      ${groups.length === 0 ? html`<div class="mark">No steps recorded.</div>` : ''}
    </div>
    ${rawLines.length > 0 && html`<div class="panel-h"><h2>Full log</h2></div><div class="log">${rawLines.map((t, i) => html`<div class="ln ${lineMatches(t, query) ? 'hit' : ''}"><a>${i + 1}</a><span>${t}</span></div>`)}</div>`}
  </div>`;
}

function Step({ s, open, query }) {
  const c = stateClass(s.result);
  if (!s.step) {
    return html`<div class="mark"><span class=${c}>${s.result}</span><span>${s.label}${s.detail ? ' — ' + s.detail : ''}</span></div>`;
  }
  return html`<details class="step" open=${open}>
    <summary><span class="chev">▸</span><${Icon} state=${s.result}/><span class="lbl">${s.label}${s.detail ? html` <span class="dim">${s.detail}</span>` : ''}</span>
      <span class="d">${s.exit != null && s.exit !== 0 ? 'exit ' + s.exit + ' · ' : ''}${fmtDuration(s.duration_s)}</span></summary>
    <div class="body">${s.lines.map((l) => html`<div id=${'L' + l.n} class="ln ${l.kind} ${lineMatches(l.text, query) ? 'hit' : ''}"><a href=${'#L' + l.n}>${l.n}</a><span>${l.text}</span></div>`)}
      ${s.lines.length === 0 ? html`<div class="mark">no output</div>` : ''}</div>
  </details>`;
}

// LaneControls are the commands a lane takes now, from this user, on
// this worker (can), each pinned to what the page shows.
// failedState: a lane's round ended without passing, so it may be run again.
function failedState(s) {
  s = String(s || '').toLowerCase();
  return s.startsWith('fail') || s.startsWith('interrupt') || s === 'error' || s === 'orphaned';
}

// WhyNoRun says, to a user who could act, why the page offers no way to run
// lanes on e: nobody at it takes hub commands, or its swim.yml doesn't let
// the hub queue work (headless.allow).
function WhyNoRun({ me, e, kind }) {
  if (!me || !me.user) return null;
  if (!e.headless) return html`<div class="note">This worker isn't headless (someone is at its terminal, or it runs <code>swim watch</code> without <code>--headless</code>), so it takes no commands from the hub. On its machine: <code>swim watch --headless --detach</code>.</div>`;
  if (!isOperator(me)) return null;
  const need = kind === 'queue' ? ['queue'] : ['run', 'queue'];
  if (need.some((k) => can(me, e, k))) return null;
  if (e.access === 'read') return html`<div class="note">${e.name} enrolled with <b>read</b> access: the hub shows it and fetches its logs, and sends no commands. On its machine: <code>swim hub access readwrite</code>.</div>`;
  if (e.access === 'readwrite') return html`<div class="note">This repo's committed <code>swim.yml</code> narrows what the hub may send (<code>headless.allow</code>): add ${need.map((k, i) => html`${i ? ' or ' : ''}<code>${k}</code>`)} there, or drop the list to take everything readwrite allows.</div>`;
  return html`<div class="note">${e.name} enrolled before access levels, so its <code>swim.yml</code>'s <code>headless.allow</code> decides: on its machine, <code>swim hub access readwrite</code> lets the hub send every command.</div>`;
}

function LaneControls({ e, l, me, cmd }) {
  const running = l.state === 'running';
  const lane = 'lane ' + l.lane;
  const b = [];
  if (running && l.run && !l.paused && can(me, e, 'pause')) {
    b.push(html`<button class="btn sm" onClick=${() => cmd.run({ kind: 'pause', lane: l.lane, pin: { run: l.run } }, 'Pause ' + lane)}>Pause</button>`);
  }
  if (l.paused && l.run && can(me, e, 'unpause')) {
    b.push(html`<button class="btn sm" onClick=${() => cmd.run({ kind: 'unpause', lane: l.lane, pin: { run: l.run } }, 'Unpause ' + lane)}>Unpause</button>`);
  }
  if (running && l.run && can(me, e, 'interrupt')) {
    b.push(html`<button class="btn sm danger" onClick=${() => cmd.run({ kind: 'interrupt', lane: l.lane, pin: { run: l.run } }, 'Interrupt ' + lane, {
      title: 'Interrupt ' + lane + '?', label: 'Interrupt', danger: true,
      body: html`<p>${l.round || ''}</p><p class="muted">Stops run <code>${l.run}</code> on ${e.name}. The worker refuses it if that run has ended by the time it takes the command.</p>`,
    })}>Interrupt</button>`);
  }
  if (l.run && can(me, e, 'log') && e.logs !== 'stream') {
    b.push(html`<button class="btn sm" onClick=${() => cmd.run({ kind: 'log', lane: l.lane, pin: { run: l.run } }, 'Fetch the log of ' + lane, null,
      { href: href('e', e.id, 'runs', l.run, 'jobs', l.lane), text: 'Open it' })}>Fetch log</button>`);
  }
  const runKind = can(me, e, 'run') ? 'run' : can(me, e, 'queue') ? 'queue' : '';
  if (l.script_sha256 && l.manifest_sha256 && !running && runKind) {
    const again = failedState(l.state);
    const label = again ? 'Run again' : 'Run now';
    const c = runKind === 'run'
      ? { kind: 'run', lane: l.lane, pin: { sha256: l.script_sha256, manifest: l.manifest_sha256 } }
      : { kind: 'queue', lanes: [{ lane: l.lane, sha256: l.script_sha256, manifest: l.manifest_sha256 }] };
    b.push(html`<button class="btn sm ${again ? 'primary' : ''}" onClick=${() => cmd.run(c, label + ': ' + lane, {
      title: label + ': ' + lane + '?', label,
      body: html`<p>${l.round || ''}</p><p class="muted">Queues ${lane}'s script <code>${short(l.script_sha256)}</code> (manifest <code>${short(l.manifest_sha256)}</code>) on ${e.name}, unsigned, by hub:${me.user}, with no approvals of its own: its guards approve only as any unsigned batch's do. If the script has changed, the worker refuses it.</p>`,
    }, { href: href('e', e.id, 'queue'), text: 'See the queue' })}>${label}</button>`);
  }
  if (l.trash_job && !running && can(me, e, 'trash')) {
    b.push(html`<button class="btn sm danger" onClick=${() => cmd.run({ kind: 'trash', lane: l.lane, pin: { job: l.trash_job } }, 'Trash ' + lane, {
      title: 'Trash ' + lane + '?', label: 'Trash ' + lane, danger: true,
      body: html`<p>${l.round || ''}</p><p class="muted">job <code>${short(l.trash_job)}</code> · ${l.state}</p>
        <p class="muted">The worker moves this lane's script and its lane.${l.lane}.yml to <code>.swim/cleaned/</code> and archives its log. Nothing is deleted: <code>swim trash --restore STAMP</code> on the worker puts it back. If the lane holds another job by then, it refuses and nothing moves.</p>`,
    })}>Trash</button>`);
  }
  return b.length ? html`<div class="actions">${b}</div>` : null;
}

// Commands are an enrollment's recent commands and their outcomes, for a user.
function Commands({ e, store }) {
  const res = useFetch(api('enrollments', e.id, 'commands'), [store.commands[e.id] || 0]);
  if (!res.data) return null;
  const list = res.data.commands;
  return html`<div class="panel"><div class="panel-h"><h2>Commands</h2><span class="muted">sent to ${e.name} from the hub</span></div>
    ${list.length === 0 ? html`<div class="empty">None sent.</div>` : html`<div class="tw"><table>
      <thead><tr><th>#</th><th>Command</th><th>By</th><th>Sent</th><th>Outcome</th></tr></thead>
      <tbody>${list.map((c) => html`<tr>
        <td class="mono">${c.seq}</td>
        <td>${c.kind}${c.answer ? ' ' + c.answer.decision + (c.answer.sig ? ' (signed)' : '') : ''}${c.lane ? ' lane ' + c.lane : ''}${(c.lanes || []).length ? ' lanes ' + c.lanes.map((l) => l.lane).join(', ') : ''}
          ${c.pin.batch ? html` <span class="mono muted">${c.pin.batch}</span>` : ''}${c.pin.run ? html` <span class="mono muted">${c.pin.run}</span>` : ''}${c.pin.sha256 ? html` <span class="mono muted">${short(c.pin.sha256)}</span>` : ''}
          ${c.kind === 'log' && c.state === 'done' ? html` · <a href=${href('e', e.id, 'runs', c.pin.run, 'jobs', c.lane)}>view</a>` : ''}</td>
        <td>${c.by}</td><td class="nowrap"><${Ago} t=${c.at}/></td>
        <td><${Pill} state=${c.state.startsWith('expired') ? 'skipped' : c.state === 'sent' ? 'queued' : c.state === 'done' ? 'passed' : 'failed'} label=${c.state}/>
          ${c.detail ? html`<div class="sub">${c.detail}</div>` : ''}</td></tr>`)}
      </tbody></table></div>`}</div>`;
}

function Lanes({ r, store }) {
  const e = useEnrollment(store, r.id);
  const me = store.me;
  const cmd = useCommand(e || { id: r.id });
  const [picked, setPicked] = useState({});
  useTitle(e ? e.name + ' lanes' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This runner"/>`;
  // Lanes the hub may queue as one batch: idle ones holding a pending round
  // (the worker reports the script and manifest a queue command pins).
  const canQueue = can(me, e, 'queue');
  const queueable = (e.lanes || []).filter((l) => l.script_sha256 && l.manifest_sha256 && !String(l.state).startsWith('running'));
  const chosen = queueable.filter((l) => picked[l.lane]);
  const toggle = (n) => setPicked({ ...picked, [n]: !picked[n] });
  const queueBatch = chosen.length > 0 && html`<button class="btn sm primary" onClick=${() => cmd.run(
    { kind: 'queue', lanes: chosen.map((l) => ({ lane: l.lane, sha256: l.script_sha256, manifest: l.manifest_sha256 })) },
    'Queue lanes ' + chosen.map((l) => l.lane).join(', '), {
      title: 'Queue ' + chosen.length + ' lane' + (chosen.length > 1 ? 's' : '') + ' as one batch?', label: 'Queue',
      body: html`<p>Lanes ${chosen.map((l) => l.lane).join(', ')}, pinned to the scripts shown. The batch is unsigned and carries no approvals: a guard in it waits for an answer, or dry-runs, as the worker's swim.yml says.</p>`,
    }, { href: href('e', e.id, 'queue'), text: 'See the queue' })}>Queue ${chosen.length} as one batch</button>`;
  const drain = can(me, e, 'drain') && html`<button class="btn sm danger" onClick=${() => cmd.run({ kind: 'drain' }, 'Stop ' + e.name + ' after this batch', {
    title: 'Stop ' + e.name + ' after this batch?', label: 'Stop after this batch', danger: true,
    body: html`<p class="muted">The worker finishes the batch it's running, then exits; nothing new starts. Start it again on its machine.</p>`,
  })}>Stop after this batch</button>`;
  return html`<${Crumbs} e=${e}><span>/</span><span>lanes</span></${Crumbs}>
    <div class="head"><h1>Lanes</h1><${Pill} state=${enrollmentState(e).cls === 'run' ? 'running' : e.activity} label=${e.activity}/>
      <div class="meta"><span>reported <${Ago} t=${e.at}/></span>${e.headless ? html`<span>headless (${e.headless.mode})</span>` : ''}${e.access ? html`<span>access: ${e.access}</span>` : ''}
        ${e.logs ? html`<span>logs: ${e.logs}</span>` : ''}</div>
      <span class="spacer"></span>${queueBatch}${drain}</div>
    ${cmd.note}${cmd.dialog}
    <${WhyNoRun} me=${me} e=${e}/>
    <div class="panel">${(e.lanes || []).length === 0 ? html`<div class="empty">No lanes in use on ${e.name}.
      A new op reaches the hub from the runner: <code>swim new N</code> writes its lane script there (commit it if it's shared), and once ${e.name} reports it,
      the hub can run it, pinned to that script. The hub never sends a script of its own.</div>` : html`<div class="tw"><table>
      <thead><tr><th class="i"></th><th>Lane</th><th>State</th><th>Step</th><th>Steps</th></tr></thead>
      <tbody>${e.lanes.map((l) => html`<tr>
        <td class="i">${canQueue && queueable.includes(l)
          ? html`<input type="checkbox" aria-label=${'Queue lane ' + l.lane} checked=${!!picked[l.lane]} onChange=${() => toggle(l.lane)}/>`
          : html`<${Icon} state=${l.state}/>`}</td>
        <td><a class="lane-link" href=${href('e', e.id, 'lanes', l.lane)}><b>${l.lane}</b><div class="sub">${l.round || ''}</div></a></td>
        <td><${Pill} state=${laneState(l.state, l.dry_run)}/>
          ${laneState(l.state, l.dry_run) === 'dry run' ? html`<div class="sub wait">its change didn't run: ${l.dry_run.join('; ')}</div>` : ''}
          ${l.state === 'running' ? html`<div><${When} t=${l.started} label="started"/></div>` : l.finished ? html`<div><${When} t=${l.finished} label="ended"/></div>` : ''}
          ${l.paused ? html`<div class="sub">paused ${l.paused}</div>` : ''}${l.scheduled ? html`<div class="sub">${l.scheduled}</div>` : ''}
          ${l.reason ? html`<div class="sub">${l.reason}</div>` : ''}</td>
        <td>${l.step || ''}${l.progress ? html` <progress max="100" value=${pct(l.progress)}></progress> <span class="sub">${l.progress}</span>` : ''}
          ${l.message ? html`<div class="sub">${l.message}</div>` : ''}${l.cells ? html`<div class="sub">${l.cells}</div>` : ''}</td>
        <td class="nowrap"><span class="ok">${l.pass}</span> / <span class="bad">${l.fail}</span> / <span class="wait">${l.skip}</span>
          ${l.run && l.state !== 'waiting' ? html`<div><a href=${href('e', e.id, 'lanes', l.lane, 'runs', l.run, 'jobs', l.lane)}>${l.state === 'running' ? 'Follow log' : 'Log'}</a></div>` : ''}</td></tr>
        ${(l.tail || []).length ? html`<tr class="tail-row"><td colspan="5"><pre class="tail">${l.tail.join('\n')}</pre></td></tr>` : ''}
        ${(() => { const c = LaneControls({ e, l, me, cmd }); return c ? html`<tr class="ctl-row"><td></td><td colspan="4">${c}</td></tr>` : ''; })()}`)}
      </tbody></table></div>`}</div>
    ${me && me.user ? html`<${Commands} e=${e} store=${store}/>` : ''}
    ${(e.events || []).length > 0 && html`<div class="panel"><div class="panel-h"><h2>Recent events</h2></div><pre class="tail">${e.events.join('\n')}</pre></div>`}`;
}

function pct(p) {
  const m = /^(\d+)\/(\d+)$/.exec(p || '');
  return m && Number(m[2]) > 0 ? Math.min(100, Math.round((100 * Number(m[1])) / Number(m[2]))) : 0;
}

function Queue({ r, store }) {
  const e = useEnrollment(store, r.id);
  const me = store.me;
  const cmd = useCommand(e || { id: r.id });
  useTitle(e ? e.name + ' queue' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This runner"/>`;
  const canRm = can(me, e, 'dequeue'), canAgain = can(me, e, 'requeue');
  // A real entry (not a recurring schedule's template, seq 0), not running.
  const actions = (b) => {
    if (!b.seq || b.state === 'running') return '';
    const done = b.state === 'done' || b.state === 'orphaned';
    return html`${canAgain && done ? html`<button class="btn sm" onClick=${() => cmd.run({ kind: 'requeue', pin: { batch: b.id } }, 'Queue ' + b.id + ' again', {
        title: 'Queue ' + b.id + ' again?', label: 'Queue again',
        body: html`<p>Lanes ${(b.lanes || []).join(', ')} as a new batch, if their scripts haven't changed since. It's unsigned and carries none of the old batch's approvals.</p>`,
      })}>Queue again</button>` : ''}
      ${canRm ? html` <button class="btn sm danger" onClick=${() => cmd.run({ kind: 'dequeue', pin: { batch: b.id } }, 'Remove ' + b.id, {
        title: 'Remove ' + b.id + ' from the queue?', label: 'Remove', danger: true,
        body: html`<p>${b.state === 'queued' ? "It won't run." : 'Its record leaves the queue.'} The lanes and their scripts stay as they are.</p>`,
      })}>Remove</button>` : ''}`;
  };
  const ctl = canRm || canAgain;
  return html`<${Crumbs} e=${e}><span>/</span><span>queue</span></${Crumbs}>
    <div class="head"><h1>Queue</h1></div>
    ${cmd.note}${cmd.dialog}
    <div class="panel">${(e.queue || []).length === 0 ? html`<div class="empty">The queue is empty.${can(me, e, 'queue') ? html` Queue lanes from the <a href=${href('e', e.id, 'lanes')}>Lanes</a> page.` : ''}</div>` : html`<div class="tw"><table>
      <thead><tr><th>#</th><th>Batch</th><th>State</th><th>When</th><th>Lanes</th><th>By</th><th>Note</th>${ctl ? html`<th></th>` : ''}</tr></thead>
      <tbody>${e.queue.map((b) => html`<tr>
        <td class="mono">${b.seq || ''}</td><td class="mono">${b.id}</td>
        <td><${Pill} state=${b.state === 'done' && b.result ? b.result : b.state} label=${b.state + (b.result ? ': ' + b.result : '')}/>
          ${b.daily ? html`<div class="sub">${b.daily}</div>` : b.at ? html`<div class="sub">at ${b.at}</div>` : ''}
          ${b.held_by ? html`<div class="sub">held until ${b.held_by} has run</div>` : ''}</td>
        <td>${b.finished ? html`<${When} t=${b.finished} label="ended"/>` : b.started ? html`<${When} t=${b.started} label="started"/>` : b.queued ? html`<${When} t=${b.queued} label="queued"/>` : ''}
          ${b.run ? html`<div class="sub"><a href=${href('e', e.id, 'runs', b.run)}>${b.run}</a></div>` : ''}</td>
        <td><div class="lanes-cell">${(b.lanes || []).map((l) => html`<span class="chip">${l}</span>`)}</div></td>
        <td>${b.by || ''}</td><td class="muted">${b.note || ''}</td>${ctl ? html`<td class="nowrap">${actions(b)}</td>` : ''}</tr>`)}
      </tbody></table></div>`}</div>`;
}

// Runner is what the hub knows of a runner: where it reports from, when
// it first connected, its runs; then its effective configuration, as swim
// config shows it there, from its last report. All read only: the hub
// can't change any of it. The config is for a hub user (masked of secrets,
// it still names paths and env defaults).
function Runner({ r, store }) {
  const e = useEnrollment(store, r.id);
  const q = useFetch(api('enrollments', r.id, 'config'));
  useTitle(e ? e.name + ' runner' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This runner"/>`;
  const ri = e.runner || {};
  const at = (t) => (t ? html`<span title=${when(t)}>${when(t)}</span>` : '—');
  const kv = [
    ['Host', e.host], ['Address', ri.addr || '—'], ['Enrolled from', ri.from || '—'],
    ['First connected', at(ri.enrolled_at)], ['Admitted', ri.admitted_at ? html`${at(ri.admitted_at)}${ri.admitted_by ? html`<div class="sub">by ${ri.admitted_by}</div>` : ''}` : '—'],
    ['Last report', html`<${Ago} t=${e.seen}/>`], ['Runs', html`${ri.runs || 0}${ri.kept_runs !== ri.runs ? html`<div class="sub">${ri.kept_runs || 0} kept on the hub</div>` : ''}`],
    ['Last run', ri.last_run ? html`<${Ago} t=${ri.last_run}/>` : '—'],
    ['Access', e.access || '—'], ['Mode', e.headless ? 'headless (' + e.headless.mode + ')' : 'swim watch'],
    ['Version', e.version || '—'], ['PID', e.pid || '—'], ['Key', e.key_fp ? html`<span class="mono">${e.key_fp}</span>${e.approved ? '' : html`<div class="sub">not approved</div>`}` : '—'],
  ];
  return html`<${Crumbs} e=${e}><span>/</span><span>runner</span></${Crumbs}>
    <div class="head"><h1>Runner</h1><span class="muted mono">${e.root}</span></div>
    <div class="panel"><dl class="kv">${kv.map(([k, v]) => html`<div><dt>${k}</dt><dd>${v}</dd></div>`)}</dl></div>
    <div class="head sub-head"><h2>Configuration</h2><span class="muted">as <code>swim config</code> shows it on ${e.host}, read only</span>
      <span class="spacer"></span><button class="btn sm" onClick=${q.reload}>Refresh</button></div>
    ${q.loading && !q.data ? html`<${Loading}/>` : !q.data ? html`<${Problem} r=${q} what="This runner's configuration"/>` : html`
    <div class="panel">${q.data.config
      ? html`<pre class="config">${q.data.config}</pre>`
      : html`<div class="empty">This runner hasn't reported its configuration (a swim from before the hub showed it, or no report yet).</div>`}</div>`}
    <p class="muted">Change it on the runner: its config.yml, or a commit to the repo's swim.yml; <code>swim hub access</code> sets what the hub may do.</p>`;
}

// Activity is every runner's runs, newest first: what failed (or only
// dry-ran its change) in the last day across everything, filterable by
// status, repo, trigger and time.
function Activity({ store }) {
  useTitle('Activity');
  const [f, setF] = useState({ since: '24h', status: '', ns: '', trigger: '' });
  const q = new URLSearchParams(Object.entries(f).filter(([, v]) => v)).toString();
  const res = useFetch('/api/activity?' + q, [Object.values(store.runs || {}).reduce((a, b) => a + b, 0)]);
  const repos = byNamespace(store.enrollments || []).map(([ns]) => ns);
  const sel = (k, opts) => html`<select value=${f[k]} onChange=${(ev) => setF({ ...f, [k]: ev.target.value })} aria-label=${k}>
    ${opts.map(([v, l]) => html`<option value=${v}>${l}</option>`)}</select>`;
  const rows = res.data ? res.data.runs : [];
  return html`<div class="head"><h1>Activity</h1><span class="muted">${res.data ? rows.length + (res.data.more ? '+' : '') + ' runs' : ''}</span></div>
    <div class="filters">
      ${sel('status', [['', 'any result'], ['failed', 'failed'], ['dry run', 'dry run'], ['passed', 'passed']])}
      ${sel('since', [['1h', 'last hour'], ['24h', 'last 24 hours'], ['7d', 'last 7 days'], ['30d', 'last 30 days'], ['all', 'everything kept']])}
      ${sel('ns', [['', 'every repo'], ...repos.map((ns) => [ns || '-', ns || 'no repo'])])}
      ${sel('trigger', [['', 'any trigger'], ['schedule', 'schedule'], ['hub', 'hub'], ['queue', 'queue'], ['cli', 'swim run']])}
    </div>
    <div class="panel">${res.loading && !res.data ? html`<${Loading}/>` : !res.data ? html`<${Problem} r=${res} what="Activity"/>`
      : rows.length === 0 ? html`<div class="empty">No runs match.</div>` : html`<div class="tw"><table>
      <thead><tr><th class="i"></th><th>When</th><th>Result</th><th>Repo · runner</th><th>Run</th><th>Lanes</th><th>Trigger</th></tr></thead>
      <tbody>${rows.map((x) => {
        const t = x.finished && !String(x.finished).startsWith('0001') ? x.finished : x.started;
        const to = href('e', x.worker, 'runs', x.run);
        return html`<tr class="link" onClick=${go(to)}>
          <td class="i"><span class="dot ${exitClass(x.status)}"></span></td>
          <td class="nowrap"><${When} t=${t}/></td>
          <td><${Pill} state=${x.status}/></td>
          <td>${x.namespace || 'no repo'}<div class="sub">${x.name} · ${x.host}</div></td>
          <td class="mono"><a href=${to}>${x.run}</a></td>
          <td><div class="lanes-cell">${(x.lane_results || []).map((l) => html`<a class="chip ${exitClass(laneState(l.state, l.dry_run), l.exit)}" href=${href('e', x.worker, 'lanes', l.lane, 'runs', x.run, 'jobs', l.lane)} title=${'lane ' + l.lane + ' ' + laneState(l.state, l.dry_run) + (l.round ? ': ' + l.round : '')}>${l.lane}</a>`)}</div></td>
          <td class="sub">${triggerText(x.trigger)}</td></tr>`;
      })}</tbody></table></div>`}</div>`;
}

// Trend is a job's recent durations as bars, colored by how each run ended.
function Trend({ points }) {
  const max = Math.max(1, ...points.map((p) => p.duration_s || 0));
  const w = 6, gap = 2, h = 22;
  return html`<svg class="trend" width=${points.length * (w + gap)} height=${h} role="img" aria-label="recent durations">
    ${points.map((p, i) => {
      const bh = Math.max(2, Math.round(((p.duration_s || 0) / max) * h));
      return html`<rect class=${exitClass(p.status)} x=${i * (w + gap)} y=${h - bh} width=${w} height=${bh} rx="1"><title>${p.run}: ${p.status}, ${fmtDuration(p.duration_s || 0)}</title></rect>`;
    })}</svg>`;
}

// Jobs are every job across the runners (a runner's lane running one round
// title), failing first: last result, success rate, duration trend.
function Jobs({ store }) {
  useTitle('Jobs');
  const [ns, setNs] = useState('');
  const [query, setQuery] = useState('');
  const res = useFetch('/api/jobs' + (ns ? '?ns=' + encodeURIComponent(ns) : ''), [Object.values(store.runs || {}).reduce((a, b) => a + b, 0)]);
  const repos = byNamespace(store.enrollments || []).map(([n]) => n);
  const ql = query.trim().toLowerCase();
  const rows = res.data ? res.data.jobs.filter((j) => !ql || [j.round, j.name, j.namespace, String(j.lane)].some((v) => String(v || '').toLowerCase().includes(ql))) : [];
  return html`<div class="head"><h1>Jobs</h1><span class="muted">${res.data ? rows.length + ' jobs, from the runs the hub keeps' : ''}</span></div>
    <div class="filters">
      <select value=${ns} onChange=${(ev) => setNs(ev.target.value)} aria-label="repo"><option value="">every repo</option>${repos.map((n) => html`<option value=${n || '-'}>${n || 'no repo'}</option>`)}</select>
      <input class="search" type="search" placeholder="Find a job…" value=${query} onInput=${(ev) => setQuery(ev.target.value)} aria-label="Find a job"/>
    </div>
    <div class="panel">${res.loading && !res.data ? html`<${Loading}/>` : !res.data ? html`<${Problem} r=${res} what="Jobs"/>`
      : rows.length === 0 ? html`<div class="empty">No jobs have run on the hub${ql || ns ? ' that match' : ' yet'}.</div>` : html`<div class="tw"><table>
      <thead><tr><th class="i"></th><th>Job</th><th>Repo · runner</th><th>Last</th><th>Success</th><th>Last passed</th><th>Recent</th></tr></thead>
      <tbody>${rows.map((j) => {
        const to = href('e', j.worker, 'lanes', j.lane);
        return html`<tr class="link" onClick=${go(to)}>
          <td class="i"><span class="dot ${exitClass(j.last_status, j.last_exit)}"></span></td>
          <td><a href=${to}><b>${j.lane}</b></a> ${j.round || ''}</td>
          <td>${j.namespace || 'no repo'}<div class="sub">${j.name} · ${j.host}</div></td>
          <td class="nowrap"><${Pill} state=${j.last_status}/><div class="sub">${ago(j.last_at)}${j.last_exit != null && j.last_exit !== 0 ? ' · exit ' + j.last_exit : ''}</div></td>
          <td class="nowrap"><b>${successRate(j.passed, j.runs)}</b><div class="sub">${j.passed}/${j.runs} runs</div></td>
          <td class="nowrap">${j.last_pass && !String(j.last_pass).startsWith('0001') ? ago(j.last_pass) : html`<span class="muted">never</span>`}</td>
          <td><${Trend} points=${j.recent || []}/></td></tr>`;
      })}</tbody></table></div>`}</div>`;
}

// Schedules are every runner's recurring schedules, worst first: what
// runs when, how its last occurrence went, its last success, when it
// expires, and the hub's verdict: missed (due and not started: the
// runner was down, or busy), failed, skipped, expired, paused, expiring.
function Schedules({ store }) {
  useTitle('Schedules');
  const res = useFetch('/api/schedules', [store.enrollments]);
  if (res.loading && !res.data) return html`<${Loading}/>`;
  if (!res.data) return html`<${Problem} r=${res} what="The schedules"/>`;
  const rows = res.data.schedules;
  const bad = rows.filter((s) => scheduleClass(s.status) === 'bad').length;
  return html`<div class="head"><h1>Schedules</h1><span class="muted">${rows.length} recurring${bad ? html` · <span class="bad">${bad} need${bad === 1 ? 's' : ''} attention</span>` : ''}</span></div>
    <div class="panel">${rows.length === 0 ? html`<div class="empty">No recurring schedules. On a runner: <code>swim schedule LANES 02:00 --daily</code> or <code>swim schedule LANES --cron "0 */6 * * *"</code>.</div>` : html`<div class="tw"><table>
      <thead><tr><th>Status</th><th>Job</th><th>Runner</th><th>Cadence</th><th>Next</th><th>Last</th><th>Last success</th><th>Expires</th></tr></thead>
      <tbody>${rows.map((s) => {
        const cls = scheduleClass(s.status);
        const last = s.last;
        return html`<tr>
          <td class="why"><span class="pill ${cls}">${s.status}</span>${s.why ? html`<div class="sub ${cls}">${s.why}</div>` : ''}</td>
          <td title=${s.id}>${s.lanes.map((n, i) => html`<div><a href=${href('e', s.worker, 'lanes', n)}><b>${n}</b></a> <span class="sub">${(s.rounds || [])[i] || ''}</span></div>`)}
            ${s.note ? html`<div class="sub">${s.note}</div>` : ''}</td>
          <td><a href=${href('e', s.worker, 'lanes')}>${s.name}</a><div class="sub">${s.namespace || s.host}</div></td>
          <td class="nowrap">${s.cadence}</td>
          <td class="nowrap">${s.next ? html`<span title=${when(s.next)}><b>${Date.parse(s.next) < Date.now() ? 'due ' + ago(s.next) : until(s.next)}</b></span><div class="sub">${when(s.next)}</div>` : html`<span class="muted">—</span>`}</td>
          <td class="nowrap">${last ? html`<${Pill} state=${last.result || last.state} label=${last.result || last.state}/>
            <div class="sub">${last.run ? html`<a href=${href('e', s.worker, 'lanes', s.lanes[0], 'runs', last.run)}>${last.occurrence}</a>` : last.occurrence}${last.finished ? ' · ' + ago(last.finished) : ''}</div>` : html`<span class="muted">never ran</span>`}</td>
          <td class="nowrap">${s.last_success ? html`<${When} t=${s.last_success}/>` : html`<span class="muted">never</span>`}</td>
          <td class="nowrap ${s.status === 'expiring' || s.status === 'expired' ? cls : ''}">${s.until ? (s.expired ? 'expired ' : '') + s.until : html`<span class="muted">never</span>`}${s.paused ? html`<div class="sub wait">paused</div>` : ''}</td></tr>`;
      })}</tbody></table></div>`}</div>
    <p class="muted">A schedule is <b>missed</b> when an occurrence is ${Math.round((res.data.grace_s || 600) / 60)} minutes past due and hasn't started: its runner was down then, or busy. Each occurrence is an ordinary batch on the runner's queue.</p>`;
}

function Approvals({ store }) {
  useTitle('Approvals');
  if (store.enrollments === null) return html`<${Loading}/>`;
  const waiting = store.enrollments.flatMap((e) => (e.pending || []).map((p) => ({ e, p })));
  return html`<div class="head"><h1>Approvals</h1><span class="muted">guards waiting for an answer, on every enrollment</span></div>
    <div class="panel">${waiting.length === 0 ? html`<div class="empty">Nothing is waiting for an answer.</div>` : html`<div class="tw"><table>
      <thead><tr><th>Enrollment</th><th>Guard</th><th>Lane</th><th>Waits</th><th></th></tr></thead>
      <tbody>${waiting.map(({ e, p }) => html`<tr>
        <td><a href=${href('e', e.id, 'lanes')}>${e.name}</a><div class="sub">${e.host}</div></td>
        <td><span class="mono">${p.flag}</span><div class="sub">${p.desc}</div>${p.need ? html`<div class="sub">needs ${p.need}</div>` : ''}</td>
        <td>${p.lane}${p.cell ? ' · ' + p.cell : ''}</td>
        <td class="nowrap" title=${'until ' + when(p.expires) + '; no answer by then is a decline'}>expires ${until(p.expires)}</td>
        <td><a class="btn sm primary" href=${href('e', e.id, 'questions', p.id)}>Answer</a></td></tr>`)}
      </tbody></table></div>`}</div>`;
}

// Question answers a guard waiting on a headless worker: by a signature
// (any user: the signature is what the worker counts), or by a button
// (an operator, where the worker takes button answers).
function Question({ r, store }) {
  const e = useEnrollment(store, r.id);
  const me = store.me;
  const [decision, setDecision] = useState('approve');
  const [key, setKey] = useState('');
  const [sig, setSig] = useState('');
  const [sent, setSent] = useState(false);
  const cmd = useCommand(e || { id: r.id });
  const q = useFetch(api('enrollments', r.id, 'questions', r.req) + '?decision=' + decision);
  useTitle('Answer ' + r.req);
  const waiting = e && (e.pending || []).some((p) => p.id === r.req);
  const head = html`<${Crumbs} e=${e}><span>/</span><a href="/approvals">approvals</a><span>/</span><span>${r.req}</span></${Crumbs}>`;
  if (e && !waiting) {
    return html`${head}<div class="panel"><div class="empty">${sent ? 'Answered: ' : ''}this guard isn't waiting any more (answered, timed out, or withdrawn).
      <a href=${href('e', e.id, 'lanes')}>Back to the lanes</a></div></div>${cmd.note}`;
  }
  if (q.loading && !q.data) return html`${head}<${Loading}/>`;
  if (!q.data) return html`${head}<${Problem} r=${q} what="This question"/>`;
  const p = q.data.question;
  const answer = (dec, signed) => {
    setSent(true);
    const c = { kind: 'answer', pin: { request: p.id, evidence: p.evidence_sha256 || '' }, answer: { decision: dec } };
    if (signed) Object.assign(c.answer, { key: key.trim(), sig: sig.trim() });
    cmd.run(c, (signed ? 'Signed ' : '') + dec);
  };
  const operator = isOperator(me) && can(me, e, 'answer');
  return html`${head}
    <div class="head"><${Icon} state="waiting"/><h1>Lane ${p.lane}${p.cell ? ' [' + p.cell + ']' : ''} asks to approve <code>${p.flag}</code></h1></div>
    ${cmd.note}${cmd.dialog}
    <div class="panel">
      <div class="sum">
        <div><div class="k">Worker</div><div class="v">${e ? e.name : r.id}</div></div>
        <div><div class="k">Run</div><div class="v mono" title=${p.run}>${p.run || '—'}</div></div>
        <div><div class="k">Script</div><div class="v mono" title=${p.script_sha256}>${short(p.script_sha256) || '—'}</div></div>
        <div><div class="k">Evidence</div><div class="v mono" title=${p.evidence_sha256}>${short(p.evidence_sha256) || '—'}</div></div>
        <div><div class="k">Expires</div><div class="v" title=${when(p.expires)}>${until(p.expires)}</div></div>
      </div>
      <div class="panel-b"><p>The lane says: ${p.desc}</p>
        ${(p.signers || []).length ? html`<p class="muted">Signed so far: ${p.signers.join(', ')}${p.need ? ' · still ' + p.need : ''}</p>` : ''}
        ${p.evidence ? html`<h2>Evidence: what it will do</h2><pre class="tail">${p.evidence}</pre>` : ''}</div>
    </div>
    ${!me || !me.user ? html`<div class="panel"><div class="empty">Answering takes a hub user. <a href="/login">Log in</a></div></div>` : !can(me, e, 'answer') ? html`<div class="note">This worker doesn't take answers from the hub (swim.yml headless.allow).</div>` : html`
    ${operator ? html`<div class="panel"><div class="panel-h"><h2>Answer with a button</h2><span class="muted">as ${me.user}, an operator</span></div>
      <div class="panel-b actions">
        ${!p.signed_only ? html`<button class="btn primary" onClick=${() => answer('approve', false)}>Approve</button>` : html`<span class="muted">Only a signed answer approves this guard.</span>`}
        <button class="btn danger" onClick=${() => cmd.run({ kind: 'answer', pin: { request: p.id, evidence: p.evidence_sha256 || '' }, answer: { decision: 'decline' } }, 'Decline', {
          title: 'Decline ' + p.flag + '?', label: 'Decline', danger: true, body: html`<p class="muted">The guarded step runs as a dry run.</p>` })}>Decline</button>
      </div></div>` : ''}
    <div class="panel"><div class="panel-h"><h2>Sign your answer</h2>
      <div class="seg" role="radiogroup" aria-label="Decision">${['approve', 'decline'].map((d) => html`<button role="radio" aria-checked=${decision === d} class=${decision === d ? 'on' : ''} onClick=${() => setDecision(d)}>${d}</button>`)}</div></div>
      <div class="panel-b">
        <p class="muted">The worker checks your signature of exactly this statement against its lookup.yml. Run this with your key, then paste your public key and <code>answer.txt.sig</code>. Or from a terminal: <code>${q.data.terminal}</code></p>
        <pre class="tail">${q.data.command}</pre>
        <button class="btn sm" onClick=${() => navigator.clipboard && navigator.clipboard.writeText(q.data.command)}>Copy</button>
        <label class="field">Your public key<textarea rows="2" value=${key} onInput=${(ev) => setKey(ev.target.value)} placeholder="ssh-ed25519 AAAA… you@host (your .pub)"></textarea></label>
        <label class="field">The signature<textarea rows="7" value=${sig} onInput=${(ev) => setSig(ev.target.value)} placeholder="-----BEGIN SSH SIGNATURE-----"></textarea></label>
        <button class="btn primary sign-send" disabled=${!key.trim() || !sig.trim()} onClick=${() => answer(decision, true)}>Send the signed ${decision}</button>
      </div></div>`}`;
}

// LiveLog is a job's log while its run isn't on the hub yet: what the
// worker streamed (hub.logs: stream) or uploaded when asked, growing with
// each log event, following the end unless you've scrolled up.
function LiveLog({ r, store }) {
  const e = useEnrollment(store, r.id);
  const me = store.me;
  const path = api('enrollments', r.id, 'runs', r.run, 'jobs', r.lane, 'log');
  const [text, setText] = useState('');
  const [status, setStatus] = useState(0);
  const [final, setFinal] = useState(false);
  const [follow, setFollow] = useState(true);
  const bytes = useRef(0);
  const busy = useRef(false);
  const load = async (from) => {
    if (busy.current) return;
    busy.current = true;
    const res = await getText(path, from);
    busy.current = false;
    if (from > 0 && res.status === 206) {
      bytes.current += utf8Length(res.text);
      setText((t) => t + res.text);
    } else if (res.status === 200) {
      bytes.current = utf8Length(res.text);
      setText(res.text);
    }
    if (res.status !== 416 && res.status !== 206) setStatus(res.status);
    else setStatus(200);
  };
  useEffect(() => { load(0); }, [path]);
  useEffect(() => onLog((l) => {
    if (l.enrollment !== r.id || l.run !== r.run || l.lane !== r.lane) return;
    if (l.final) setFinal(true);
    load(l.bytes < bytes.current ? 0 : bytes.current);
  }), [path]);
  useEffect(() => {
    const f = () => setFollow(innerHeight + scrollY >= document.documentElement.scrollHeight - 48);
    addEventListener('scroll', f, { passive: true });
    return () => removeEventListener('scroll', f);
  }, []);
  useEffect(() => { if (follow && text) scrollTo(0, document.documentElement.scrollHeight); }, [text]);
  const l = e && (e.lanes || []).find((x) => x.lane === r.lane && x.run === r.run);
  const lines = text ? text.replace(/\n$/, '').split('\n') : [];
  const cmd = useCommand(e || { id: r.id });
  return html`<div class="head"><${Icon} state=${l ? l.state : final ? 'done' : 'running'}/><h1>${(l && l.round) || 'lane ' + r.lane}</h1>
      ${l ? html`<${Pill} state=${l.state}/>` : ''}<span class="muted">run ${r.run}</span></div>
    ${cmd.note}
    <div class="panel">
      <div class="toolbar">
        ${l && l.state === 'running' ? html`<span class="live"><span class="dot run"></span>${l.step ? 'running: ' + l.step : 'running'}${l.progress ? ' · ' + l.progress : ''}</span>`
          : html`<span class="muted">${final ? 'The round has ended; its full record shows here once the run reaches the hub.' : 'The log as the hub has it.'}</span>`}
        <span class="spacer"></span>
        <a class="btn sm" href=${path} target="_blank" rel="noopener">Raw log</a>
      </div>
      ${status === 404 ? html`<div class="panel-b"><div class="note">${e && e.logs === 'stream'
        ? 'Nothing yet: the worker sends the log as each step ends.'
        : html`This run isn't on the hub yet, and neither is its log: this repo sends logs only when asked.
          ${can(me, e, 'log') ? html` <button class="btn sm" onClick=${() => cmd.run({ kind: 'log', lane: r.lane, pin: { run: r.run } }, 'Fetch the log')}>Fetch log</button>` : ''}`}</div></div>`
        : status === 401 ? html`<div class="empty">A job's log needs a hub user. <a href="/login">Log in</a></div>` : ''}
      ${lines.length > 0 && html`<div class="log">${lines.map((t, i) => html`<div id=${'L' + (i + 1)} class="ln"><a href=${'#L' + (i + 1)}>${i + 1}</a><span>${t}</span></div>`)}</div>`}
    </div>
    ${!follow && lines.length > 0 && html`<button class="btn jump" onClick=${() => { setFollow(true); scrollTo(0, document.documentElement.scrollHeight); }}>Jump to end ↓</button>`}`;
}

// Admin is what the admin token does, for an admin user: the hub's users,
// which workers' keys are approved for secrets, and the grants made to
// them. A grant itself is made with swim secret grant, which encrypts the
// value to the worker's key on the admin's machine: the hub never holds a
// secret's value, so the browser never sends one.
function Admin({ store }) {
  useTitle('Admin');
  const me = store.me;
  const users = useFetch('/api/users');
  const grants = useFetch('/api/grants');
  const pending = useFetch('/api/pending', [store.enrollments]);
  const [note, setNote] = useState(null);
  const [ask, setAsk] = useState(null);
  const [form, setForm] = useState({ name: '', role: 'viewer', tailscale: '' });
  if (!me || me.user === undefined) return html`<${Loading}/>`;
  if (!isAdmin(me)) {
    return html`<div class="panel"><div class="empty">This page is for the hub's admins. <a href="/">All repos</a></div></div>`;
  }
  const act = async (what, method, path, body, after) => {
    setNote({ cls: 'wait', text: what + '…' });
    const r = await adminCall(method, path, body);
    if (!r.ok) {
      setNote({ cls: 'bad', text: what + ': ' + r.error });
      return;
    }
    setNote(after ? after(r.data) : { cls: 'ok', text: what + ': done.' });
    users.reload();
    grants.reload();
    pending.reload();
  };
  const confirm = (c) => setAsk(c);
  const addUser = (ev) => {
    ev.preventDefault();
    const body = { name: form.name.trim(), role: form.role, tailscale: form.tailscale.trim() };
    act('Add ' + body.name, 'POST', '/api/users', body, (d) => ({
      cls: 'ok', text: (body.tailscale ? d.name + ' signs in with Tailscale as ' + body.tailscale + '. ' : '') + d.name + "'s token (shown once; they log in with it at /login, or use it with swim hub):",
      token: d.token,
    }));
    setForm({ name: '', role: 'viewer', tailscale: '' });
  };
  const list = (users.data && users.data.users) || [];
  const workers = store.enrollments || [];
  const gs = (grants.data && grants.data.grants) || [];
  const waiting = (pending.data && pending.data.pending) || [];
  return html`<div class="head"><h1>Admin</h1><span class="muted">users, worker approvals and secret grants</span></div>
    ${ask && html`<${Confirm} c=${ask} close=${() => setAsk(null)}/>`}
    ${note && html`<div class="note ${note.cls}" role="status">${note.text}${note.token ? html` <code class="mono">${note.token}</code>` : ''}</div>`}
    <h2>Waiting to join <span class="count ${waiting.length ? 'hot' : ''}">${waiting.length}</span></h2>
    <div class="panel">${pending.loading && !pending.data ? html`<${Loading}/>` : pending.error ? html`<div class="err">${pending.error}</div>` : waiting.length === 0 ? html`<div class="empty">No one is waiting. A swim enrolls with <code>swim hub enroll ${location.origin}</code> and shows here until you admit it.</div>` : html`<div class="tw"><table>
      <thead><tr><th>Worker</th><th>Access</th><th>Namespace</th><th>Key</th><th>From</th><th>Since</th><th></th></tr></thead>
      <tbody>${waiting.map((p) => html`<tr>
        <td><b>${p.name}</b><div class="sub mono">${p.host}:${p.root}</div></td>
        <td>${p.access ? html`<span class="pill ${p.access === 'readwrite' ? 'wait' : ''}">${p.access}</span>` : ''}</td>
        <td class="mono">${p.namespace || ''}</td><td class="mono">${p.key_fp || html`<span class="muted">none</span>`}</td>
        <td class="mono muted">${p.from || ''}</td><td class="muted">${p.enrolled_at ? when(p.enrolled_at) : ''}</td>
        <td class="nowrap"><button class="btn sm primary" onClick=${() => confirm({ title: 'Admit ' + p.name,
          body: html`Let <b>${p.name}</b> (${p.host}:${p.root}) report to this hub? It enrolled with <b>${p.access || 'unknown'}</b> access${p.access === 'readwrite' ? ': it takes every command from this hub (run, queue, interrupt, answers…) its swim.yml allows' : p.access === 'read' ? ': the hub only shows it and fetches its logs' : ''}. ${p.key_fp ? html`Its key is <code>${p.key_fp}</code>: check it against what <code>swim hub enroll</code> printed there.` : ''}`,
          label: 'Admit', run: () => act('Admit ' + p.name, 'POST', api('workers', p.id, 'admit')) })}>Admit</button>
          <button class="btn sm danger" onClick=${() => confirm({ title: 'Turn away ' + p.name, body: 'Its enrollment is forgotten; it can enroll and wait again.', label: 'Turn away', danger: true,
            run: () => act('Turn away ' + p.name, 'DELETE', api('workers', p.id, 'admit')) })}>Turn away</button></td></tr>`)}
      </tbody></table></div>`}</div>
    <h2>Users</h2>
    <div class="panel">${users.loading && !users.data ? html`<${Loading}/>` : users.error ? html`<div class="err">${users.error}</div>` : html`<div class="tw"><table>
      <thead><tr><th>Name</th><th>Role</th><th>Tailscale</th><th>Added</th><th></th></tr></thead>
      <tbody>${list.map((u) => html`<tr>
        <td><b>${u.name}</b>${u.name === me.user ? html` <span class="muted">(you)</span>` : ''}</td>
        <td><span class="pill">${u.role}</span></td><td class="mono">${u.tailscale || ''}</td><td class="muted">${u.added ? when(u.added) : ''}</td>
        <td>${u.name !== me.user ? html`<button class="btn sm danger" onClick=${() => confirm({
          title: 'Remove ' + u.name, body: 'Their token stops working and their sessions end.', label: 'Remove', danger: true,
          run: () => act('Remove ' + u.name, 'DELETE', api('users', u.name)) })}>Remove</button>` : ''}</td></tr>`)}
      </tbody></table></div>`}
      <form class="admin-add" onSubmit=${addUser}>
        <input required placeholder="name (as lookup.yml knows them)" value=${form.name} onInput=${(e) => setForm({ ...form, name: e.target.value })}/>
        <select value=${form.role} onChange=${(e) => setForm({ ...form, role: e.target.value })}>
          <option value="viewer">viewer</option><option value="operator">operator</option><option value="admin">admin</option></select>
        <input placeholder="Tailscale login (optional)" value=${form.tailscale} onInput=${(e) => setForm({ ...form, tailscale: e.target.value })}/>
        <button class="btn primary">Add user</button>
      </form>
      <div class="sub">Adding a name again gives them a new token and role, and ends their sessions.</div></div>
    <h2>Workers</h2>
    <div class="panel">${workers.length === 0 ? html`<div class="empty">No enrollments yet.</div>` : html`<div class="tw"><table>
      <thead><tr><th>Worker</th><th>Key for secrets</th><th>Approved</th><th>Grants</th><th></th></tr></thead>
      <tbody>${workers.map((e) => html`<tr>
        <td><a href=${href('e', e.id, 'lanes')}>${e.name}</a><div class="sub">${e.host} · ${e.namespace || 'no namespace'}${e.access ? ' · ' + e.access : ''}</div></td>
        <td class="mono">${e.key_fp || html`<span class="muted">none</span>`}</td>
        <td>${e.approved ? html`<span class="pill ok">approved</span>` : e.key_fp ? html`<span class="pill wait">pending</span>` : ''}</td>
        <td>${(e.grants || []).join(', ')}</td>
        <td>${e.key_fp && (e.approved
          ? html`<button class="btn sm danger" onClick=${() => confirm({ title: 'Withdraw ' + e.name, body: 'Its approval ends and its grants are deleted.', label: 'Withdraw', danger: true,
              run: () => act('Withdraw ' + e.name, 'DELETE', api('workers', e.id, 'approve')) })}>Withdraw</button>`
          : html`<button class="btn sm primary" onClick=${() => confirm({ title: 'Approve ' + e.name, body: html`Approve key <code>${e.key_fp}</code> on ${e.host} for secrets? Check the fingerprint against what <code>swim hub enroll</code> printed there.`, label: 'Approve',
              run: () => act('Approve ' + e.name, 'POST', api('workers', e.id, 'approve')) })}>Approve</button>`)}</td></tr>`)}
      </tbody></table></div>`}</div>
    <h2>Secret grants</h2>
    <div class="panel">${grants.loading && !grants.data ? html`<${Loading}/>` : grants.error ? html`<div class="err">${grants.error}</div>` : gs.length === 0 ? html`<div class="empty">No grants.</div>` : html`<div class="tw"><table>
      <thead><tr><th>Worker</th><th>Secret</th><th>Guard</th><th>By</th><th>When</th><th></th></tr></thead>
      <tbody>${gs.map((g) => html`<tr><td>${g.name}</td><td class="mono">${g.secret}</td><td class="mono">${(g.guard || []).join(', ')}</td><td>${g.by || ''}</td><td class="muted">${g.at ? when(g.at) : ''}</td>
        <td><button class="btn sm danger" onClick=${() => confirm({ title: 'Revoke ' + g.secret, body: g.name + ' loses it at its next fetch.', label: 'Revoke', danger: true,
          run: () => act('Revoke ' + g.secret, 'DELETE', api('grants', g.worker, g.secret)) })}>Revoke</button></td></tr>`)}
      </tbody></table></div>`}
      <div class="sub">To grant a secret: <code>swim secret grant WORKER NAME</code> (log in first with <code>swim hub login ${location.origin} --admin-token</code> and your token). It encrypts the value to the worker's key on your machine, so the hub never sees it.</div></div>`;
}

function NotFound() {
  useTitle('Not found');
  return html`<div class="panel"><div class="empty">No such page. <a href="/">All repos</a></div></div>`;
}

// ---- the app ------------------------------------------------------------

const views = { activity: Activity, jobs: Jobs, schedules: Schedules, home: Home, repo: Repo, history: History, lanes: Lanes, queue: Queue, runner: Runner, approvals: Approvals, question: Question, admin: Admin, notfound: NotFound };

function App() {
  const path = usePath();
  const store = useStore();
  const r = route(path);
  const View = views[r.name];
  const v = store.me && store.me.version;
  return html`<${TopBar} r=${r} store=${store}/><main><${View} key=${path} r=${r} store=${store}/></main>
    ${v && html`<footer class="foot">swim hub · swim ${v}</footer>`}`;
}

start();
render(html`<${App}/>`, document.getElementById('app'));

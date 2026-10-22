// The swim hub app: a top bar (enrollment switcher, tabs, live state,
// user) over one view per route (lib.js route). It reads the hub's read
// API and event stream (api.js) and renders everything through htm, which
// escapes text: nothing a worker sends is ever parsed as HTML.
import { html, render, useEffect, useMemo, useRef, useState } from './vendor/preact-htm.module.js';
import { adminCall, get, getText, onLog, post, send, start, useFetch, useStore } from './api.js';
import {
  ago, api, between, byNamespace, can, enrollmentState, fmtDuration, hasOutput, href, isAdmin, isOperator,
  layoutGraph, lineMatches, matchEnrollment, route, stateClass, steps, until, utf8Length,
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

  const pick = (e) => { setOpen(false); navigate(href('e', e.id, 'runs')); };
  const keys = (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(flat.length - 1, s + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === 'Enter' && flat[sel]) pick(flat[sel]);
  };
  const cur = current && (store.enrollments || []).find((e) => e.id === current);
  const st = cur && enrollmentState(cur);
  return html`<div class="switch" ref=${box}>
    <button class="switch-btn" onClick=${() => setOpen(!open)} aria-haspopup="listbox" aria-expanded=${open}>
      ${cur ? html`<span class="dot ${st.cls}"></span><span class="name">${cur.name}</span>` : html`<span class="name">All enrollments</span>`}
      <span class="kbd">⌘K</span>
    </button>
    ${open && html`<div class="pop" role="listbox">
      <input ref=${input} value=${q} placeholder="Find an enrollment…" onInput=${(e) => { setQ(e.target.value); setSel(0); }} onKeyDown=${keys} aria-label="Find an enrollment"/>
      <div class="pop-list">
        <a class="pop-item" href="/" onClick=${() => setOpen(false)}>All enrollments</a>
        ${flat.length === 0 && html`<div class="pop-empty">No enrollment matches.</div>`}
        ${groups.map(([ns, es]) => html`
          <div class="pop-ns">${ns || 'no namespace'}</div>
          ${es.map((e) => {
            const s = enrollmentState(e);
            return html`<a class="pop-item ${flat[sel] === e ? 'sel' : ''}" href=${href('e', e.id, 'runs')} onClick=${() => setOpen(false)}>
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
  const tab = (name, label, to) => html`<a class=${r.name === name || (name === 'runs' && ['run', 'job'].includes(r.name)) ? 'on' : ''} href=${to}>${label}</a>`;
  const live = { live: ['ok', 'Live'], connecting: ['wait', 'Connecting'], offline: ['bad', 'Offline'] }[store.live];
  return html`<header class="top"><div class="top-in">
    <a class="brand" href="/"><img src="/assets/favicon.svg" alt=""/><span>swim hub</span></a>
    <${Switcher} store=${store} current=${id}/>
    <nav class="tabs">
      ${id && tab('runs', 'Runs', href('e', id, 'runs'))}
      ${id && tab('lanes', 'Lanes', href('e', id, 'lanes'))}
      ${id && tab('queue', 'Queue', href('e', id, 'queue'))}
      <a class=${r.name === 'approvals' ? 'on' : ''} href="/approvals">Approvals <span class="count ${waiting ? 'hot' : ''}">${waiting}</span></a>
      ${isAdmin(store.me) && html`<a class=${r.name === 'admin' ? 'on' : ''} href="/admin">Admin</a>`}
    </nav>
    <span class="spacer"></span>
    <span class="live" title="Updates from the hub as they happen"><span class="dot ${live[0]}"></span><span class="t">${live[1]}</span></span>
    <${User} me=${store.me}/>
  </div></header>`;
}

// ---- views ------------------------------------------------------------

function Home({ store }) {
  useTitle('');
  if (store.enrollments === null) return store.error ? html`<div class="err">${store.error}</div>` : html`<${Loading}/>`;
  if (store.enrollments.length === 0) {
    return html`<div class="panel"><div class="empty">No enrollments yet. In a repo:
      <code>swim hub enroll ${location.origin} --token …</code>, then <code>swim watch</code>.</div></div>`;
  }
  return html`<div class="head"><h1>Enrollments</h1><span class="muted">${store.enrollments.length}</span></div>
    ${byNamespace(store.enrollments).map(([ns, es]) => html`
      <div class="ns-h">${ns || 'no namespace'}</div>
      <div class="cards">${es.map((e) => {
        const s = enrollmentState(e);
        return html`<a class="card ${e.stale ? 'stale' : ''}" href=${href('e', e.id, 'runs')}>
          <div class="card-t"><span class="dot ${s.cls}"></span>${e.name}<span class="pill ${s.cls}">${s.label}</span></div>
          <div class="card-s">${e.host} · ${e.activity}</div>
          <div class="card-l">${(e.lanes || []).map((l) => html`<span class="chip ${stateClass(l.state)}" title=${'swim ' + l.lane + ' ' + l.state + (l.round ? ': ' + l.round : '')}>swim ${l.lane}</span>`)}</div>
        </a>`;
      })}</div>`)}`;
}

function Crumbs({ e, children }) {
  return html`<div class="crumbs"><a href="/">Enrollments</a><span>/</span>
    ${e ? html`<a href=${href('e', e.id, 'runs')}>${e.name}</a>` : html`<span>…</span>`}${children}</div>`;
}

function useEnrollment(store, id) {
  return (store.enrollments || []).find((e) => e.id === id);
}

function Runs({ r, store }) {
  const e = useEnrollment(store, r.id);
  useTitle(e ? e.name : '');
  const [older, setOlder] = useState([]);
  const [next, setNext] = useState(null);
  const res = useFetch(api('enrollments', r.id, 'runs') + '?limit=50', [store.runs[r.id] || 0]);
  useEffect(() => { setOlder([]); setNext(null); }, [r.id]);
  if (res.loading && !res.data) return html`<${Loading}/>`;
  if (!res.data) return html`<${Crumbs} e=${e}/><${Problem} r=${res} what="This enrollment"/>`;
  const runs = [...res.data.runs, ...older];
  const more = next === null ? res.data.next : next;
  const loadMore = async () => {
    const p = await get(api('enrollments', r.id, 'runs') + '?limit=50&before=' + encodeURIComponent(more));
    if (p.data) { setOlder([...older, ...p.data.runs]); setNext(p.data.next); }
  };
  return html`<${Crumbs} e=${e}/>
    <div class="head"><h1>Runs</h1><span class="muted">${e ? e.host + ' · ' + e.root : ''}</span></div>
    <div class="panel">${runs.length === 0
      ? html`<div class="empty">No runs on the hub yet. A run shows here once <code>swim watch</code> has run a batch.</div>`
      : html`<div class="tw"><table>
        <thead><tr><th class="i"></th><th>Run</th><th>Lanes</th><th>Started</th><th>Duration</th></tr></thead>
        <tbody>${runs.map((x) => html`<tr class="link" onClick=${go(href('e', r.id, 'runs', x.run))}>
          <td class="i"><${Icon} state=${x.result}/></td>
          <td><a href=${href('e', r.id, 'runs', x.run)}>${x.run}</a><div class="sub">${x.result}</div></td>
          <td><div class="lanes-cell">${x.lanes.map((l) => html`<span class="chip">swim ${l}</span>`)}</div></td>
          <td class="nowrap"><${Ago} t=${x.started}/></td>
          <td class="nowrap mono">${fmtDuration(between(x.started, x.finished))}</td></tr>`)}
        </tbody></table></div>
        ${more && html`<div class="panel-b"><button class="btn" onClick=${loadMore}>Load older runs</button></div>`}`}
    </div>`;
}

function Graph({ r, run, jobs }) {
  const lanes = run.lanes.map((l) => ({ lane: l.lane, after: l.after || [] }));
  const g = useMemo(() => layoutGraph(lanes), [run.run]);
  const info = new Map(run.lanes.map((l) => [l.lane, l]));
  const hasJob = new Set(jobs.map((j) => j.lane));
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
      const label = (l.round || 'swim ' + n.lane);
      const node = html`<g class="node" transform="translate(${n.x},${n.y})">
        <title>swim ${n.lane}: ${l.round || ''} (${l.state})</title>
        <rect width=${w} height=${h} rx="8"/>
        <rect class="bar ${c}" width="4" height=${h - 16} x="0" y="8" rx="2"/>
        <circle class=${c} cx="18" cy=${h / 2 - 8} r="4.5"/>
        <text x="30" y=${h / 2 - 4}>${label.length > 24 ? label.slice(0, 23) + '…' : label}</text>
        <text class="sub" x="30" y=${h / 2 + 13}>swim ${n.lane} · ${l.state}${dur ? ' · ' + dur : ''}</text></g>`;
      return hasJob.has(n.lane)
        ? html`<a href=${href('e', r.id, 'runs', run.run, 'jobs', n.lane)}>${node}</a>`
        : node;
    })}
  </svg></div>`;
}

function Run({ r, store }) {
  const e = useEnrollment(store, r.id);
  useTitle(r.run);
  const res = useFetch(api('enrollments', r.id, 'runs', r.run));
  if (res.loading && !res.data) return html`<${Loading}/>`;
  if (!res.data) return html`<${Crumbs} e=${e}/><${Problem} r=${res} what="This run"/>`;
  const { run, jobs, result } = res.data;
  const byLane = new Map(jobs.map((j) => [j.lane, j]));
  return html`<${Crumbs} e=${e}><span>/</span><span>${run.run}</span></${Crumbs}>
    <div class="head"><${Icon} state=${result}/><h1>${run.run}</h1><${Pill} state=${result}/>
      <div class="meta"><span>started <${Ago} t=${run.started}/></span><span>${fmtDuration(between(run.started, run.finished))}</span>
      <span>${run.lanes.length} lane${run.lanes.length === 1 ? '' : 's'}</span>${run.max_parallel ? html`<span>at most ${run.max_parallel} at once</span>` : ''}</div></div>
    <div class="panel"><div class="panel-h"><h2>Lanes</h2></div><${Graph} r=${r} run=${run} jobs=${jobs}/></div>
    <div class="panel"><div class="tw"><table>
      <thead><tr><th class="i"></th><th>Lane</th><th>Result</th><th>Steps</th><th>Duration</th><th>Failed steps</th></tr></thead>
      <tbody>${run.lanes.map((l) => {
        const j = byLane.get(l.lane);
        const to = href('e', r.id, 'runs', run.run, 'jobs', l.lane);
        return html`<tr class=${j ? 'link' : ''} onClick=${j ? go(to) : null}>
          <td class="i"><${Icon} state=${l.state}/></td>
          <td>${j ? html`<a href=${to}>swim ${l.lane}</a>` : html`swim ${l.lane}`}<div class="sub">${l.round || ''}</div>
            ${l.reason ? html`<div class="sub">${l.reason}</div>` : ''}</td>
          <td><${Pill} state=${l.state}/>${l.exit != null && l.exit !== 0 ? html` <span class="mono muted">exit ${l.exit}</span>` : ''}</td>
          <td class="nowrap">${j ? html`<span class="ok">${j.counts.pass} passed</span>${j.counts.fail ? html` · <span class="bad">${j.counts.fail} failed</span>` : ''}${j.counts.skip ? html` · <span class="wait">${j.counts.skip} skipped</span>` : ''}` : html`<span class="muted">no round</span>`}</td>
          <td class="nowrap mono">${l.start != null && l.end != null ? fmtDuration(l.end - l.start) : ''}</td>
          <td>${j && j.failed_steps.length ? j.failed_steps.map((f) => html`<div class="mono bad">${f}</div>`) : ''}</td></tr>`;
      })}</tbody></table></div></div>`;
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

function Job({ r, store }) {
  const e = useEnrollment(store, r.id);
  useTitle('swim ' + r.lane + ' · ' + r.run);
  const arrived = store.runs[r.id] || 0; // a run arriving may be this one
  const run = useFetch(api('enrollments', r.id, 'runs', r.run), [arrived]);
  const job = useFetch(api('enrollments', r.id, 'runs', r.run, 'jobs', r.lane), [arrived]);
  const [query, setQuery] = useState('');
  const [all, setAll] = useState(null); // null: failed steps and the linked line open
  const target = location.hash.startsWith('#L') ? Number(location.hash.slice(2)) : 0;
  const groups = useMemo(() => (job.data ? steps(job.data.job.results) : []), [job.data]);
  useEffect(() => { setAll(null); setQuery(''); }, [r.run, r.lane]);
  useEffect(() => {
    if (target && job.data) {
      const el = document.getElementById('L' + target);
      if (el) el.scrollIntoView({ block: 'center' });
    }
  }, [job.data, target]);

  const jobs = run.data ? run.data.jobs : [];
  const d = job.data && job.data.job;
  if (!job.loading && !run.loading && !d && job.status === 404 && run.status === 404) {
    return html`<${Crumbs} e=${e}><span>/</span><span>${r.run}</span><span>/</span><span>swim ${r.lane}</span></${Crumbs}><${LiveLog} r=${r} store=${store}/>`;
  }
  let hits = 0;
  if (query) for (const g of groups) for (const s of g.items) for (const l of s.lines) if (lineMatches(l.text, query)) hits++;
  const isOpen = (s) => {
    if (all !== null) return all;
    if (query) return s.lines.some((l) => lineMatches(l.text, query));
    return stateClass(s.result) === 'bad' || s.lines.some((l) => l.n === target);
  };
  return html`<${Crumbs} e=${e}><span>/</span><a href=${href('e', r.id, 'runs', r.run)}>${r.run}</a><span>/</span><span>swim ${r.lane}</span></${Crumbs}>
    <div class="job">
      <nav class="panel jobs-nav" aria-label="Jobs in this run">
        <div class="panel-h"><h2>Jobs</h2></div>
        ${jobs.map((j) => html`<a class=${j.lane === r.lane ? 'on' : ''} href=${href('e', r.id, 'runs', r.run, 'jobs', j.lane)}>
          <${Icon} state=${j.result}/><span class="t">swim ${j.lane} · ${j.round}</span><span class="d">${j.duration || ''}</span></a>`)}
        ${run.loading && !run.data ? html`<div class="empty">…</div>` : ''}
      </nav>
      <div>${job.loading && !job.data ? html`<${Loading}/>` : !d ? html`<${Problem} r=${job} what="This job's steps"/>` : html`
        <div class="head"><${Icon} state=${d.result}/><h1>${d.round || 'swim ' + r.lane}</h1><${Pill} state=${d.result}/></div>
        <div class="panel">
          <div class="sum">
            <div><div class="k">Lane</div><div class="v">swim ${d.lane}</div></div>
            <div><div class="k">Duration</div><div class="v">${d.duration || '—'}</div></div>
            <div><div class="k">Exit</div><div class="v">${d.exit != null ? d.exit : '—'}</div></div>
            <div><div class="k">Steps</div><div class="v"><span class="ok">${d.counts.pass}</span> / <span class="bad">${d.counts.fail}</span> / <span class="wait">${d.counts.skip}</span></div></div>
            <div><div class="k">Job</div><div class="v mono" title=${d.job}>${(d.job || '').slice(0, 8)}</div></div>
            <div><div class="k">Started</div><div class="v" title=${d.started_at}>${when(d.started_at)}</div></div>
          </div>
          <div class="toolbar">
            <input class="search" type="search" placeholder="Search the output…" value=${query} onInput=${(ev) => { setQuery(ev.target.value); setAll(null); }} aria-label="Search the output"/>
            ${query && html`<span class="muted">${hits} line${hits === 1 ? '' : 's'}</span>`}
            <button class="btn sm" onClick=${() => setAll(true)}>Expand all</button>
            <button class="btn sm" onClick=${() => setAll(false)}>Collapse all</button>
            <a class="btn sm" href=${api('enrollments', r.id, 'runs', r.run, 'jobs', r.lane, 'log')} target="_blank" rel="noopener">Raw log</a>
          </div>
          ${!hasOutput(d.results) && html`<div class="panel-b"><div class="note">No step output on the hub: this repo sends steps without their output. <code>hub: {logs: stream}</code> in its swim.yml sends it.</div></div>`}
          <div class="log">${groups.map((g) => html`
            <div class="stage-h"><span class=${g.failed ? 'bad' : ''}>${g.stage || 'steps'}</span>${d.stages && d.stages[g.stage] ? html`<span>· ${d.stages[g.stage]}</span>` : ''}</div>
            ${g.items.map((s) => html`<${Step} key=${s.i} s=${s} open=${isOpen(s)} query=${query}/>`)}`)}
            ${groups.length === 0 ? html`<div class="mark">No steps recorded.</div>` : ''}
          </div>
        </div>`}
      </div>
    </div>`;
}

// LaneControls are the commands a lane takes now, from this user, on
// this worker (can), each pinned to what the page shows.
function LaneControls({ e, l, me, cmd }) {
  const running = l.state === 'running';
  const lane = 'swim ' + l.lane;
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
  if (l.script_sha256 && !running && can(me, e, 'run')) {
    b.push(html`<button class="btn sm" onClick=${() => cmd.run({ kind: 'run', lane: l.lane, pin: { sha256: l.script_sha256, manifest: l.manifest_sha256 } }, 'Run ' + lane, {
      title: 'Run ' + lane + ' now?', label: 'Run now',
      body: html`<p>${l.round || ''}</p><p class="muted">Queues ${lane}'s script <code>${short(l.script_sha256)}</code> (manifest <code>${short(l.manifest_sha256)}</code>) on ${e.name}, unsigned, by hub:${me.user}, with no approvals of its own: its guards approve only as any unsigned batch's do. If the script has changed, the worker refuses it.</p>`,
    })}>Run now</button>`);
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
        <td>${c.kind}${c.answer ? ' ' + c.answer.decision + (c.answer.sig ? ' (signed)' : '') : ''}${c.lane ? ' swim ' + c.lane : ''}
          ${c.pin.run ? html` <span class="mono muted">${c.pin.run}</span>` : ''}${c.pin.sha256 ? html` <span class="mono muted">${short(c.pin.sha256)}</span>` : ''}
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
  useTitle(e ? e.name + ' lanes' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This enrollment"/>`;
  const drain = can(me, e, 'drain') && html`<button class="btn sm danger" onClick=${() => cmd.run({ kind: 'drain' }, 'Stop ' + e.name + ' after this batch', {
    title: 'Stop ' + e.name + ' after this batch?', label: 'Stop after this batch', danger: true,
    body: html`<p class="muted">The worker finishes the batch it's running, then exits; nothing new starts. Start it again on its machine.</p>`,
  })}>Stop after this batch</button>`;
  return html`<${Crumbs} e=${e}><span>/</span><span>lanes</span></${Crumbs}>
    <div class="head"><h1>Lanes</h1><${Pill} state=${enrollmentState(e).cls === 'run' ? 'running' : e.activity} label=${e.activity}/>
      <div class="meta"><span>reported <${Ago} t=${e.at}/></span>${e.headless ? html`<span>headless (${e.headless.mode})</span>` : ''}
        ${e.logs ? html`<span>logs: ${e.logs}</span>` : ''}</div>
      <span class="spacer"></span>${drain}</div>
    ${cmd.note}${cmd.dialog}
    ${me && me.user && !e.headless ? html`<div class="note">This worker isn't headless (someone is at its terminal), so it takes no commands from the hub.</div>` : ''}
    <div class="panel">${(e.lanes || []).length === 0 ? html`<div class="empty">No lanes in use.</div>` : html`<div class="tw"><table>
      <thead><tr><th class="i"></th><th>Lane</th><th>State</th><th>Step</th><th>Steps</th></tr></thead>
      <tbody>${e.lanes.map((l) => html`<tr>
        <td class="i"><${Icon} state=${l.state}/></td>
        <td>swim ${l.lane}<div class="sub">${l.round || ''}</div></td>
        <td><${Pill} state=${l.state}/>${l.paused ? html`<div class="sub">paused ${l.paused}</div>` : ''}${l.scheduled ? html`<div class="sub">${l.scheduled}</div>` : ''}
          ${l.reason ? html`<div class="sub">${l.reason}</div>` : ''}</td>
        <td>${l.step || ''}${l.progress ? html` <progress max="100" value=${pct(l.progress)}></progress> <span class="sub">${l.progress}</span>` : ''}
          ${l.message ? html`<div class="sub">${l.message}</div>` : ''}${l.cells ? html`<div class="sub">${l.cells}</div>` : ''}</td>
        <td class="nowrap"><span class="ok">${l.pass}</span> / <span class="bad">${l.fail}</span> / <span class="wait">${l.skip}</span>
          ${l.run && e.logs === 'stream' && l.state !== 'waiting' ? html`<div><a href=${href('e', e.id, 'runs', l.run, 'jobs', l.lane)}>${l.state === 'running' ? 'Follow log' : 'Log'}</a></div>` : ''}</td></tr>
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
  useTitle(e ? e.name + ' queue' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This enrollment"/>`;
  return html`<${Crumbs} e=${e}><span>/</span><span>queue</span></${Crumbs}>
    <div class="head"><h1>Queue</h1></div>
    <div class="panel">${(e.queue || []).length === 0 ? html`<div class="empty">The queue is empty.</div>` : html`<div class="tw"><table>
      <thead><tr><th>#</th><th>Batch</th><th>State</th><th>Lanes</th><th>By</th><th>Note</th></tr></thead>
      <tbody>${e.queue.map((b) => html`<tr>
        <td class="mono">${b.seq || ''}</td><td class="mono">${b.id}</td>
        <td><${Pill} state=${b.state} label=${b.state + (b.result ? ': ' + b.result : '')}/>
          ${b.daily ? html`<div class="sub">${b.daily}</div>` : b.at ? html`<div class="sub">at ${b.at}</div>` : ''}
          ${b.held_by ? html`<div class="sub">held until ${b.held_by} has run</div>` : ''}</td>
        <td><div class="lanes-cell">${(b.lanes || []).map((l) => html`<span class="chip">swim ${l}</span>`)}</div></td>
        <td>${b.by || ''}</td><td class="muted">${b.note || ''}</td></tr>`)}
      </tbody></table></div>`}</div>`;
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
        <td>swim ${p.lane}${p.cell ? ' · ' + p.cell : ''}</td>
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
    <div class="head"><${Icon} state="waiting"/><h1>swim ${p.lane}${p.cell ? ' [' + p.cell + ']' : ''} asks to approve <code>${p.flag}</code></h1></div>
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
  return html`<div class="head"><${Icon} state=${l ? l.state : final ? 'done' : 'running'}/><h1>${(l && l.round) || 'swim ' + r.lane}</h1>
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
  const [note, setNote] = useState(null);
  const [ask, setAsk] = useState(null);
  const [form, setForm] = useState({ name: '', role: 'viewer', tailscale: '' });
  if (!me || me.user === undefined) return html`<${Loading}/>`;
  if (!isAdmin(me)) {
    return html`<div class="panel"><div class="empty">This page is for the hub's admins. <a href="/">All enrollments</a></div></div>`;
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
  return html`<div class="head"><h1>Admin</h1><span class="muted">users, worker approvals and secret grants</span></div>
    ${ask && html`<${Confirm} c=${ask} close=${() => setAsk(null)}/>`}
    ${note && html`<div class="note ${note.cls}" role="status">${note.text}${note.token ? html` <code class="mono">${note.token}</code>` : ''}</div>`}
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
        <td><a href=${href('e', e.id, 'lanes')}>${e.name}</a><div class="sub">${e.host} · ${e.namespace || 'no namespace'}</div></td>
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
  return html`<div class="panel"><div class="empty">No such page. <a href="/">All enrollments</a></div></div>`;
}

// ---- the app ------------------------------------------------------------

const views = { home: Home, runs: Runs, run: Run, job: Job, lanes: Lanes, queue: Queue, approvals: Approvals, question: Question, admin: Admin, notfound: NotFound };

function App() {
  const path = usePath();
  const store = useStore();
  const r = route(path);
  const View = views[r.name];
  return html`<${TopBar} r=${r} store=${store}/><main><${View} key=${path} r=${r} store=${store}/></main>`;
}

start();
render(html`<${App}/>`, document.getElementById('app'));

// The swim hub app: a top bar (enrollment switcher, tabs, live state,
// user) over one view per route (lib.js route). It reads the hub's read
// API and event stream (api.js) and renders everything through htm, which
// escapes text: nothing a worker sends is ever parsed as HTML.
import { html, render, useEffect, useMemo, useRef, useState } from './vendor/preact-htm.module.js';
import { get, post, start, useFetch, useStore } from './api.js';
import {
  ago, api, between, byNamespace, enrollmentState, fmtDuration, hasOutput, href, layoutGraph,
  lineMatches, matchEnrollment, route, stateClass, steps,
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
// The app's own links navigate in place; the classic pages, login and
// the API load as pages.
document.addEventListener('click', (e) => {
  const a = e.target.closest && e.target.closest('a');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (a.target || a.hasAttribute('download') || a.origin !== location.origin) return;
  const p = a.pathname;
  if (p.startsWith('/api/') || p.startsWith('/classic') || p.startsWith('/w/') || p === '/login' || p.startsWith('/assets/')) return;
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
  const run = useFetch(api('enrollments', r.id, 'runs', r.run));
  const job = useFetch(api('enrollments', r.id, 'runs', r.run, 'jobs', r.lane));
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

function Lanes({ r, store }) {
  const e = useEnrollment(store, r.id);
  useTitle(e ? e.name + ' lanes' : '');
  if (store.enrollments === null) return html`<${Loading}/>`;
  if (!e) return html`<${Crumbs}/><${Problem} r=${{ status: 404 }} what="This enrollment"/>`;
  return html`<${Crumbs} e=${e}><span>/</span><span>lanes</span></${Crumbs}>
    <div class="head"><h1>Lanes</h1><${Pill} state=${enrollmentState(e).cls === 'run' ? 'running' : e.activity} label=${e.activity}/>
      <div class="meta"><span>reported <${Ago} t=${e.at}/></span>${e.headless ? html`<span>headless (${e.headless.mode})</span>` : ''}</div></div>
    <div class="panel">${(e.lanes || []).length === 0 ? html`<div class="empty">No lanes in use.</div>` : html`<div class="tw"><table>
      <thead><tr><th class="i"></th><th>Lane</th><th>State</th><th>Step</th><th>Steps</th></tr></thead>
      <tbody>${e.lanes.map((l) => html`<tr>
        <td class="i"><${Icon} state=${l.state}/></td>
        <td>swim ${l.lane}<div class="sub">${l.round || ''}</div></td>
        <td><${Pill} state=${l.state}/>${l.paused ? html`<div class="sub">paused ${l.paused}</div>` : ''}${l.scheduled ? html`<div class="sub">${l.scheduled}</div>` : ''}
          ${l.reason ? html`<div class="sub">${l.reason}</div>` : ''}</td>
        <td>${l.step || ''}${l.progress ? html` <progress max="100" value=${pct(l.progress)}></progress> <span class="sub">${l.progress}</span>` : ''}
          ${l.message ? html`<div class="sub">${l.message}</div>` : ''}${l.cells ? html`<div class="sub">${l.cells}</div>` : ''}</td>
        <td class="nowrap"><span class="ok">${l.pass}</span> / <span class="bad">${l.fail}</span> / <span class="wait">${l.skip}</span></td></tr>
        ${(l.tail || []).length ? html`<tr class="tail-row"><td colspan="5"><pre class="tail">${l.tail.join('\n')}</pre></td></tr>` : ''}`)}
      </tbody></table></div>`}</div>
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
      <thead><tr><th>Enrollment</th><th>Guard</th><th>Lane</th><th>Expires</th><th></th></tr></thead>
      <tbody>${waiting.map(({ e, p }) => html`<tr>
        <td><a href=${href('e', e.id, 'lanes')}>${e.name}</a><div class="sub">${e.host}</div></td>
        <td><span class="mono">${p.flag}</span><div class="sub">${p.desc}</div>${p.need ? html`<div class="sub">needs ${p.need}</div>` : ''}</td>
        <td>swim ${p.lane}${p.cell ? ' · ' + p.cell : ''}</td>
        <td class="nowrap" title=${p.expires}>${when(p.expires)}</td>
        <td><a class="btn sm" href=${'/w/' + encodeURIComponent(e.id) + '/answer/' + encodeURIComponent(p.id)}>Answer</a></td></tr>`)}
      </tbody></table></div>`}</div>`;
}

function NotFound() {
  useTitle('Not found');
  return html`<div class="panel"><div class="empty">No such page. <a href="/">All enrollments</a></div></div>`;
}

// ---- the app ------------------------------------------------------------

const views = { home: Home, runs: Runs, run: Run, job: Job, lanes: Lanes, queue: Queue, approvals: Approvals, notfound: NotFound };

function App() {
  const path = usePath();
  const store = useStore();
  const r = route(path);
  const View = views[r.name];
  return html`<${TopBar} r=${r} store=${store}/><main><${View} key=${path} r=${r} store=${store}/></main>`;
}

start();
render(html`<${App}/>`, document.getElementById('app'));

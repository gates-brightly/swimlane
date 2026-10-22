// The hub's read API and event stream, as a small store the views read.
import { useEffect, useState } from './vendor/preact-htm.module.js';

// get fetches a JSON document: {status, data, error}.
export async function get(path) {
  try {
    const r = await fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' });
    const text = await r.text();
    if (!r.ok) return { status: r.status, data: null, error: text.trim() || r.statusText };
    return { status: r.status, data: JSON.parse(text), error: '' };
  } catch (e) {
    return { status: 0, data: null, error: 'the hub is unreachable' };
  }
}

// post sends a form with the session's CSRF token.
export async function post(path, csrf, form = {}) {
  const body = new URLSearchParams({ ...form, csrf });
  return fetch(path, { method: 'POST', body, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf } });
}

// The store: who's logged in, every enrollment and namespace, the event
// stream's state, and a counter per enrollment that ticks when a run
// arrives (views keyed on it fetch again).
const state = {
  me: null,
  enrollments: null,
  namespaces: [],
  error: '',
  live: 'connecting', // connecting | live | offline
  runs: {}, // enrollment id -> runs seen arrive
};
const subs = new Set();
function changed() {
  for (const f of subs) f({ ...state });
}

export function useStore() {
  const [s, set] = useState({ ...state });
  useEffect(() => {
    subs.add(set);
    set({ ...state }); // what changed between the first render and now
    return () => subs.delete(set);
  }, []);
  return s;
}

async function loadEnrollments() {
  const r = await get('/api/enrollments');
  if (r.data) {
    state.enrollments = r.data.enrollments;
    state.namespaces = r.data.namespaces;
    state.error = '';
  } else {
    state.error = r.error;
    if (r.status === 401) location.assign('/login');
  }
  changed();
}

async function loadMe() {
  const r = await get('/api/me');
  state.me = r.data || { user: '' };
  changed();
}

let source = null;
let wasOffline = false;

function connect() {
  source = new EventSource('/api/events');
  source.onopen = () => {
    state.live = 'live';
    // Nothing is replayed: what changed while away, read again.
    if (wasOffline) loadEnrollments();
    wasOffline = false;
    changed();
  };
  source.onerror = () => {
    state.live = source.readyState === EventSource.CLOSED ? 'offline' : 'connecting';
    wasOffline = true;
    changed();
    if (source.readyState === EventSource.CLOSED) setTimeout(connect, 3000);
  };
  source.addEventListener('snapshot', (ev) => {
    const v = JSON.parse(ev.data);
    const list = state.enrollments || [];
    const i = list.findIndex((e) => e.id === v.id);
    if (i < 0) {
      loadEnrollments(); // a new enrollment: its namespace tab too
      return;
    }
    state.enrollments = list.map((e, j) => (j === i ? v : e));
    changed();
  });
  source.addEventListener('run', (ev) => {
    const r = JSON.parse(ev.data);
    state.runs = { ...state.runs, [r.worker]: (state.runs[r.worker] || 0) + 1 };
    changed();
  });
}

export function start() {
  loadMe();
  loadEnrollments();
  connect();
}

// useFetch fetches path (again whenever a key changes): {loading, status,
// data, error, reload}.
export function useFetch(path, keys = []) {
  const [r, set] = useState({ loading: true, status: 0, data: null, error: '' });
  const [n, reload] = useState(0);
  useEffect(() => {
    let live = true;
    set((old) => ({ ...old, loading: true }));
    get(path).then((res) => live && set({ loading: false, ...res }));
    return () => { live = false; };
  }, [path, n, ...keys]);
  return { ...r, reload: () => reload((x) => x + 1) };
}

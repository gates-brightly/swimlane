// node --test internal/hub/web/lib.test.js (web_test.go runs it when node
// is on PATH).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  can, isAdmin, isOperator, until, utf8Length, ago, api, between, byNamespace, enrollmentState, fmtDuration, hasOutput, href, layoutGraph,
  lineMatches, matchEnrollment, route, stateClass, steps,
} from './lib.js';

test('route', () => {
  assert.deepEqual(route('/'), { name: 'home' });
  assert.deepEqual(route('/approvals'), { name: 'approvals' });
  assert.deepEqual(route('/admin'), { name: 'admin' });
  assert.deepEqual(route('/e/abc'), { name: 'runs', id: 'abc' });
  assert.deepEqual(route('/e/abc/runs/'), { name: 'runs', id: 'abc' });
  assert.deepEqual(route('/e/abc/lanes'), { name: 'lanes', id: 'abc' });
  assert.deepEqual(route('/e/abc/queue'), { name: 'queue', id: 'abc' });
  assert.deepEqual(route('/e/abc/runs/r-1'), { name: 'run', id: 'abc', run: 'r-1' });
  assert.deepEqual(route('/e/abc/runs/r-1/jobs/3'), { name: 'job', id: 'abc', run: 'r-1', lane: 3 });
  assert.deepEqual(route('/e/a%2Fb/runs'), { name: 'runs', id: 'a/b' });
  assert.deepEqual(route('/e/abc/questions/q1'), { name: 'question', id: 'abc', req: 'q1' });
  for (const p of ['/e', '/e/abc/runs/r-1/jobs/x', '/e/abc/nope', '/nope']) assert.equal(route(p).name, 'notfound', p);
});

test('href and api escape each part', () => {
  assert.equal(href('e', 'a/b', 'runs', 'r 1'), '/e/a%2Fb/runs/r%201');
  assert.equal(api('enrollments', '../x', 'runs'), '/api/enrollments/..%2Fx/runs');
});

test('stateClass', () => {
  for (const [s, c] of [['passed', 'ok'], ['PASS', 'ok'], ['failed', 'bad'], ['interrupted', 'bad'], ['BLOCKED', 'bad'],
    ['running', 'run'], ['running?', 'run'], ['skipped', 'wait'], ['queued', 'wait'], ['idle', 'dim'], [undefined, 'dim']]) {
    assert.equal(stateClass(s), c, String(s));
  }
});

test('durations and times', () => {
  assert.equal(fmtDuration(0.42), '0.4s');
  assert.equal(fmtDuration(12.4), '12s');
  assert.equal(fmtDuration(184), '3m04s');
  assert.equal(fmtDuration(3725), '1h02m');
  assert.equal(fmtDuration(null), '');
  assert.equal(between('2026-10-22T12:00:00Z', '2026-10-22T12:01:30Z'), 90);
  assert.equal(between('0001-01-01T00:00:00Z', '2026-10-22T12:01:30Z'), null);
  const now = Date.parse('2026-10-22T12:00:00Z');
  assert.equal(ago('2026-10-22T11:59:50Z', now), '10s ago');
  assert.equal(ago('2026-10-22T11:55:00Z', now), '5m ago');
  assert.equal(ago('2026-10-22T09:00:00Z', now), '3h ago');
  assert.equal(ago('', now), '');
  assert.equal(until('2026-10-22T12:25:00Z', now), 'in 25m');
  assert.equal(until('2026-10-22T13:05:30Z', now), 'in 1h05m');
  assert.equal(until('2026-10-22T12:00:20Z', now), 'in 20s');
  assert.equal(until('2026-10-22T11:00:00Z', now), 'expired');
  assert.equal(utf8Length('aé…'), 6);
});

test('layoutGraph places lanes by dependency depth', () => {
  // 1 -> 2, 1 -> 3, {2,3} -> 4; 5 alone; 6 waits on 9 (not in the run).
  const g = layoutGraph([
    { lane: 4, after: [2, 3] }, { lane: 1, after: [] }, { lane: 2, after: [1] },
    { lane: 3, after: [1] }, { lane: 5, after: [] }, { lane: 6, after: [9] },
  ]);
  const at = Object.fromEntries(g.nodes.map((n) => [n.lane, [n.col, n.row]]));
  assert.deepEqual(at, { 1: [0, 0], 5: [0, 1], 6: [0, 2], 2: [1, 0], 3: [1, 1], 4: [2, 0] });
  assert.deepEqual(g.edges.map((e) => e.from + '>' + e.to).sort(), ['1>2', '1>3', '2>4', '3>4']);
  assert.equal(g.width, 3 * 200 + 2 * 56);
  assert.equal(g.height, 3 * 54 + 2 * 14);
  // A cycle doesn't hang.
  assert.equal(layoutGraph([{ lane: 1, after: [2] }, { lane: 2, after: [1] }]).nodes.length, 2);
  assert.equal(layoutGraph([]).nodes.length, 0);
});

test('steps groups by stage and numbers lines', () => {
  const g = steps([
    { result: 'PASS', label: 'a', stage: 'check', step: true, command: 'echo a', output: ['a'] },
    { result: 'WARN', label: 'note', stage: 'check', step: false },
    { result: 'FAIL', label: 'b', stage: 'change', step: true, command: 'false', output: ['x', 'y'], messages: ['half'] },
  ]);
  assert.deepEqual(g.map((x) => [x.stage, x.failed, x.items.length]), [['check', false, 2], ['change', true, 1]]);
  assert.deepEqual(g[0].items[0].lines, [{ n: 1, text: '$ echo a', kind: 'cmd' }, { n: 2, text: 'a', kind: '' }]);
  assert.deepEqual(g[1].items[0].lines.map((l) => l.n + l.kind), ['3cmd', '4', '5', '6msg']);
  assert.equal(hasOutput([{ output: [] }, {}]), false);
  assert.equal(hasOutput([{ output: ['x'] }]), true);
  assert.equal(lineMatches('Hello World', 'world'), true);
  assert.equal(lineMatches('Hello', ''), false);
});

test('enrollments', () => {
  const es = [
    { id: '1', name: 'b', namespace: 'github.com/o/x', lanes: [{ state: 'running' }] },
    { id: '2', name: 'a', namespace: '', lanes: [] },
    { id: '3', name: 'c', namespace: 'github.com/o/a', stale: true, pending: [{}] },
    { id: '4', name: 'd', namespace: 'github.com/o/a', pending: [{}] },
    { id: '5', name: 'e', namespace: 'github.com/o/a', lanes: [{ state: 'failed' }] },
  ];
  assert.deepEqual(byNamespace(es).map(([k, v]) => k + ':' + v.map((e) => e.id).join('')), ['github.com/o/a:345', 'github.com/o/x:1', ':2']);
  assert.deepEqual(es.map((e) => enrollmentState(e).label), ['running', 'idle', 'stale', 'waiting', 'failed']);
  assert.equal(matchEnrollment({ name: 'billing', host: 'mac' }, 'MAC'), true);
  assert.equal(matchEnrollment({ name: 'billing', host: 'mac' }, 'spiff'), false);
  assert.equal(matchEnrollment({ name: 'billing' }, '  '), true);
});

test('can: the worker takes it, and the user may send it', () => {
  const e = { headless: { allow: ['answer', 'interrupt', 'log', 'run'] } };
  const op = { user: 'a', role: 'operator' }, viewer = { user: 'v', role: 'viewer' };
  assert.equal(can(op, e, 'interrupt'), true);
  assert.equal(can(op, e, 'trash'), false, 'not in headless.allow');
  assert.equal(can(viewer, e, 'interrupt'), false, 'a viewer');
  assert.equal(can(viewer, e, 'log'), true);
  assert.equal(can(viewer, e, 'answer'), true);
  assert.equal(can(op, { headless: { allow: ['log'], logs_off: true } }, 'log'), false, 'logs off');
  assert.equal(can(op, {}, 'log'), false, 'not headless');
  assert.equal(can({ user: '' }, e, 'log'), false, 'nobody');
  assert.equal(can(null, e, 'log'), false);
});

test('an admin is an operator too', () => {
  const e = { headless: { allow: ['interrupt', 'log'] } };
  const admin = { user: 'zach', role: 'admin' };
  assert.equal(isAdmin(admin), true);
  assert.equal(isOperator(admin), true);
  assert.equal(can(admin, e, 'interrupt'), true);
  assert.equal(isAdmin({ user: 'op', role: 'operator' }), false);
  assert.equal(isOperator({ user: 'v', role: 'viewer' }), false);
  assert.equal(isAdmin(null), false);
});

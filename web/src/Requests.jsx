import React, { useEffect, useRef, useState } from 'react';
import { request } from './api.js';
import { Badge, Button, Empty, Notice, Panel } from './components.jsx';

const stages = { routing: 'Route matching', authentication: 'Authentication', rate_limit: 'Request allowance', cache: 'Cache lookup', retry_policy: 'Retry policy', backend: 'Backend selection', upstream: 'Backend response', health: 'Health update', retry: 'Retry decision', proxy: 'Gateway response', body: 'Request size', cache_store: 'Cache storage', response: 'Response completion' };
const failed = new Set(['rejected', 'error', 'unavailable', 'timeout', 'failed', 'interrupted']);
const ms = value => `${Number(value).toFixed(2)} ms`;
const routeName = row => row.route || 'No route';
function Result({ row }) { return <Badge tone={!row.complete || row.status >= 400 ? 'warning' : 'success'}>{row.status || 'Interrupted'}</Badge>; }

export default function Requests({ gateway: g }) {
  const explanation = useRef(null);
  const [page, setPage] = useState(null);
  const [selected, setSelected] = useState(null);
  const [filter, setFilter] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  useEffect(() => { if (selected) explanation.current?.focus(); }, [selected]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError('');
    request(g.token, 'requests', { signal: controller.signal }).then(value => {
      if (!controller.signal.aborted) setPage(value);
    }).catch(e => { if (!controller.signal.aborted) setError(e.message); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [g.token, g.requestsVersion]);
  const rows = (page?.requests || []).filter(row => `${row.id} ${row.method} ${row.route} ${row.status}`.toLowerCase().includes(filter.toLowerCase()));
  const attempts = selected?.events.filter(event => event.stage === 'backend' && event.outcome === 'selected').length || 0;
  const missing = selected ? [['authentication', 'Authentication'], ['rate_limit', 'Quota'], ['cache', 'Cache'], ['backend', 'Backend']].filter(([stage]) => !selected.events.some(event => event.stage === stage)).map(([, label]) => label) : [];
  const explainButton = row => <Button variant="secondary" aria-label={`Explain ${row.method} ${routeName(row)} ${row.id}`} onClick={() => setSelected(row)}>Explain →</Button>;
  return <div className="space-y-6">
    <Notice>{error}</Notice>
    <div className="rounded-xl border border-teal-200 bg-teal-50 px-5 py-4 text-sm leading-6 text-teal-900"><strong>Real traffic. Recorded decisions.</strong> Send a request to your gateway, then refresh and choose Explain. Match a response using its <code className="break-all">X-GateForge-Request-ID</code> header.</div>
    <Panel title="Recent requests" subtitle="Latest 100 finished handlers on this instance · Refresh to load new traffic">
      <div className="border-b border-slate-100 p-5"><label className="text-xs font-medium text-slate-600" htmlFor="request-filter">Find a request</label><input id="request-filter" className="mt-2 block w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm sm:max-w-md" placeholder="Request ID, route, method, or status" value={filter} onChange={event => setFilter(event.target.value)}/></div>
      {loading ? <div role="status" className="p-6 text-sm text-slate-500">Loading request decisions…</div> : rows.length ? <>
        <div className="max-h-[420px] overflow-y-auto sm:hidden">{rows.map(row => <article key={row.id} className={`border-b border-slate-100 p-5 ${selected?.id === row.id ? 'bg-teal-50' : ''}`}><div className="flex items-center justify-between gap-3"><p className="min-w-0 break-all text-sm"><strong>{row.method}</strong> <code>{routeName(row)}</code></p><Result row={row}/></div><p className="mt-2 text-xs text-slate-500">{new Date(row.started_at).toLocaleTimeString()} · {ms(row.duration_ms)}</p><div className="mt-3 flex items-center justify-between gap-3"><code className="text-[11px] text-slate-500">{row.id.slice(0, 12)}…</code>{explainButton(row)}</div></article>)}</div>
        <div className="table-scroll hidden max-h-[420px] overflow-y-auto sm:block"><table className="data-table"><thead><tr><th>Time</th><th>Request / matched route</th><th>Result</th><th>Duration</th><th>Request ID</th><th><span className="sr-only">Explain request</span></th></tr></thead><tbody>{rows.map(row => <tr key={row.id} className={selected?.id === row.id ? 'bg-teal-50' : ''}><td className="whitespace-nowrap">{new Date(row.started_at).toLocaleTimeString()}</td><td><span className="mr-2 text-xs font-semibold">{row.method}</span><code>{routeName(row)}</code></td><td><Result row={row}/></td><td className="whitespace-nowrap">{ms(row.duration_ms)}</td><td><code title={row.id}>{row.id.slice(0, 12)}…</code></td><td>{explainButton(row)}</td></tr>)}</tbody></table></div>
      </> : <Empty title={error ? 'Requests unavailable' : filter ? 'No matching requests' : 'Waiting for your first request'}>{error ? 'Refresh to try again.' : 'Call an application route through GateForge, then refresh this view. Health checks and admin traffic are excluded.'}</Empty>}
    </Panel>
    {selected && <section ref={explanation} tabIndex={-1} aria-label="Request explanation" className="overflow-hidden rounded-2xl border border-slate-800 bg-slate-950 text-slate-100">
      <div className="border-b border-white/10 p-6 sm:p-8"><div className="flex flex-wrap items-start justify-between gap-4"><div><p className="text-[10px] font-semibold tracking-[.2em] text-teal-300">EXPLAIN THIS REQUEST</p><h2 className="mt-3 break-all text-2xl font-semibold">{selected.method} <span className="text-slate-300">{selected.route || 'Unmatched route'}</span></h2><p className="mt-2 text-xs text-slate-400">Matched prefix shown; the original path and query are not retained.</p></div><button className="rounded-lg border border-white/20 px-3 py-2 text-xs text-slate-300 hover:bg-white/10" onClick={() => setSelected(null)}>Close explanation</button></div>
      <dl className="mt-6 grid grid-cols-2 gap-5 sm:grid-cols-4">{[['Response', selected.status || 'No headers'], ['Total time', ms(selected.duration_ms)], ['Backend attempts', attempts], ['Policy revision', selected.revision]].map(([label, value]) => <div key={label}><dt className="text-[10px] uppercase tracking-wider text-slate-400">{label}</dt><dd className="mt-2 text-lg font-semibold">{value}</dd></div>)}</dl><p className="mt-5 break-all font-mono text-[11px] text-slate-400">ID {selected.id} · {new Date(selected.started_at).toLocaleString()}</p></div>
      <div className="p-6 sm:p-8"><p className="mb-6 text-xs leading-6 text-slate-400">Each step was recorded while the gateway handled this request. Times are elapsed since arrival at the application pipeline.</p><ol>{selected.events.map((event, index) => <li key={index} className="relative flex gap-4 pb-7 last:pb-0"><div className="relative flex w-7 shrink-0 justify-center">{index < selected.events.length - 1 && <span aria-hidden="true" className="absolute top-7 bottom-[-4px] w-px bg-slate-700"/>}<span className={`z-10 flex size-7 items-center justify-center rounded-full border text-[11px] ${failed.has(event.outcome) ? 'border-rose-400/50 bg-rose-950 text-rose-300' : 'border-teal-400/40 bg-slate-900 text-teal-300'}`}>{index + 1}</span></div><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-x-3 gap-y-1"><h3 className="text-sm font-semibold">{stages[event.stage] || event.stage}</h3><span className={`text-[10px] uppercase tracking-wider ${failed.has(event.outcome) ? 'text-rose-300' : 'text-teal-300'}`}>{event.outcome}</span><span className="ml-auto font-mono text-[11px] text-slate-500">+{ms(event.elapsed_ms)}</span></div><p className="mt-2 break-words text-sm leading-6 text-slate-400">{event.reason}</p></div></li>)}</ol>
      {missing.length > 0 && <p className="mt-7 rounded-lg border border-white/10 p-4 text-xs leading-6 text-slate-400">No decision recorded for: {missing.join(', ')}. An earlier step ended the request before these stages were reached.</p>}
      {!selected.complete && <p className="mt-6 text-sm text-rose-300">The handler was interrupted. A recorded HTTP status does not confirm complete delivery to the client.</p>}
      {selected.truncated && <p className="mt-6 text-sm text-amber-300">The timeline reached its 64-event limit. Some later decisions are omitted.</p>}</div>
    </section>}
    <p className="text-xs leading-6 text-slate-500">Instance-local memory only; records expire as new requests arrive and disappear on restart. In a multi-instance deployment, each instance has its own list. No request bodies, raw paths, queries, credentials, backend addresses, or client identities are retained. This view is restricted to administrators.</p>
  </div>;
}

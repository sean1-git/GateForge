import React, { useEffect, useState } from 'react';
import { Button, Empty, Notice, Panel } from './components.jsx';

export default function Audit({ gateway: g }) {
  const [page, setPage] = useState(null); const [history, setHistory] = useState([]);
  const [cursor, setCursor] = useState(0); const [error, setError] = useState(''); const [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true; setLoading(true); setError('');
    g.readAudit(cursor).then(value => { if (active) setPage(value); }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [cursor, g.auditVersion]);
  return <div className="space-y-5"><Notice>{error}</Notice><Panel title="Administrator audit trail" subtitle="Successful changes and session events, committed with the operation">
    {loading ? <div className="p-6 text-sm text-slate-500" role="status">Loading audit records…</div> : page?.events.length ? <div className="table-scroll"><table className="data-table"><thead><tr><th>Time</th><th>Administrator ID</th><th>Action</th><th>Resource</th></tr></thead><tbody>{page.events.map(event => <tr key={event.id}><td className="whitespace-nowrap">{new Date(event.created_at).toLocaleString()}</td><td><code title={event.actor}>{event.actor.length > 20 ? `${event.actor.slice(0, 16)}…` : event.actor}</code></td><td>{event.action}</td><td><code>{event.resource}</code></td></tr>)}</tbody></table></div> : <Empty title={error ? 'Audit records unavailable' : 'No audit events yet'}>{error ? 'Refresh to retry.' : 'Administrator sign-ins and changes will appear here.'}</Empty>}
    <div className="flex items-center justify-between border-t border-slate-100 p-5"><span className="text-xs text-slate-500">Page {history.length + 1} · Up to 50 records</span><div className="flex gap-2"><Button variant="secondary" disabled={loading || !history.length} onClick={() => {setCursor(history.at(-1));setHistory(history.slice(0,-1));}}>Previous</Button><Button variant="secondary" disabled={loading || !!error || !page?.next_cursor} onClick={() => {setHistory([...history,cursor]);setCursor(page.next_cursor);}}>Next</Button></div></div>
  </Panel><p className="text-xs leading-6 text-slate-500">Records contain no tokens, key secrets or request bodies. Administrator IDs identify the signing account. Ordinary database updates and deletes are blocked; a database owner can still change the schema.</p></div>;
}

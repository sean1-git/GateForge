import { useEffect, useRef, useState } from 'react';
import { request, actionGate } from './api.js';

export function useGateway(learnOpen) {
  const [token, setToken] = useState('');
  const [tab, setTab] = useState('Overview');
  const [metrics, setMetrics] = useState(null); const [backends, setBackends] = useState([]);
  const [config, setConfig] = useState(null); const [keys, setKeys] = useState([]);
  const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const [notice, setNotice] = useState(''); const [pollError, setPollError] = useState('');
  const [updated, setUpdated] = useState(null); const [secret, setSecret] = useState('');
  const [keysLoading, setKeysLoading] = useState(false); const [keysLoaded, setKeysLoaded] = useState(false);
  const [cursor, setCursor] = useState(''); const [nextCursor, setNextCursor] = useState(''); const [history, setHistory] = useState([]);
  const gate = useRef(actionGate()); const session = useRef(new AbortController()); const creation = useRef(null);
  const api = (path, options = {}) => request(token, path, { ...options, signal: session.current.signal });
  async function load(activeToken = token, all = false, signal = session.current.signal) {
    const [m, b, c] = await Promise.all([request(activeToken, 'metrics', { signal }), request(activeToken, 'backends', { signal }), all ? request(activeToken, 'config', { signal }) : null]);
    if (signal.aborted) return;
    setMetrics(m); setBackends(b); setUpdated(new Date()); setPollError('');
    if (all) setConfig(c);
  }
  useEffect(() => {
    if (!token || learnOpen) return;
    const controller = new AbortController(); let timer;
    const poll = async () => {
      if (!document.hidden) { try { await load(token, false, controller.signal); } catch (e) { if (!controller.signal.aborted) setPollError(e.message); } }
      if (!controller.signal.aborted) timer = setTimeout(poll, 5000);
    };
    timer = setTimeout(poll, 5000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [token, learnOpen]);
  useEffect(() => () => session.current.abort(), []);
  const action = fn => gate.current(async () => {
    setBusy(true); setError(''); setNotice('');
    try { await fn(); return true; } catch (e) { if (e.name !== 'AbortError') setError(e.message); return false; } finally { setBusy(false); }
  });
  async function loadKeys(value = '', past = []) {
    setKeysLoading(true);
    try { const page = await api(`keys?limit=50&cursor=${encodeURIComponent(value)}`); setKeys(page.keys); setNextCursor(page.next_cursor || ''); setCursor(value); setHistory(past); setKeysLoaded(true); }
    finally { setKeysLoading(false); }
  }
  async function updateKeyList() { try { await loadKeys(); } catch (e) { setError(`The operation succeeded, but the key list could not refresh. ${e.message}`); } }
  const signIn = value => action(async () => { await load(value, true); setToken(value); });
  const signOut = () => { session.current.abort(); session.current = new AbortController(); creation.current = null; setToken(''); setSecret(''); setConfig(null); setMetrics(null); setBackends([]); setKeys([]); setKeysLoaded(false); setError(''); setNotice(''); setPollError(''); setUpdated(null); setTab('Overview'); setHistory([]); setCursor(''); setNextCursor(''); };
  const select = name => { if (busy) return; setTab(name); setError(''); setNotice(''); if (name === 'API keys') action(() => loadKeys()); };
  const refresh = () => action(() => tab === 'API keys' ? loadKeys(cursor, history) : load(token, tab === 'Routes'));
  const save = (editor, revision) => action(async () => { const routes = JSON.parse(editor); if (!Array.isArray(routes)) throw new Error('Routes must be a JSON array.'); const c = await api('config', { method: 'PUT', body: JSON.stringify({ revision, routes }) }); setConfig(c); setNotice(`Configuration saved as revision ${c.revision}.`); });
  const createKey = form => action(async () => {
    const identity = JSON.stringify(form);
    if (creation.current?.identity !== identity) creation.current = { identity, id: crypto.randomUUID(), body: JSON.stringify({ name: form.name, prefixes: form.prefixes, expires_at: new Date(Date.now() + Number(form.days) * 86400000).toISOString() }) };
    const pending = creation.current;
    const result = await api('keys', { method: 'POST', headers: { 'Idempotency-Key': pending.id }, body: pending.body });
    setSecret(result.secret); creation.current = null; await updateKeyList();
  });
  const revoke = key => action(async () => { await api(`keys/${encodeURIComponent(key.id)}`, { method: 'DELETE' }); setNotice(`Access revoked for ${key.name}.`); await updateKeyList(); });
  return { token, tab, metrics, backends, config, keys, busy, error, setError, notice, setNotice, pollError, updated, secret, setSecret, keysLoading, keysLoaded, nextCursor, history, signIn, signOut, select, refresh, save, createKey, revoke, nextPage: () => action(() => loadKeys(nextCursor, [...history, cursor])), previousPage: () => action(() => loadKeys(history.at(-1), history.slice(0, -1))) };
}

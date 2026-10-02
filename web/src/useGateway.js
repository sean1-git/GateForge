import { useEffect, useRef, useState } from 'react';
import { request, actionGate } from './api.js';

export function useGateway(learnOpen) {
  const [token, setToken] = useState('');
  const [authMode, setAuthMode] = useState(''); const [authLoading, setAuthLoading] = useState(true);
  const [authError, setAuthError] = useState('');
  const [tab, setTab] = useState('Overview');
	const [requestsVersion, setRequestsVersion] = useState(0);
  const [auditVersion, setAuditVersion] = useState(0);
  const [metrics, setMetrics] = useState(null); const [backends, setBackends] = useState([]);
  const [config, setConfig] = useState(null); const [keys, setKeys] = useState([]);
  const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const [notice, setNotice] = useState(''); const [pollError, setPollError] = useState('');
  const [updated, setUpdated] = useState(null); const [secret, setSecret] = useState('');
  const [keysLoading, setKeysLoading] = useState(false); const [keysLoaded, setKeysLoaded] = useState(false);
  const [cursor, setCursor] = useState(''); const [nextCursor, setNextCursor] = useState(''); const [history, setHistory] = useState([]);
  const gate = useRef(actionGate()); const session = useRef(new AbortController()); const creation = useRef(null);
  const api = (path, options = {}) => request(token, path, { ...options, signal: session.current.signal });
  const auth = (path, options = {}, credential = token) => request(credential, path, { ...options, basePath: '/admin/auth/', signal: session.current.signal });
  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const settings = await auth('config', {}, '');
        if (!active) return;
        if (!['token', 'token_session', 'google'].includes(settings.mode)) throw new Error('Unsupported sign-in configuration.');
        setAuthMode(settings.mode);
        if (settings.mode !== 'token') {
          let current;
          try { current = await auth('session', {}, ''); } catch (e) { if (e.status !== 401) throw e; }
          if (current && active) { await load(current, true); if (active) setToken(current); }
        }
        if (new URLSearchParams(window.location.search).get('signin') === 'failed') {
          setError('Sign-in was not completed. Use an approved Google account and try again.');
          window.history.replaceState(null, '', window.location.pathname + window.location.hash);
        }
      } catch (e) { if (active && e.name !== 'AbortError') setAuthError(e.message); }
      finally { if (active) setAuthLoading(false); }
    })();
    return () => { active = false; };
  }, []);
  async function load(activeToken = token, all = false, signal = session.current.signal) {
    const [m, b, c] = await Promise.all([request(activeToken, 'metrics', { signal }), request(activeToken, 'backends', { signal }), all ? request(activeToken, 'policies', { signal }) : null]);
    if (signal.aborted) return;
    setMetrics(m); setBackends(b); setUpdated(new Date()); setPollError('');
    if (all) setConfig(c);
  }
  useEffect(() => {
    if (!token || learnOpen) return;
    const controller = new AbortController(); let timer;
    const poll = async () => {
      if (!document.hidden) { try { await load(token, false, controller.signal); } catch (e) { if (!controller.signal.aborted) { if (e.status === 401) { clearSession(); setError('Your session expired. Sign in again.'); } else setPollError(e.message); } } }
      if (!controller.signal.aborted) timer = setTimeout(poll, 5000);
    };
    timer = setTimeout(poll, 5000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [token, learnOpen]);
  useEffect(() => () => session.current.abort(), []);
  const action = fn => gate.current(async () => {
    setBusy(true); setError(''); setNotice('');
    try { await fn(); return true; } catch (e) { if (e.name !== 'AbortError') { if (e.status === 401 && token) clearSession(); setError(e.message); } return false; } finally { setBusy(false); }
  });
  async function loadKeys(value = '', past = []) {
    setKeysLoading(true);
    try { const page = await api(`keys?limit=50&cursor=${encodeURIComponent(value)}`); setKeys(page.keys); setNextCursor(page.next_cursor || ''); setCursor(value); setHistory(past); setKeysLoaded(true); }
    finally { setKeysLoading(false); }
  }
  async function updateKeyList() { try { await loadKeys(); } catch (e) { setError(`The operation succeeded, but the key list could not refresh. ${e.message}`); } }
  const signIn = value => action(async () => {
    if (authMode === 'token_session') {
      await auth('token', { method: 'POST', body: JSON.stringify({ token: value }) }, '');
      value = '';
      const current = await auth('session', {}, '');
      setToken(current);
      await load(current, true);
    } else { await load(value, true); setToken(value); }
  });
  function clearSession() { session.current.abort(); session.current = new AbortController(); creation.current = null; setToken(''); setSecret(''); setConfig(null); setMetrics(null); setBackends([]); setKeys([]); setKeysLoaded(false); setError(''); setNotice(''); setPollError(''); setUpdated(null); setTab('Overview'); setHistory([]); setCursor(''); setNextCursor(''); }
  const signOut = () => action(async () => { if (typeof token === 'object') await auth('logout', { method: 'POST' }); clearSession(); });
  const select = name => { if (busy) return; setTab(name); setError(''); setNotice(''); if (name === 'API keys') action(() => loadKeys()); };
  const refresh = () => action(() => tab === 'API keys' ? loadKeys(cursor, history) : tab === 'Requests' ? setRequestsVersion(v => v + 1) : tab === 'Audit log' ? setAuditVersion(v => v + 1) : load(token, tab === 'Routes'));
  const save = (editor, revision) => action(async () => { const routes = JSON.parse(editor); if (!Array.isArray(routes)) throw new Error('Routes must be a JSON array.'); const c = await api('policies', { method: 'PUT', body: JSON.stringify({ revision, routes }) }); setConfig(c); setNotice(`Configuration saved as revision ${c.revision}.`); });
  const createKey = form => action(async () => {
    const identity = JSON.stringify(form);
    if (creation.current?.identity !== identity) creation.current = { identity, id: crypto.randomUUID(), body: JSON.stringify({ name: form.name, prefixes: form.prefixes, expires_at: new Date(Date.now() + Number(form.days) * 86400000).toISOString() }) };
    const pending = creation.current;
    const result = await api('keys', { method: 'POST', headers: { 'Idempotency-Key': pending.id }, body: pending.body });
    setSecret(result.secret); creation.current = null; await updateKeyList();
  });
  const revoke = key => action(async () => { await api(`keys/${encodeURIComponent(key.id)}`, { method: 'DELETE' }); setNotice(`Access revoked for ${key.name}.`); await updateKeyList(); });
  return { requestsVersion, auditVersion, readAudit: before => api(`audit?before=${before}`), token, authMode, authLoading, authError, identity: typeof token === 'object' ? token.identity : null, tab, metrics, backends, config, keys, busy, error, setError, notice, setNotice, pollError, updated, secret, setSecret, keysLoading, keysLoaded, nextCursor, history, signIn, signOut, select, refresh, save, createKey, revoke, nextPage: () => action(() => loadKeys(nextCursor, [...history, cursor])), previousPage: () => action(() => loadKeys(history.at(-1), history.slice(0, -1))) };
}

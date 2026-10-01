export async function request(token, path, options = {}) {
  const { timeoutMs = 10000, signal, basePath = '/admin/api/', ...fetchOptions } = options;
  const controller = new AbortController();
  let timedOut = false;
  const abort = () => controller.abort();
  if (signal?.aborted) abort();
  signal?.addEventListener('abort', abort, { once: true });
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs);
  const mutation = options.method && options.method !== 'GET';
  try {
    const authentication = token && typeof token === 'object' ? { 'X-GateForge-CSRF': token.csrf_token } : token ? { Authorization: `Bearer ${token}` } : {};
    const response = await fetch(`${basePath}${path}`, { ...fetchOptions, signal: controller.signal, credentials: 'same-origin', cache: 'no-store', headers: { 'Content-Type': 'application/json', ...authentication, ...options.headers } });
    if (response.status === 204) return null;
    let body;
    try { body = await response.json(); } catch (error) {
      if (controller.signal.aborted) throw error;
      throw new Error(response.ok ? 'The server returned an unreadable response. Refresh to check the result.' : `Request failed (${response.status}). Please try again later.`);
    }
    if (!response.ok) {
      const error = new Error(body?.error || `Request failed (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return body;
  } catch (error) {
    if (timedOut) throw new Error(`The request timed out.${mutation ? ' It may have completed; refresh before submitting again.' : ' Try refreshing.'}`);
    if (signal?.aborted) { const cancelled = new Error('Request cancelled'); cancelled.name = 'AbortError'; throw cancelled; }
    if (error instanceof TypeError) throw new Error(`Could not reach the gateway.${mutation ? ' The operation may have completed; refresh before retrying.' : ' Check your connection and try again.'}`);
    throw error;
  } finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); }
}
// Lock immediately, before React renders disabled buttons, including double clicks.
export function actionGate() {
  let active = false;
  return async fn => { if (active) return; active = true; try { return await fn(); } finally { active = false; } };
}
export function totals(rows = []) {
  return rows.reduce((a, r) => ({ requests: a.requests + r.requests, errors: a.errors + r.errors, hits: a.hits + r.cache_hits, misses: a.misses + r.cache_misses, seconds: a.seconds + r.duration_seconds }), { requests: 0, errors: 0, hits: 0, misses: 0, seconds: 0 });
}

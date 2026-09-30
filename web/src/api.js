export async function request(token, path, options = {}) {
  const response = await fetch(`/admin/api/${path}`, { ...options, cache: 'no-store', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, ...options.headers } });
  if (response.status === 204) return null;
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
  return body;
}
export function totals(rows = []) {
  return rows.reduce((a, r) => ({ requests: a.requests + r.requests, errors: a.errors + r.errors, hits: a.hits + r.cache_hits, misses: a.misses + r.cache_misses, seconds: a.seconds + r.duration_seconds }), { requests: 0, errors: 0, hits: 0, misses: 0, seconds: 0 });
}

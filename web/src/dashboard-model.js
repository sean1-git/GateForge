export function keyStatus(key, now = Date.now()) {
  if (key.revoked) return 'Revoked';
  return Date.parse(key.expires_at) <= now ? 'Expired' : 'Active';
}
export function filterKeys(keys, search, hideRevoked) {
  const query = search.trim().toLowerCase();
  return keys.filter(key => (!hideRevoked || !key.revoked) && [key.name, key.id, ...key.prefixes].some(value => value.toLowerCase().includes(query)));
}
export function routeAuth(route) {
  return ({ api_key: 'API key', jwt: 'JWT', either: 'Key or JWT', public: 'Public' })[route.auth || 'public'] || route.auth;
}
export function routeTargets(route) { return route.upstreams || (route.upstream ? [route.upstream] : []); }

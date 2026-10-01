// Local teaching model. These counters never read or change gateway traffic.
export const LOCAL_LIMITS = [2, 4, 6];
export const SHARED_LIMIT = 6;
export const initialSimulation = () => ({ sent: 0, accepted: 0, blocked: 0, counts: [0, 0, 0], recent: [] });

export function simulate(state, settings, amount = 1) {
  const next = { ...state, counts: [...state.counts], recent: [] };
  for (let i = 0; i < amount; i++) {
    let target = settings.route;
    let accepted = true;
    if (settings.scene === 'limits') {
      target = next.sent % 3;
      accepted = settings.centralized ? next.accepted < SHARED_LIMIT : next.counts[target] < LOCAL_LIMITS[target];
      if (!accepted && settings.centralized) target = null;
    }
    if (settings.scene === 'balance') {
      const schedule = (settings.weighted ? [0, 0, 0, 0, 1, 2] : [0, 1, 2]).filter(n => !(settings.offline && n === 0));
      target = schedule[next.sent % schedule.length];
    }
    next.sent++;
    if (accepted) { next.accepted++; next.counts[target]++; } else next.blocked++;
    next.recent.push({ id: next.sent, target, accepted });
  }
  return next;
}

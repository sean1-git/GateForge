import React, { useEffect, useId, useRef } from 'react';

const shapes = {
  overview: <><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></>,
  routes: <><path d="M5 4v12a3 3 0 0 0 3 3h11M5 9h11M15 5l4 4-4 4m0 2 4 4-4 4"/></>,
  key: <><circle cx="8" cy="9" r="5"/><path d="m12 13 9 9m-5-5 3-3m-6 0 3-3"/><path d="M6 8h.01"/></>,
  book: <><path d="M12 6C9 3 5 3 2 4v15c4-1 7 0 10 2 3-2 6-3 10-2V4c-3-1-7-1-10 2Zm0 0v15"/></>,
  shield: <><path d="m12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6z"/><path d="m8 12 3 3 5-6"/></>,
  arrow: <path d="M4 12h16m-6-6 6 6-6 6"/>,
  refresh: <><path d="M20 8a8 8 0 1 0 0 9M20 3v5h-5"/></>,
  activity: <path d="M2 12h5l3-8 4 16 3-8h5"/>,
  clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>,
  alert: <><path d="m12 3 10 18H2zM12 9v5m0 3v.01"/></>,
  cache: <><path d="m12 2-9 5 9 5 9-5-9-5Zm-9 10 9 5 9-5M3 17l9 5 9-5"/></>,
  server: <><rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M12 6.5h5M12 17.5h5"/></>,
  plus: <path d="M12 5v14M5 12h14"/>, close: <path d="m6 6 12 12M6 18 18 6"/>,
  search: <><circle cx="10" cy="10" r="7"/><path d="m15 15 6 6"/></>,
  copy: <><rect x="8" y="8" width="12" height="13" rx="2"/><path d="M15 8V3H3v13h5"/></>,
  logout: <><path d="M9 3H3v18h6m5-15 6 6-6 6m-7-6h13"/></>,
  menu: <path d="M4 6h16M4 12h16M4 18h16"/>,
  code: <><path d="m8 7-5 5 5 5m8-10 5 5-5 5M14 4l-4 16"/></>,
  check: <path d="m5 12 4 4L19 6"/>,
};
export function Icon({ name, className = '', ...props }) {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" className={`size-5 shrink-0 ${className}`} aria-hidden="true" {...props}>{shapes[name]}</svg>;
}
export function Logo({ light = false }) {
  return <div className={`flex items-center gap-3 text-xl font-bold tracking-tight ${light ? 'text-white' : 'text-slate-900'}`}><span className="flex size-9 items-center justify-center rounded-xl bg-teal-400 text-slate-950"><Icon name="shield"/></span>GateForge<span className="rounded border border-current/20 px-1.5 py-0.5 text-[9px] font-medium tracking-widest opacity-60">BETA</span></div>;
}
export function Button({ children, variant = 'primary', icon, className = '', busy, disabled, ...props }) {
  return <button type="button" className={`btn btn-${variant} ${className}`} disabled={disabled || busy} {...props}>{busy ? <span className="spinner" aria-hidden="true"/> : icon && <Icon name={icon}/>} {children}</button>;
}
export function Badge({ children, tone = 'neutral', dot = false }) {
  const colors = { neutral: 'bg-slate-100 text-slate-600', success: 'bg-emerald-50 text-emerald-700 ring-emerald-200/60', warning: 'bg-amber-50 text-amber-800 ring-amber-200/60', danger: 'bg-rose-50 text-rose-700 ring-rose-200/60', accent: 'bg-teal-50 text-teal-800 ring-teal-200/60' };
  return <span className={`inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-1 text-[11px] font-semibold ring-1 ring-inset ring-slate-200/60 ${colors[tone]}`}>{dot && <span className="size-1.5 rounded-full bg-current"/>}{children}</span>;
}
export function Empty({ icon = 'activity', title, children, action }) {
  return <div className="flex flex-col items-center px-5 py-12 text-center"><span className="mb-4 flex size-12 items-center justify-center rounded-2xl bg-slate-100 text-slate-400"><Icon name={icon}/></span><h3 className="text-sm font-semibold text-slate-800">{title}</h3><p className="mt-2 max-w-sm text-sm leading-6 text-slate-500">{children}</p>{action && <div className="mt-5">{action}</div>}</div>;
}
export function Notice({ children, type = 'error', onClose }) {
  if (!children) return null;
  return <div role={type === 'error' ? 'alert' : 'status'} className={`flex items-start gap-3 rounded-xl border p-4 text-sm leading-6 ${type === 'error' ? 'border-rose-200 bg-rose-50 text-rose-800' : 'border-teal-200 bg-teal-50 text-teal-900'}`}><Icon name={type === 'error' ? 'alert' : 'check'} className="mt-0.5"/><span className="min-w-0 flex-1 break-words">{children}</span>{onClose && <button aria-label="Dismiss notification" onClick={onClose} className="rounded p-1"><Icon name="close" className="size-4"/></button>}</div>;
}
export function Panel({ title, subtitle, action, children, className = '' }) {
  return <section className={`panel ${className}`}><div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-5 py-5 sm:px-6"><div><h2 className="text-sm font-semibold text-slate-900">{title}</h2>{subtitle && <p className="mt-1 text-xs leading-5 text-slate-500">{subtitle}</p>}</div>{action}</div>{children}</section>;
}
export function Modal({ open, onClose, title, description, busy, children, wide = false }) {
  const ref = useRef(null); const id = useId();
  useEffect(() => { if (open && !ref.current.open) ref.current.showModal(); else if (!open && ref.current.open) ref.current.close(); }, [open]);
  return <dialog ref={ref} aria-labelledby={id} aria-describedby={description ? `${id}-description` : undefined} className={`modal ${wide ? 'modal-wide' : ''}`} onCancel={e => { e.preventDefault(); if (!busy) onClose(); }}><div className="flex items-start justify-between gap-5 border-b border-slate-100 px-6 py-5"><div><h2 id={id} className="text-lg font-semibold tracking-tight">{title}</h2>{description && <p id={`${id}-description`} className="mt-1 text-sm leading-6 text-slate-500">{description}</p>}</div><button type="button" aria-label="Close dialog" className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-100 hover:text-slate-900" disabled={busy} onClick={onClose}><Icon name="close"/></button></div>{children}</dialog>;
}

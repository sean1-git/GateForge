import React, { useState } from 'react';
import { Button, Notice } from './components.jsx';
import googleSignIn from './assets/google-sign-in.png';

export default function SignIn({ gateway: g }) {
  const [draft, setDraft] = useState('');
  const [revealed, setRevealed] = useState(false);
  if (g.authLoading) return <div className="mt-8 flex items-center gap-3 text-sm text-slate-500" role="status"><span className="spinner"/>Checking your session…</div>;
  if (g.authError) return <div className="mt-6 space-y-4"><Notice>{g.authError}</Notice><Button variant="secondary" onClick={() => window.location.reload()}>Retry sign-in setup</Button></div>;
  if (g.authMode === 'google') return <div className="mt-6 space-y-5">
    <p className="text-sm leading-6 text-slate-500">Use your approved Google account to open the control room.</p>
    <Notice>{g.error}</Notice>
    <a href="/admin/auth/login" className="inline-block rounded outline-offset-4"><img src={googleSignIn} alt="Sign in with Google" className="h-10 w-auto"/></a>
    <p className="text-xs leading-6 text-slate-500">Access is limited to approved administrators. Your session lasts up to eight hours, and signing out ends it immediately.</p>
  </div>;
  return <><p className="mt-3 text-sm leading-6 text-slate-500">Enter your administrator token to open the control room.</p>
    <form className="mt-8 space-y-5" onSubmit={e => { e.preventDefault(); g.signIn(draft.trim()); setDraft(''); setRevealed(false); }}>
      <label className="field-label">Administrator token<div className="relative mt-2"><input className="field pr-16" type={revealed ? 'text' : 'password'} value={draft} onChange={e => setDraft(e.target.value)} autoComplete="off" placeholder="Enter your private token" required minLength={32} disabled={g.busy}/><button type="button" className="absolute inset-y-0 right-0 px-3 text-xs text-slate-500 hover:text-teal-700" onClick={() => setRevealed(v => !v)} aria-label={revealed ? 'Hide administrator token' : 'Show administrator token'}>{revealed ? 'Hide' : 'Show'}</button></div></label>
      <Notice>{g.error}</Notice><Button className="w-full justify-center py-3" type="submit" busy={g.busy} icon="arrow">{g.busy ? 'Connecting…' : 'Open control room'}</Button>
    </form><p className="mt-5 text-xs leading-6 text-slate-500">{g.authMode === 'token_session' ? 'Your token is sent once over HTTPS, then cleared from this form. A secure session keeps you signed in for up to eight hours. Signing out revokes it immediately.' : 'Your token stays in this tab’s memory. It is cleared when you sign out or reload.'}</p></>;
}

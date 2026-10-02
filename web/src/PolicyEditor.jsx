import React, { useState } from 'react';
import { Button, Notice, Badge } from './components.jsx';

export default function PolicyEditor({ gateway:g, initial, revision, onClose }) {
  const [editor,setEditor]=useState(initial), [preview,setPreview]=useState(null), [pending,setPending]=useState(false), [error,setError]=useState('');
  const current=preview?.source===editor?preview.result:null;
  async function inspect(){const source=editor;setPending(true);setError('');try{const routes=JSON.parse(source);if(!Array.isArray(routes))throw new Error('Routes must be an array.');const result=await g.previewPolicies(routes,revision);setPreview({source,result});}catch(e){setError(e.message);}finally{setPending(false);}}
  const samples=current?.samples||[];
  return <form onSubmit={async e=>{e.preventDefault();if(current&&!pending&&await g.save(editor,revision))onClose();}}>
    <div className="space-y-4 p-6"><Notice>{error||g.error}</Notice><label className="field-label" htmlFor="route-json">Proposed policies</label><textarea id="route-json" className="json-editor" value={editor} onChange={e=>setEditor(e.target.value)} disabled={g.busy||pending} spellCheck={false}/>
    <p className="text-xs leading-6 text-slate-500">Keep <code>source_prefix</code> to identify an existing backend pool. Change <code>prefix</code> to preview a routing change, or edit authentication, scope, quotas and timeout. Preview does not send traffic or save configuration.</p>
    <Button variant="secondary" onClick={inspect} disabled={g.busy||pending}>{pending?'Evaluating samples…':'Preview sampled requests'}</Button>
    {current&&<section aria-label="Policy preview" className="space-y-3"><div className="flex flex-wrap gap-2"><Badge>{samples.length} samples</Badge><Badge tone="warning">{samples.filter(s=>s.routing_changed||s.authorization_changed).length} changed</Badge><Badge>{samples.filter(s=>s.unknown).length} uncertain</Badge></div><p className="text-xs leading-6 text-slate-500">{current.note}</p>
    {!samples.length?<p className="rounded-lg bg-amber-50 p-3 text-sm">No retained samples on this instance. This preview provides no evidence about traffic impact.</p>:<div className="table-scroll max-h-80 overflow-auto"><table className="data-table"><thead><tr><th>Sample</th><th>Route before → after</th><th>Authorization before → after</th><th>Evidence</th></tr></thead><tbody>{samples.map(s=><tr key={s.id}><td><code>{s.id.slice(0,12)}</code><br/>{s.method}</td><td>{s.before_route||'Unmatched'} → {s.after_route||'Unmatched / unknown'}</td><td><Badge tone={s.unknown?'warning':s.authorization_changed?'accent':'neutral'}>{s.before_auth} → {s.after_auth}</Badge></td><td className="text-xs">{s.reason}</td></tr>)}</tbody></table></div>}</section>}
    </div><div className="modal-actions"><Button variant="secondary" disabled={g.busy||pending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={!current||pending} busy={g.busy}>Apply reviewed draft</Button></div>
  </form>;
}

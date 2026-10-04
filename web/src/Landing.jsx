import React, { useState } from 'react';
import { Icon, Logo, Modal } from './components.jsx';
import SignIn from './SignIn.jsx';
import './landing.css';

const examples = [
  { name: 'Explain a request', icon: 'activity', label: 'REQUEST EXAMPLE', title: 'Every decision, in plain sight.', description: 'Follow a request from arrival to response. See what happened and why, one decision at a time.', rows: [
    ['Route matched', 'The longest matching route determines where this request can go.', 'routes'],
    ['Access checked', 'A valid, scoped credential allows this request to continue.', 'shield'],
    ['Capacity available', 'The tenant has room in its concurrent request allowance.', 'activity'],
    ['Response delivered', 'A healthy backend handles the request and returns its response.', 'check'],
  ] },
  { name: 'Preview a change', icon: 'routes', label: 'POLICY PREVIEW EXAMPLE', title: 'Understand the change before you apply it.', description: 'Compare routing and access decisions using retained request samples. Review the differences without sending requests again.', rows: [
    ['Draft a policy', 'Edit a route or access rule while the current configuration stays active.', 'code'],
    ['Compare past requests', 'Evaluate up to 100 retained samples using historical authentication facts.', 'search'],
    ['Review the differences', 'See changed routes, changed access decisions and any unknown results.', 'routes'],
    ['Apply separately', 'Apply the reviewed draft only if the configuration revision is still current.', 'check'],
  ] },
  { name: 'Explore a failure', icon: 'server', label: 'FAILURE LAB EXAMPLE', title: 'See resilience in action.', description: 'Run bounded experiments with disposable backends. Observe real failures and the gateway’s response as they happen.', rows: [
    ['Establish a baseline', 'Measure complete requests against healthy disposable backends.', 'activity'],
    ['Introduce a failure', 'Stop a demo backend or introduce latency in the isolated lab.', 'alert'],
    ['Observe the response', 'Inspect actual retry, failover and timeout decisions.', 'refresh'],
    ['Measure recovery', 'Restore the backend and compare success, errors and latency.', 'check'],
  ] },
];

function ProductShowcase() {
  return <figure className="gf-showcase" aria-label="Illustrative GateForge product previews">
    <div className="gf-showcase-plane" aria-hidden="true">
      <div className="gf-showcase-column">
        <div className="gf-preview gf-preview-trace">
          <div className="gf-preview-bar"><Icon name="shield"/><strong>GateForge</strong><span>REQUEST INSIGHTS</span></div>
          <div className="gf-preview-content"><span className="gf-preview-kicker">FROM REQUEST TO RESPONSE</span><h3>Every decision.<br/>An explanation.</h3><div className="gf-trace-list">{['Route matched','Access checked','Backend selected'].map(text=><div key={text}><span><Icon name="check"/></span><strong>{text}</strong><i/></div>)}</div></div>
          <div className="gf-preview-foot">Understand the why. <Icon name="arrow"/></div>
        </div>
        <div className="gf-preview gf-preview-policy"><div className="gf-preview-content"><span className="gf-preview-kicker">POLICY PREVIEW</span><h3>Change with<br/>confidence.</h3><div className="gf-policy-visual"><div><Icon name="code"/><span>Current policy</span></div><Icon name="arrow"/><div><Icon name="check"/><span>Proposed policy</span></div></div><p>Compare decisions. No traffic replay.</p></div></div>
      </div>
      <div className="gf-showcase-column gf-showcase-offset">
        <div className="gf-preview gf-preview-metrics"><div className="gf-preview-bar"><Icon name="activity"/><strong>Traffic overview</strong><span>DEMO</span></div><div className="gf-preview-content"><span className="gf-preview-kicker">A CLEARER PICTURE</span><h3>Know your traffic.</h3><div className="gf-chart" aria-hidden="true">{[24,42,35,56,44,65,50,76,62,86,73,96].map((height,i)=><i key={i} style={{height:`${height}%`}}/>)}</div><div className="gf-chart-labels"><span>Requests</span><span>Latency</span><span>Errors</span></div></div></div>
        <div className="gf-preview gf-preview-routing"><div className="gf-preview-content"><span className="gf-preview-kicker">RESILIENT BY DESIGN</span><h3>Keep requests<br/>moving.</h3><div className="gf-routing-visual"><span><Icon name="shield"/></span><i/><div><span><Icon name="server"/>Healthy</span><span><Icon name="refresh"/>Failover</span></div></div><p>Health checks. Retries. Recovery.</p></div></div>
      </div>
    </div>
    <figcaption>PRODUCT ILLUSTRATIONS · NO LIVE DATA</figcaption>
  </figure>;
}

export default function Landing({ gateway }) {
  const [signInOpen, setSignInOpen] = useState(false);
  const [exampleIndex, setExampleIndex] = useState(0);
  const [step, setStep] = useState(0);
  const example = examples[exampleIndex];
  const openSignIn = () => setSignInOpen(true);
  return <div className="gf-landing">
    <a className="skip-link" href="#front-content">Skip to content</a>
    <header className="gf-header gf-wrap">
      <a className="gf-brand" href="/admin/" aria-label="GateForge home"><Logo/></a>
      <nav aria-label="Website navigation"><a href="#platform">Platform</a><a href="#learn">How it works</a><a href="https://github.com/sean1-git/GateForge" target="_blank" rel="noreferrer">GitHub <Icon name="arrow"/></a></nav>
      <button className="gf-action gf-action-small" onClick={openSignIn}>Sign in <Icon name="arrow"/></button>
    </header>
    <main id="front-content">
      <section className="gf-hero" aria-labelledby="front-title"><div className="gf-wrap gf-hero-layout">
        <div className="gf-hero-copy"><p className="gf-eyebrow">GO API GATEWAY & DEVELOPER PLATFORM</p>
          <h1 id="front-title">Your backend.<br/>One powerful<br/>gateway.</h1>
          <p className="gf-intro">Give every request a clear path. Secure your APIs, keep traffic moving, and understand the decisions behind every response.</p>
          <div className="gf-hero-actions"><button className="gf-action" onClick={openSignIn}>Open control room <Icon name="arrow"/></button><a className="gf-text-link" href="#learn"><Icon name="book"/>See how it works</a></div>
          <p className="gf-hero-note">Built in Go. Deployed on Google Cloud. Open source.</p>
        </div>
        <ProductShowcase/>
      </div></section>
      <section className="gf-stack" aria-label="Gateway capabilities"><div className="gf-wrap"><p className="gf-eyebrow">ONE PLACE TO MANAGE THE REQUEST JOURNEY</p><ul>{['Authentication','Rate limiting','Smart routing','Response caching','Health checks','Request insights'].map((name,i)=><li key={name}><Icon name={['shield','clock','routes','cache','server','activity'][i]}/>{name}</li>)}</ul></div></section>
      <section id="platform" className="gf-platform" aria-labelledby="platform-title"><div className="gf-wrap">
        <div className="gf-section-heading"><p className="gf-eyebrow">CLARITY AT EVERY STEP</p><h2 id="platform-title">A gateway you can<br/><span>actually understand.</span></h2><p>Go beyond status codes. Explore the decisions behind a request, review a change, or find out what happens when something fails.</p></div>
        <div className="gf-example-tabs" aria-label="Choose a platform example">{examples.map((item,i)=><button key={item.name} aria-pressed={i===exampleIndex} onClick={()=>{setExampleIndex(i);setStep(0);}}><Icon name={item.icon}/>{item.name}</button>)}</div>
        <div className="gf-example-window">
          <div className="gf-window-bar"><div aria-hidden="true"><i/><i/><i/></div><span>GateForge / {example.name}</span><span className="gf-demo-label">Illustrative demo</span></div>
          <div className="gf-example-body"><aside><span className="gf-eyebrow">{example.label}</span><h3>{example.title}</h3><p>{example.description}</p><a className="gf-text-link" href="#learn">Try the interactive walkthrough <Icon name="arrow"/></a><small>These examples contain no live traffic or private backend details. Sign in to inspect your gateway.</small></aside>
            <div className="gf-example-steps">{example.rows.map(([title,description,icon],i)=><button key={title} aria-expanded={step===i} onClick={()=>setStep(i)} className={step===i?'is-selected':''}><span className="gf-step-number">0{i+1}</span><span className="gf-step-text"><strong>{title}</strong>{step===i&&<span>{description}</span>}</span><Icon name={icon}/></button>)}</div>
          </div>
        </div>
      </div></section>
      <section className="gf-principles"><div className="gf-wrap"><div className="gf-section-heading"><p className="gf-eyebrow">BUILT FOR THE HARD QUESTIONS</p><h2>Keep traffic moving.<br/><span>Keep the evidence.</span></h2></div><div className="gf-feature-grid">
        <article><span className="gf-feature-icon"><Icon name="shield"/></span><h3>Give every tenant room.</h3><p>Bound concurrent requests so a busy customer cannot occupy the entire shared request pool. Compare the behavior in the fairness lab.</p><span>PER-TENANT CONCURRENCY</span></article>
        <article><span className="gf-feature-icon"><Icon name="routes"/></span><h3>Review before you change.</h3><p>Preview routing and access changes against recent samples. See the differences before applying a policy, without replaying traffic.</p><span>OFFLINE POLICY PREVIEWS</span></article>
        <article><span className="gf-feature-icon"><Icon name="activity"/></span><h3>Measure what happens.</h3><p>Run isolated failure experiments and export latency, errors, retries and memory measurements. Make the results reproducible.</p><span>REPEATABLE EXPERIMENTS</span></article>
      </div></div></section>
      <section className="gf-evidence gf-wrap" aria-labelledby="evidence-title"><div><p className="gf-eyebrow">ENGINEERED. THEN MEASURED.</p><h2 id="evidence-title">Small details.<br/>Measurable impact.</h2><a className="gf-text-link" href="https://github.com/sean1-git/GateForge/blob/main/docs/performance.md" target="_blank" rel="noreferrer">Read the benchmarks <Icon name="arrow"/></a></div><div className="gf-stat"><strong>79.5<span>%</span></strong><h3>Fewer allocated bytes</h3><p>Per request in the response-buffer reuse microbenchmark.</p></div><div className="gf-stat"><strong>3.5<span>×</span></strong><h3>Faster metrics recording</h3><p>With 8 workers and 64 configured routes in the concurrency microbenchmark.</p></div><p className="gf-evidence-note">Measured local microbenchmarks. These results do not represent total process memory or production gateway throughput.</p></section>
      <section className="gf-bottom-cta gf-wrap"><div><p className="gf-eyebrow">YOUR NEXT REQUEST STARTS HERE</p><h2>See what your gateway<br/>is really doing.</h2></div><div><button className="gf-action" onClick={openSignIn}>Open control room <Icon name="arrow"/></button><a className="gf-text-link" href="/catalog/items" target="_blank" rel="noreferrer">Explore the public API <Icon name="arrow"/></a></div></section>
    </main>
    <footer className="gf-footer gf-wrap"><div className="gf-brand"><Logo/></div><p>API Gateway & Developer Platform<br/><span>Built by sean1-git.</span></p><a href="https://github.com/sean1-git/GateForge" target="_blank" rel="noreferrer">View source <Icon name="code"/></a></footer>
    <Modal open={signInOpen} onClose={()=>setSignInOpen(false)} busy={gateway.busy} title="Welcome to GateForge" description="Administrator access to your control room."><div className="p-6">{signInOpen && <SignIn gateway={gateway}/>}</div></Modal>
  </div>;
}

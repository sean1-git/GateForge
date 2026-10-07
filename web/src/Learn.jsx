import React, { useEffect, useRef, useState } from 'react';
import { initialSimulation, simulate, LOCAL_LIMITS, SHARED_LIMIT } from './learn-model.js';
import './learn.css';

const lessons = [
  { id: 'journey', label: 'One front door', title: 'One request. A clear path.', intro: 'An app asks for something. GateForge finds the right service and passes the request along.' },
  { id: 'limits', label: 'Rate limiting', title: 'Keep a busy app in check.', intro: 'A rate limit is a request budget. See what changes when one gateway checks that budget for every service.' },
  { id: 'balance', label: 'Load balancing', title: 'Share the work.', intro: 'Several servers can do the same job. The gateway chooses a healthy server for each new request.' },
];
const services = ['Users', 'Orders', 'Catalog'];
const paths = ['/users/42', '/orders/7', '/catalog/items'];

function Packet({ event, index, bypass }) {
  const motion = useRef(null);
  const fade = useRef(null);
  useEffect(() => {
    // Absolute SVG start times may already be in the past when a packet mounts;
    // start relative to this request so later interactions still animate.
    motion.current?.beginElementAt(index * 0.06);
    fade.current?.beginElementAt(index * 0.06);
  }, [index]);
  const y = [90, 210, 330][event.target];
  const path = event.target === null ? 'M215 210H480' : bypass ? `M215 210C440 210 500 ${y} 740 ${y}` : `M215 210H480C650 210 650 ${y} 740 ${y}`;
  return <circle r="4" className={`learn-packet ${event.accepted ? '' : 'is-blocked'}`}><animateMotion ref={motion} path={path} dur="1.25s" begin="indefinite" fill="freeze"/><animate ref={fade} attributeName="opacity" values="0;1;1;0" keyTimes="0;0.05;0.9;1" dur="1.4s" begin="indefinite" fill="freeze"/></circle>;
}

function Icon({ name }) {
  const shapes = {
    client: <><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8m-4-4v4"/></>,
    gate: <><path d="M12 3l8 4v5c0 5-8 9-8 9s-8-4-8-9V7z"/><path d="M8 12l3 3 5-6"/></>,
    server: <><rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M12 6.5h5M12 17.5h5"/></>,
    arrow: <path d="M5 12h14m-5-5 5 5-5 5"/>,
  };
  return <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{shapes[name]}</svg>;
}

export default function Learn({ onClose }) {
  useEffect(() => {
    const previous = document.title;
    document.title = 'GateForge · Explained';
    return () => { document.title = previous; };
  }, []);
  const [scene, setScene] = useState('journey');
  const [route, setRoute] = useState(0);
  const [centralized, setCentralized] = useState(true);
  const [weighted, setWeighted] = useState(false);
  const [offline, setOffline] = useState(false);
  const [playing, setPlaying] = useState(false);
  const [simulation, setSimulation] = useState(initialSimulation);
  const lesson = lessons.find(l => l.id === scene);
  const settings = { scene, route, centralized, weighted, offline };
  const change = (fn, value) => { fn(value); setPlaying(false); setSimulation(initialSimulation()); };
  const send = amount => setSimulation(s => simulate(s, settings, amount));
  useEffect(() => {
    if (!playing) return;
    const timer = setInterval(() => setSimulation(s => simulate(s, { scene, route, centralized, weighted, offline })), 1500);
    return () => clearInterval(timer);
  }, [playing, scene, route, centralized, weighted, offline]);
  const bypass = scene === 'limits' && !centralized;
  const balance = scene === 'balance';
  const description = scene === 'journey'
    ? `The ${paths[route]} path points to the ${services[route].toLowerCase()} service. Other services do not receive this request.`
    : scene === 'limits'
      ? centralized ? 'One shared budget allows the first 6 requests in this demo window. Extra requests stop at the gateway, before they reach a service.' : 'Each service has its own budget: 2, 4, or 6 requests. The same client can be blocked by one service while another still accepts it.'
      : offline ? 'Server A is unavailable. New requests go to B and C. In a real gateway, a health check must detect the failure first.' : weighted ? 'A 4:1:1 weighting sends 4 of every 6 requests to A, and 1 each to B and C. These weights are configured; memory size does not set them automatically.' : 'Round robin takes turns: A, then B, then C. Over a full cycle, each healthy server receives the same number of requests.';
  const result = simulation.sent === 0 ? 'Ready when you are. Send a request to follow its path.' : `${simulation.sent} sent · ${simulation.accepted} reached a server${simulation.blocked ? ` · ${simulation.blocked} stopped with 429 Too Many Requests` : ''}.`;

  return <div className="learn">
    <header className="learn-top"><a className="learn-brand" href="#learn"><span className="learn-mark">G</span>GateForge<span className="learn-divider"/><span className="learn-wordmark">EXPLAINED</span></a><button className="learn-back" onClick={onClose}>Back to control room <span aria-hidden="true">↗</span></button></header>
    <main className="learn-main">
      <div className="learn-heading"><div><p className="learn-kicker">BEHIND EVERY API REQUEST</p><h1>{lesson.title}</h1><p className="learn-intro">{lesson.intro}</p></div><span className="learn-mode"><span/>Interactive simulation</span></div>
      <nav className="learn-lessons" aria-label="Choose a gateway lesson">{lessons.map((l, i) => <button key={l.id} aria-pressed={scene === l.id} onClick={() => change(setScene, l.id)}><span className="learn-number">0{i + 1}</span>{l.label}<span className="learn-lesson-arrow" aria-hidden="true">↗</span></button>)}</nav>
      <section className="learn-stage" aria-label={lesson.label + ' simulation'}>
        <div className="learn-stage-top"><div className="learn-stage-title"><span className="learn-status-dot"/>{scene === 'journey' ? 'FOLLOW THE REQUEST' : scene === 'limits' ? (centralized ? 'ONE SHARED LIMIT' : 'SEPARATE SERVICE LIMITS') : (weighted ? 'WEIGHTED DISTRIBUTION' : 'ROUND ROBIN')}</div><span className="learn-stage-note">{balance && weighted ? 'Weighted routing · concept demo' : 'Illustrative traffic · no live requests'}</span></div>
        <div className="learn-options">
          {scene === 'journey' && <label>Request path<select value={route} onChange={e => change(setRoute, Number(e.target.value))}>{paths.map((path, i) => <option key={path} value={i}>{path}</option>)}</select></label>}
          {scene === 'limits' && <div className="learn-segment" role="group" aria-label="Where limits are checked"><button aria-pressed={!centralized} onClick={() => change(setCentralized, false)}>Without a gateway</button><button aria-pressed={centralized} onClick={() => change(setCentralized, true)}>With GateForge</button></div>}
          {balance && <><div className="learn-segment" role="group" aria-label="Distribution strategy"><button aria-pressed={!weighted} onClick={() => change(setWeighted, false)}>Equal turns</button><button aria-pressed={weighted} onClick={() => change(setWeighted, true)}>Weighted 4:1:1</button></div><label className="learn-switch"><input type="checkbox" checked={offline} onChange={e => change(setOffline, e.target.checked)}/>Take server A offline</label></>}
        </div>
        <div className={`learn-diagram${bypass ? ' is-bypass' : ''}`}>
          <svg className="learn-wires" viewBox="0 0 1000 420" preserveAspectRatio="none" aria-hidden="true">
            {!bypass && <path d="M215 210H380" className="learn-wire"/>}
            {[90, 210, 330].map((y, i) => <path key={y} className={`learn-wire${balance && offline && i === 0 ? ' is-offline' : ''}${scene === 'journey' && i === route ? ' is-selected' : ''}`} d={bypass ? `M215 210C440 210 500 ${y} 740 ${y}` : `M580 210C650 210 650 ${y} 740 ${y}`}/>)}
            {simulation.recent.map((event, i) => <Packet key={event.id} event={event} index={i} bypass={bypass}/>) }
          </svg>
          <div className="learn-source"><div className="learn-node learn-client"><div className="learn-icon"><Icon name="client"/></div><strong>{scene === 'limits' ? 'One busy app' : 'Your app'}</strong><span>{simulation.sent} requests sent</span><code>{scene === 'journey' ? paths[route] : balance ? 'GET /users' : 'GET /service'}</code></div><span className="learn-node-caption">The client asks for something.</span></div>
          <div className="learn-middle">{bypass ? <div className="learn-no-gate"><span aria-hidden="true">↗</span><strong>No shared checkpoint</strong><span>Every service decides for itself.</span></div> : <div className="learn-node learn-gate"><div className="learn-gate-symbol"><Icon name="gate"/></div><span className="learn-gate-label">GATEFORGE</span><h2>{scene === 'journey' ? 'The front door' : scene === 'limits' ? 'The traffic guard' : 'The load balancer'}</h2><span>{scene === 'journey' ? 'Match the path. Find the service.' : scene === 'limits' ? 'Check the budget before forwarding.' : 'Choose a healthy server.'}</span><div className="learn-gate-rule">{scene === 'journey' ? paths[route].split('/').slice(0, 2).join('/') + ' → ' + services[route] : scene === 'limits' ? `${Math.max(0, SHARED_LIMIT - simulation.accepted)} of 6 requests left` : offline ? 'B → C → repeat' : weighted ? 'A : B : C = 4 : 1 : 1' : 'A → B → C → repeat'}</div>{scene === 'limits' && simulation.blocked > 0 && <div className="learn-rejected">{simulation.blocked} stopped · 429</div>}</div>}</div>
          <div className="learn-destinations">{services.map((name, i) => {
            const down = balance && offline && i === 0;
            const selected = scene !== 'journey' || route === i;
            const share = simulation.accepted ? Math.round(simulation.counts[i] / simulation.accepted * 100) : 0;
            return <div key={name} className={`learn-node learn-server${selected ? ' is-selected' : ''}${down ? ' is-down' : ''}`}><div className="learn-server-heading"><Icon name="server"/><strong>{balance ? `Server ${'ABC'[i]}` : name}</strong><span className="learn-server-state">{down ? 'Offline' : 'Ready'}</span></div><div className="learn-server-detail"><span>{balance ? 'Same service · replica ' + (i + 1) : scene === 'limits' ? (centralized ? 'Shared limit at gateway' : `${LOCAL_LIMITS[i]} requests / demo window`) : 'Handles ' + paths[i].split('/')[1]}</span></div><div className="learn-server-bottom"><span>{simulation.counts[i]} received</span>{balance && <span>{share}% of delivered</span>}</div>{balance && <meter min="0" max="100" value={share} aria-label={`Server ${'ABC'[i]} share of delivered requests`}>{share}%</meter>}</div>;
          })}</div>
        </div>
        <div className="learn-transport"><div className="learn-playback"><button className="learn-primary" onClick={() => send(1)}>Send request <Icon name="arrow"/></button>{scene !== 'journey' && <button onClick={() => send(12)}>Send burst of 12</button>}<button className="learn-play" aria-pressed={playing} onClick={() => setPlaying(!playing)}>{playing ? 'Ⅱ Pause' : '▷ Auto play'}</button><button className="learn-reset" onClick={() => { setPlaying(false); setSimulation(initialSimulation()); }}>{scene === 'limits' ? 'Reset window' : 'Reset'}</button></div><div className="learn-legend"><span><i/>Allowed</span>{scene === 'limits' && <span><i className="is-blocked"/>Limited (429)</span>}</div></div>
        <p className="learn-result" role="status" aria-live={playing ? 'off' : 'polite'}>{result}</p>
      </section>
      <section className="learn-explanation"><div><span className="learn-explain-icon" aria-hidden="true">↳</span><div><h2>What you’re seeing</h2><p>{description}</p></div></div><div className="learn-takeaway"><span>THE SIMPLE IDEA</span><p>{scene === 'journey' ? 'One address. The right destination.' : scene === 'limits' ? 'Let services focus on their job.' : 'More healthy servers, shared work.'}</p></div></section>
      <footer className="learn-footer"><span>Built to explain, safe to explore. Controls only change this simulation.</span><span>{balance && weighted ? 'Concept demo: weighted routing is not implemented in GateForge.' : scene === 'limits' ? 'Demo budgets are illustrative; live limits come from route configuration.' : 'GateForge / An API gateway, made visible.'}</span></footer>
    </main>
  </div>;
}

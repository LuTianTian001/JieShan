import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './app/App';
import './styles/tokens.css';
import './styles/base.css';
import './styles/pages.css';
import './styles/overview-sites-polish.css';
import './styles/routing-monitor-polish.css';
import './styles/core-polish.css';
import './styles/desktop-final-fixes.css';

function prototypeRequested(): boolean {
  const query = new URLSearchParams(window.location.search).get('prototype');
  try {
    if (query === '1') sessionStorage.setItem('jieshan.prototype', '1');
    if (query === '0') sessionStorage.removeItem('jieshan.prototype');
    return query === '1'
      || import.meta.env.VITE_JIESHAN_PROTOTYPE === 'true'
      || sessionStorage.getItem('jieshan.prototype') === '1';
  } catch {
    return query === '1' || import.meta.env.VITE_JIESHAN_PROTOTYPE === 'true';
  }
}

async function start() {
  if (prototypeRequested()) {
    const { installPrototypeFetch } = await import('./prototype/mockApi');
    installPrototypeFetch();
    document.documentElement.dataset.prototype = 'true';
  }

  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>,
  );
}

void start();

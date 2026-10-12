import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@opensignage/ui';
import './app.css';
import { installUiFonts } from './lib/fonts';
import { App } from './App';

installUiFonts();
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

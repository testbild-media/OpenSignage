// Operator UI entry (/control). Implemented in src/control/Control.tsx.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@opensignage/ui';
import '../app.css';
import './control.css';
import { installUiFonts } from '../lib/fonts';
import { ControlApp } from './Control';

installUiFonts();
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ControlApp />
  </StrictMode>,
);

import { MapPinned } from 'lucide-react';
import type { Feature } from '../types';
import { ZonesPage } from './ZonesPage';
import { TickerTemplatesPage } from './TickerTemplatesPage';
import '../../components/controls.css';
import './zones.css';

export const zonesFeature: Feature = {
  routes: [
    { path: '/zones', element: <ZonesPage />, minRole: 'operator' },
    { path: '/zones/tickers', element: <TickerTemplatesPage />, minRole: 'operator', visible: (r) => r.canManageGlobal },
  ],
  nav: [
    {
      key: 'zones',
      label: 'admin.nav.zones',
      icon: MapPinned,
      path: '/zones',
      minRole: 'operator',
      order: 60,
      sub: [{ key: 'tickers', label: 'admin.zones.tickers.title', path: '/zones/tickers', minRole: 'operator', visible: (r) => r.canManageGlobal }],
    },
  ],
};

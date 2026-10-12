import { CalendarClock } from 'lucide-react';
import type { Feature } from '../types';
import { SchedulesPage } from './SchedulesPage';
import '../../components/controls.css';
import './schedules.css';

export const schedulesFeature: Feature = {
  routes: [{ path: '/schedules', element: <SchedulesPage />, minRole: 'operator' }],
  nav: [{ key: 'schedules', label: 'admin.nav.schedules', icon: CalendarClock, path: '/schedules', minRole: 'operator', order: 50 }],
};

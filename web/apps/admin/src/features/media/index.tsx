import { Images } from 'lucide-react';
import type { Feature } from '../types';
import { MediaPage } from './MediaPage';
import { TrashPage } from './TrashPage';
import { UnusedPage } from './UnusedPage';

export const mediaFeature: Feature = {
  routes: [
    { path: '/media', element: <MediaPage />, minRole: 'operator' },
    { path: '/media/folder/:id', element: <MediaPage />, minRole: 'operator' },
    {
      path: '/media/trash',
      element: <TrashPage />,
      minRole: 'operator',
      visible: (r) => r.canMedia('manage'),
    },
    {
      path: '/media/unused',
      element: <UnusedPage />,
      minRole: 'operator',
      visible: (r) => r.canMedia('manage'),
    },
  ],
  nav: [
    {
      key: 'media',
      label: 'admin.nav.media',
      icon: Images,
      path: '/media',
      minRole: 'operator',
      order: 10,
      sub: [
        {
          key: 'trash',
          label: 'admin.nav.mediaTrash',
          path: '/media/trash',
          minRole: 'operator',
          visible: (r) => r.canMedia('manage'),
        },
        {
          key: 'unused',
          label: 'admin.nav.mediaUnused',
          path: '/media/unused',
          minRole: 'operator',
          visible: (r) => r.canMedia('manage'),
        },
      ],
    },
  ],
};

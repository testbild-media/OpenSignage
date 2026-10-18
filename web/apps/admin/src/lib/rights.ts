// Rights of the logged-in user (server: internal/server/core/rights.go). Admins and operators
// of all zones may do everything their role allows; operators limited to zones see only
// their zones (view or edit) and have separate rights for new content, media, products and
// displays. The server checks every request; the WebGUI only hides what would be refused.
import { useMemo } from 'react';
import type { Role } from '@opensignage/contracts';
import { useSession } from './session';

export type ZoneAccess = 'view' | 'edit';
export type MediaRight = 'use' | 'upload' | 'manage';

/** Rights as the session reports them (effective values). */
export interface SessionRights {
  scope: 'all' | 'zones';
  zones: Record<string, ZoneAccess>;
  createContent: boolean;
  media: MediaRight | '';
  products: 'view' | 'edit';
  devices: boolean;
}

/** Rights on one presentation or playlist (lists and details). */
export interface ContentAccess {
  view: boolean;
  edit: boolean;
  /** Visible but read only: it also runs in zones the user may not change. */
  shared: boolean;
}

const mediaRank: Record<string, number> = { '': 0, use: 1, upload: 2, manage: 3 };

export interface Rights {
  /** Admin or operator of all zones. */
  unrestricted: boolean;
  zoneAccess: (zoneId: string | null | undefined) => ZoneAccess | '';
  canViewZone: (zoneId: string | null | undefined) => boolean;
  canEditZone: (zoneId: string | null | undefined) => boolean;
  canCreateContent: boolean;
  canMedia: (level: MediaRight) => boolean;
  canEditProducts: boolean;
  canControlDevice: (zoneId: string | null | undefined) => boolean;
  /** Changes global things: global rules, zones, display groups, ticker and presentation templates. */
  canManageGlobal: boolean;
}

export function rightsOf(role: Role | undefined, r: SessionRights | undefined): Rights {
  const unrestricted = role === 'admin' || (role === 'operator' && r?.scope !== 'zones');
  const zoneAccess = (z: string | null | undefined): ZoneAccess | '' => (!z ? '' : unrestricted ? 'edit' : (r?.zones[z] ?? ''));
  const canEditZone = (z: string | null | undefined): boolean => zoneAccess(z) === 'edit';
  const operator = role === 'operator' || role === 'admin';
  return {
    unrestricted,
    zoneAccess,
    canViewZone: (z) => zoneAccess(z) !== '',
    canEditZone,
    canCreateContent: unrestricted || (operator && !!r?.createContent),
    canMedia: (level) => unrestricted || (operator && (mediaRank[r?.media ?? ''] ?? 0) >= (mediaRank[level] ?? 9)),
    canEditProducts: unrestricted || (operator && r?.products === 'edit'),
    canControlDevice: (z) => unrestricted || (operator && !!r?.devices && canEditZone(z)),
    canManageGlobal: unrestricted,
  };
}

export function useRights(): Rights {
  const { user } = useSession();
  return useMemo(() => rightsOf(user?.role, user?.rights), [user]);
}

/** Full access (content of admins and operators of all zones, and older servers). */
export const FULL_ACCESS: ContentAccess = { view: true, edit: true, shared: false };

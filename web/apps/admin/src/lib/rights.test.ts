import { describe, expect, it } from 'vitest';
import { rightsOf, type SessionRights } from './rights';

const limited: SessionRights = { scope: 'zones', zones: { bar2: 'edit', bar3: 'view' }, createContent: false, media: 'upload', products: 'view', devices: true };

describe('rights', () => {
  it('admins and operators of all zones may do everything', () => {
    for (const r of [rightsOf('admin', limited), rightsOf('operator', { ...limited, scope: 'all' })]) {
      expect(r.unrestricted).toBe(true);
      expect(r.canEditZone('any')).toBe(true);
      expect(r.canMedia('manage')).toBe(true);
      expect(r.canCreateContent && r.canEditProducts && r.canManageGlobal).toBe(true);
    }
  });

  it('operators limited to zones', () => {
    const r = rightsOf('operator', limited);
    expect(r.unrestricted).toBe(false);
    expect([r.zoneAccess('bar2'), r.zoneAccess('bar3'), r.zoneAccess('bar1'), r.zoneAccess(null)]).toEqual(['edit', 'view', '', '']);
    expect(r.canViewZone('bar3') && !r.canEditZone('bar3')).toBe(true);
    expect([r.canMedia('use'), r.canMedia('upload'), r.canMedia('manage')]).toEqual([true, true, false]);
    expect(r.canCreateContent || r.canEditProducts || r.canManageGlobal).toBe(false);
    expect([r.canControlDevice('bar2'), r.canControlDevice('bar3')]).toEqual([true, false]);
  });

  it('users only operate their zones live', () => {
    const r = rightsOf('user', { ...limited, zones: { bar2: 'edit' }, media: '' });
    expect(r.canEditZone('bar2')).toBe(true);
    expect(r.canMedia('use') || r.canCreateContent || r.canControlDevice('bar2')).toBe(false);
  });
});

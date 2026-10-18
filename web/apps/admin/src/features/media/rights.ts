// Media rights of the logged-in user (lib/rights.ts): "use" browses, "upload" adds files and
// folders and changes own ones, "manage" (admins, operators of all zones) changes everything.
import { useMemo } from 'react';
import { useRights } from '../../lib/rights';
import { useSession } from '../../lib/session';
import type { MediaAsset, MediaFolder } from './model';

export interface MediaRights {
  upload: boolean;
  manage: boolean;
  /** Rename, move or replace this file. */
  ownAsset: (a: Pick<MediaAsset, 'uploadedBy'>) => boolean;
  /** Rename or move this folder. */
  ownFolder: (f: Pick<MediaFolder, 'createdBy'>) => boolean;
}

export function useMediaRights(): MediaRights {
  const rights = useRights();
  const { user } = useSession();
  return useMemo(() => {
    const manage = rights.canMedia('manage');
    const upload = rights.canMedia('upload');
    const me = user?.id ?? '';
    return {
      upload,
      manage,
      ownAsset: (a) => manage || (upload && !!a.uploadedBy && a.uploadedBy === me),
      ownFolder: (f) => manage || (upload && !!f.createdBy && f.createdBy === me),
    };
  }, [rights, user]);
}

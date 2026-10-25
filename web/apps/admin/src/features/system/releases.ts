// Which releases the updates page lists.

export interface ReleaseLike {
  version: string;
  state: 'available' | 'downloading' | 'ready' | 'failed';
  newer: boolean;
}

/**
 * Releases with files on this server (or being loaded) and newer ones. Older releases that
 * are only on GitHub stay hidden: they cannot be installed (no downgrades) and only make the
 * page long. `loading`: the version of a download running now.
 */
export function visibleReleases<T extends ReleaseLike>(list: T[], loading?: string): T[] {
  return list.filter((r) => r.newer || r.state !== 'available' || r.version === loading);
}

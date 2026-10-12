// API shapes of zones, ticker templates (catalog module) and playlist summaries (content
// module) used by the zone and schedule pages.
import type { TickerTemplate, ZoneState } from '@opensignage/contracts';

export interface Zone {
  id: string;
  name: string;
  state: ZoneState;
  closedPlaylistId?: string;
  tickerTemplateId?: string;
  sort: number;
  deviceCount: number;
  /** Optimistic concurrency: every PATCH sends the revision it is based on. */
  revision: number;
  createdAt: string;
  updatedAt: string;
  /** Display groups; older servers send none. */
  groups: DisplayGroup[];
}

/** A named row of screens inside a zone, optionally with its own playlist for CLOSED. */
export interface DisplayGroup {
  id: string;
  zoneId: string;
  name: string;
  closedPlaylistId?: string;
  sort: number;
  deviceCount: number;
  revision: number;
}

export interface ZoneUsage {
  devices: number;
  rules: number;
  users: number;
}

export interface TickerTemplateRecord {
  id: string;
  name: string;
  template: TickerTemplate;
  /** Optimistic concurrency: every PATCH sends the revision it is based on. */
  revision: number;
  createdAt: string;
  updatedAt: string;
}

export interface TickerTemplateList {
  tickerTemplates: TickerTemplateRecord[];
  default: TickerTemplate;
}

export interface PlaylistSummary {
  id: string;
  name: string;
  itemCount: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  archived: boolean;
  updatedAt: string;
}

/** Playlists a zone or rule can run: published and not archived. */
export function playablePlaylists(list: PlaylistSummary[] | undefined): PlaylistSummary[] {
  return (list ?? []).filter((p) => p.publishedVersion !== null && !p.archived);
}

/**
 * Options for a playlist select. A referenced playlist that is not playable (never
 * published) stays visible so the current value is never silently replaced.
 */
export function playlistOptions(
  list: PlaylistSummary[] | undefined,
  current: string | undefined,
  none: string,
  notPublished: (name: string) => string,
): { value: string; label: string }[] {
  const playable = playablePlaylists(list);
  const out = [{ value: '', label: none }, ...playable.map((p) => ({ value: p.id, label: p.name }))];
  if (current && !playable.some((p) => p.id === current)) {
    const p = (list ?? []).find((x) => x.id === current);
    out.push({ value: current, label: notPublished(p?.name ?? current) });
  }
  return out;
}

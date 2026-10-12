import type { ComponentType, ReactNode } from 'react';
import type { Role } from '@opensignage/contracts';

/** A route provided by a feature module. */
export interface RouteDef {
  path: string; // react-router pattern, e.g. "/media/folder/:id"
  element: ReactNode;
  minRole: Role;
  /** Full-bleed pages (editor) collapse the sidebar to icons. */
  fullscreen?: boolean;
}

/** A sidebar child entry (real objects like presentations or categories). */
export interface NavChild {
  key: string;
  label: string;
  path: string;
}

export interface NavDef {
  key: string;
  label: string; // i18n key
  icon: ComponentType<{ size?: number }>;
  path: string;
  minRole: Role;
  order: number;
  /** Static sub entries (i18n label keys). */
  sub?: { key: string; label: string; path: string; minRole: Role }[];
  /** Hook returning dynamic children (loaded async; the group must expand when they arrive). */
  useChildren?: () => NavChild[];
  /** Shown at the bottom of the sidebar, above the account (rarely used, e.g. settings). */
  pinBottom?: boolean;
}

/** Each feature folder exports one Feature; only features with routes appear in the UI. */
export interface Feature {
  routes: RouteDef[];
  nav: NavDef[];
}

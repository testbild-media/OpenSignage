// Version history of a presentation: published versions grouped into editing phases (the
// server assigns each version the first version of its phase).

export interface Version {
  version: number;
  publishedAt: string;
  publishedBy: string;
  slideCount: number;
  /** Name of a version kept for good ('' = unnamed). */
  label: string;
  /** First version of the editing phase. */
  phase: number;
}

export interface HistoryRules {
  phaseGapMinutes: number;
  thinAfterDays: number;
  keepPhases: number;
  maxNamed: number;
  maxLabel: number;
}

export interface Phase {
  id: number;
  /** Newest first; versions[0] is the state the phase ended with. */
  versions: [Version, ...Version[]];
}

/** Groups versions (newest first) into editing phases (newest first). */
export function groupPhases(versions: Version[]): Phase[] {
  const out: Phase[] = [];
  for (const v of versions) {
    const cur = out[out.length - 1];
    if (cur && cur.id === v.phase) cur.versions.push(v);
    else out.push({ id: v.phase, versions: [v] });
  }
  return out;
}

/** Time span of a phase ("17 Oct 2026, 14:02 – 14:51"); a single time for one version. */
export function phaseTime(p: Phase, fmt: Intl.DateTimeFormat): string {
  const last = new Date(p.versions[0].publishedAt);
  const first = new Date((p.versions.at(-1) ?? p.versions[0]).publishedAt);
  if (p.versions.length === 1 || Number.isNaN(first.getTime()) || Number.isNaN(last.getTime()) || first >= last) return fmt.format(last);
  return fmt.formatRange(first, last);
}

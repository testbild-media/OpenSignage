// One screen of the zone preview, rendered like a player: the same stage, sequencer and
// ticker overlay, fed with the deployment, the live state and the schedule of the zone.
import { useEffect, useRef } from 'react';
import type { Presentation, ScaleMode, Slide, Typography } from '@opensignage/contracts';
import { LiveStore, Sequencer, Stage, ZoneTickerOverlay, zoneTickerSuppressed, type RenderEnv, type SequencerContent } from '@opensignage/render';
import { editorAssets } from '../presentations/editor/env';

/** Media events that start playback: every medium of the preview stays silent. */
const MEDIA_START = ['loadstart', 'play', 'volumechange'];

export function ScreenTile({
  content,
  live,
  typography,
  playlistId,
  anchor,
  position,
  scaleMode,
  width,
  height,
  label,
}: {
  content: SequencerContent;
  live: LiveStore;
  /** Typography for slides of media items (no presentation of their own). */
  typography: Typography | undefined;
  playlistId: string | null;
  anchor: number | null;
  position: number;
  scaleMode: ScaleMode;
  width: number;
  height: number;
  label: string;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const tickerRef = useRef<HTMLDivElement>(null);
  const seq = useRef<Sequencer | null>(null);
  const typo = useRef(typography);
  typo.current = typography;

  useEffect(() => {
    const host = hostRef.current!;
    const tickerEl = tickerRef.current!;
    const stage = new Stage(host, scaleMode);
    const overlay = new ZoneTickerOverlay(tickerEl);
    let slide: Slide | null = null;
    const updateTicker = (): void => overlay.update(zoneTickerSuppressed(slide) ? null : live.ticker, host.clientHeight / 1080 || 0.25);
    const envFor = (p: Presentation | null): RenderEnv => ({
      typography: p?.typography ?? (typo.current as Typography),
      assets: editorAssets,
      live,
      mode: 'play',
    });
    const sequencer = new Sequencer(stage, envFor, {
      onSlide(s) {
        slide = s;
        updateTicker();
      },
      now: () => live.now().getTime(), // the previewed moment, running on
    });
    seq.current = sequencer;
    const mute = (e: Event): void => {
      const m = e.target as HTMLMediaElement;
      if (!m.muted) m.muted = true;
    };
    for (const ev of MEDIA_START) host.addEventListener(ev, mute, true);
    const unsub = live.subscribe(updateTicker);
    const resize = new ResizeObserver(updateTicker);
    resize.observe(host);
    return () => {
      unsub();
      resize.disconnect();
      for (const ev of MEDIA_START) host.removeEventListener(ev, mute, true);
      sequencer.stop();
      overlay.stop();
      stage.destroy();
      seq.current = null;
    };
    // A new stage only for another scale mode; content and source are set below.
  }, [live, scaleMode]);

  useEffect(() => {
    seq.current?.setContent(content);
  }, [content, scaleMode]);

  useEffect(() => {
    seq.current?.play(playlistId, anchor, position);
  }, [playlistId, anchor, position, content, scaleMode]);

  return (
    <div className="preview-screen" style={{ aspectRatio: `${width} / ${height}` }} role="img" aria-label={label}>
      <div className="preview-stage" ref={hostRef} />
      <div className="preview-ticker hidden" ref={tickerRef} />
    </div>
  );
}

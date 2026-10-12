// Preview: plays the presentation with the player's Stage + Sequencer (identical output).
import { useEffect, useRef } from 'react';
import { X } from 'lucide-react';
import type { Presentation } from '@opensignage/contracts';
import { Sequencer, Stage, type LiveStore } from '@opensignage/render';
import { useT } from '../../../lib/hooks';
import { editorAssets } from './env';

export function Preview({ doc, live, startSlideId, onClose }: { doc: Presentation; live: LiveStore; startSlideId: string; onClose: () => void }) {
  const t = useT();
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!host.current) return;
    const stage = new Stage(host.current, 'contain');
    // Start at the current slide: rotate the slide order.
    const idx = Math.max(0, doc.slides.findIndex((s) => s.id === startSlideId));
    const rotated: Presentation = { ...doc, slides: [...doc.slides.slice(idx), ...doc.slides.slice(0, idx)] };
    const seq = new Sequencer(stage, () => ({ typography: doc.typography, assets: editorAssets, live, mode: 'play' }));
    seq.setContent({
      presentations: new Map([[doc.id, rotated]]),
      playlists: new Map([['preview', { id: 'preview', name: '', items: [{ id: 'i', kind: 'presentation', refId: doc.id, play: { mode: 'repeat', count: 1 } }] }]]),
      media: new Map(),
    });
    seq.play('preview');
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
      seq.stop();
      stage.destroy();
    };
  }, [doc, live, startSlideId, onClose]);
  return (
    <div className="preview-overlay">
      <div className="preview-stage" ref={host} />
      <button type="button" className="preview-close" onClick={onClose} title={t('common.action.close')}>
        <X size={20} />
      </button>
    </div>
  );
}

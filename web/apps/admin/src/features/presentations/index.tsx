import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { Copy, LayoutTemplate, Plus, Presentation as PresIcon, Trash2 } from 'lucide-react';
import type { Slide, Typography } from '@opensignage/contracts';
import { renderThumbnail, type LiveStore } from '@opensignage/render';
import type { Feature, NavChild } from '../types';
import { del, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { loadCustomFonts } from '../../lib/fonts';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, ErrorText, IconButton, PageHeader, Select, TextInput } from '../../components/ui';
import { EditorPage } from './editor/Editor';
import { editorAssets, useEditorLive } from './editor/env';
import { ASPECTS, RESOLUTION_PRESETS, resolutionForAspect } from './editor/factory';

interface PresSummary {
  id: string;
  name: string;
  width: number;
  height: number;
  slideCount: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  updatedAt: string;
  firstSlide: Slide | null;
  /** Text styles of the draft (product lists and single products in the thumbnail). */
  typography: Typography | null;
}

// Only for a draft without text styles (the server always stores them).
const typo = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' as const };
const fallbackTypography: Typography = { heading: typo, subheading: typo, item: typo, description: typo, price: typo };

/** First slide of the draft as the screen shows it: the presentation's own text styles and,
 * once loaded, the uploaded fonts. */
function Card({ p, live, fonts }: { p: PresSummary; live: LiveStore; fonts: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!ref.current || !p.firstSlide) return;
    return renderThumbnail(ref.current, p.firstSlide, p.width, p.height, {
      typography: p.typography ?? fallbackTypography,
      assets: editorAssets,
      live,
      mode: 'thumb',
    });
  }, [p, live, fonts]);
  return <div className="pres-thumb" ref={ref} />;
}

function NewDialog({ onClose }: { onClose: () => void }) {
  const t = useT();
  const navigate = useNavigate();
  const templates = useQuery<{ presentations: PresSummary[] }>('/api/v1/presentations?templates=1');
  const [name, setName] = useState('');
  const [size, setSize] = useState({ w: 1920, h: 1080 });
  const [templateId, setTemplateId] = useState('');
  const create = useAction(async () => {
    // A template keeps its own design size; otherwise the chosen resolution is used.
    const r = await post<{ id: string }>('/api/v1/presentations', templateId ? { name: name.trim(), templateId } : { name: name.trim(), width: size.w, height: size.h });
    navigate(`/presentations/${r.id}`);
  });
  return (
    <Dialog
      title={t('pres.new')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={create.busy} disabled={!name.trim()} onClick={() => void create.run()}>
            {t('pres.create')}
          </Button>
        </>
      }
    >
      <div className="form">
        <TextInput label={t('fields.name')} value={name} autoFocus onChange={(e) => setName(e.target.value)} />
        <Select
          label={t('editor.pres.preset')}
          value={`${size.w}x${size.h}`}
          onChange={(e) => {
            const [w, h] = e.target.value.split('x').map(Number);
            setSize({ w: w!, h: h! });
          }}
          options={[...RESOLUTION_PRESETS.map((p) => ({ value: `${p.w}x${p.h}`, label: `${p.label} (${p.w}×${p.h})` }))]}
        />
        <Select
          label={t('editor.pres.aspect')}
          value=""
          onChange={(e) => {
            const a = ASPECTS[Number(e.target.value)];
            if (a) {
              const r = resolutionForAspect(a.w, a.h);
              setSize({ w: r.w, h: r.h });
            }
          }}
          options={[{ value: '', label: '—' }, ...ASPECTS.map((a, i) => ({ value: String(i), label: a.label }))]}
        />
        <div className="field-hint">
          {size.w} × {size.h}
        </div>
        {(templates.data?.presentations.length ?? 0) > 0 && (
          <Select
            label={t('pres.template')}
            value={templateId}
            onChange={(e) => setTemplateId(e.target.value)}
            options={[{ value: '', label: t('pres.noTemplate') }, ...(templates.data?.presentations ?? []).map((p) => ({ value: p.id, label: p.name }))]}
          />
        )}
        <ErrorText error={create.error} />
      </div>
    </Dialog>
  );
}

function PresentationsPage() {
  const t = useT();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const toast = useToast();
  const [creating, setCreating] = useState(false);
  const live = useEditorLive();
  const list = useQuery<{ presentations: PresSummary[] }>('/api/v1/presentations');
  useEvents(['content'], () => list.reload());
  // Uploaded fonts: the thumbnails are drawn again once their @font-face rules are installed.
  const [fonts, setFonts] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadCustomFonts(true).then(() => alive && setFonts(true));
    return () => {
      alive = false;
    };
  }, []);
  const duplicate = useAction(async (p: PresSummary) => {
    await post(`/api/v1/presentations/${p.id}/duplicate`);
    list.reload();
  });
  const remove = useAction(async (p: PresSummary) => {
    const ok = await confirm({ title: t('pres.delete'), message: t('pres.deleteConfirm', { name: p.name }), danger: true, confirmLabel: t('common.action.delete') });
    if (!ok) return;
    await del(`/api/v1/presentations/${p.id}`);
    toast(t('pres.deleted'));
    list.reload();
  });
  const template = useAction(async (p: PresSummary) => {
    await post(`/api/v1/presentations/${p.id}/save-as-template`, { name: p.name });
    toast(t('pres.templateSaved'));
  });
  return (
    <div className="page">
      <PageHeader
        title={t('admin.nav.presentations')}
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setCreating(true)}>
            {t('pres.new')}
          </Button>
        }
      />
      <ErrorText error={remove.error ?? duplicate.error ?? template.error} />
      <DataState loading={list.loading && !list.data} error={list.error} onRetry={list.reload} empty={list.data?.presentations.length === 0} emptyText={t('pres.empty')}>
        <div className="pres-grid">
          {list.data?.presentations.map((p) => (
            <div key={p.id} className="pres-card" onClick={() => navigate(`/presentations/${p.id}`)}>
              <Card p={p} live={live} fonts={fonts} />
              <div className="pres-info">
                <div className="pres-name">{p.name}</div>
                <div className="faint">
                  {p.width}×{p.height} · {t('pres.slides', { count: p.slideCount })}
                </div>
                <div>{p.publishedVersion ? p.hasChanges ? <Badge tone="warning">{t('editor.changesNotPublished')}</Badge> : <Badge tone="success">{t('pres.published')}</Badge> : <Badge>{t('editor.neverPublished')}</Badge>}</div>
              </div>
              <div className="pres-actions" onClick={(e) => e.stopPropagation()}>
                <IconButton label={t('editor.duplicate')} onClick={() => void duplicate.run(p)}>
                  <Copy size={16} />
                </IconButton>
                <IconButton label={t('pres.saveTemplate')} onClick={() => void template.run(p)}>
                  <LayoutTemplate size={16} />
                </IconButton>
                <IconButton label={t('common.action.delete')} onClick={() => void remove.run(p)}>
                  <Trash2 size={16} />
                </IconButton>
              </div>
            </div>
          ))}
        </div>
      </DataState>
      {creating && <NewDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

function usePresentationChildren(): NavChild[] {
  const q = useQuery<{ presentations: PresSummary[] }>('/api/v1/presentations');
  useEvents(['content'], () => q.reload());
  return (q.data?.presentations ?? []).map((p) => ({ key: p.id, label: p.name, path: `/presentations/${p.id}` }));
}

export const presentationsFeature: Feature = {
  routes: [
    { path: '/presentations', element: <PresentationsPage />, minRole: 'operator' },
    { path: '/presentations/:id', element: <EditorPage />, minRole: 'operator', fullscreen: true },
  ],
  nav: [{ key: 'presentations', label: 'admin.nav.presentations', icon: PresIcon, path: '/presentations', minRole: 'operator', order: 30, useChildren: usePresentationChildren }],
};

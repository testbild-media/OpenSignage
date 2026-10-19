// Right panel, by what is selected: the presentation (nothing selected), the slide, the
// element(s) – each with its own tab – and the layer list of the slide.
import { useState, type ReactNode } from 'react';
import { Bold, Eye, EyeOff, GripVertical, Italic, Layers as LayersIcon, Lock, Pin, Presentation as PresentationIcon, RectangleHorizontal, RotateCcw, SquareMousePointer, Underline, Unlock } from 'lucide-react';
import type { Background, Marks, ProductOverrides, Slide, SlideElement, TextStyle, Typography } from '@opensignage/contracts';
import { useT } from '../../../lib/hooks';
import { Button, Dialog } from '../../../components/ui';
import { MediaPickerButton, SourcePicker } from './pickers';
import { ColorInput, FontSelect, Num, Row } from './controls';
import type { FontOption } from './env';
import type { EditorStore } from './store';
import { useEditor } from './useEditor';
import { resizePresentation } from './store';
import { ASPECTS, RESOLUTION_PRESETS, aspectLabel, resolutionForAspect } from './factory';
import { elementText } from './richtext';
import { LIST_ROLES, PRODUCT_ROLES, clearRole, effectiveStyle, isOverridden, setOverride, type OverrideKey, type OverrideRole } from './overrides';
import { ContextMenu, menuPointFor, type MenuState } from './ContextMenu';
import { elementMenu } from './menus';
import { TextFormat, isTextLike } from './TextFormat';

type Upd = (fn: (e: SlideElement) => void) => void;

/** Bold, italic and underline of a text style (product typography). No strikethrough: a
 *  struck-through product means sold out. */
const STYLE_FLAGS = [
  { key: 'bold', icon: Bold, label: 'editor.bold' },
  { key: 'italic', icon: Italic, label: 'editor.italic' },
  { key: 'underline', icon: Underline, label: 'editor.underline' },
] as const;
type StyleFlag = (typeof STYLE_FLAGS)[number]['key'];

function StyleToggles({ value, onToggle }: { value: (k: StyleFlag) => boolean; onToggle: (k: StyleFlag) => void }) {
  const t = useT();
  return (
    <>
      {STYLE_FLAGS.map(({ key, icon: Icon, label }) => (
        <button key={key} type="button" className={`tog ${value(key) ? 'active' : ''}`} aria-pressed={value(key)} title={t(label)} aria-label={t(label)} onClick={() => onToggle(key)}>
          <Icon size={14} />
        </button>
      ))}
    </>
  );
}

/** Text style rows; basics (font, size, color, style) only for the presentation typography –
 *  elements have them in TextFormat, which also formats marked characters. */
function TextStyleFields({ s, set, fonts, sample, basics = true }: { s: TextStyle; set: (fn: (s: TextStyle) => void) => void; fonts: FontOption[]; sample: string; basics?: boolean }) {
  const t = useT();
  return (
    <>
      {basics && (
        <>
          <Row label={t('editor.prop.font')}>
            <FontSelect value={s.font} fonts={fonts} sampleText={sample} missingLabel={t('editor.font.missingChars')} onChange={(f) => set((x) => (x.font = f))} />
          </Row>
          <Row label={t('editor.prop.size')}>
            <Num value={s.size} min={1} max={2000} onChange={(v) => set((x) => (x.size = v))} />
          </Row>
          <Row label={t('editor.prop.color')}>
            <ColorInput value={s.color} onChange={(v) => set((x) => (x.color = v ?? '#ffffff'))} />
          </Row>
          <Row label={t('editor.ovr.style')}>
            <StyleToggles value={(k) => !!s[k]} onToggle={(k) => set((x) => (x[k] = !x[k]))} />
          </Row>
        </>
      )}
      <Row label={t('editor.prop.lineHeight')}>
        <Num value={s.lineHeight} min={0.5} max={5} step={0.05} onChange={(v) => set((x) => (x.lineHeight = v))} />
      </Row>
      <Row label={t('editor.prop.letterSpacing')}>
        <Num value={s.letterSpacing} min={-100} max={1000} onChange={(v) => set((x) => (x.letterSpacing = v))} />
      </Row>
      <Row label={t('editor.prop.outline')}>
        <ColorInput value={s.outline?.color} allowNone noneLabel={t('editor.none')} onChange={(v) => set((x) => (x.outline = v ? { color: v, width: x.outline?.width ?? 2 } : undefined))} />
      </Row>
      {s.outline && (
        <Row label={t('editor.prop.outlineWidth')}>
          <Num value={s.outline.width} min={0} max={100} onChange={(v) => set((x) => x.outline && (x.outline.width = v))} />
        </Row>
      )}
      <Row label={t('editor.prop.shadow')}>
        <ColorInput value={s.shadow?.color} allowNone noneLabel={t('editor.none')} onChange={(v) => set((x) => (x.shadow = v ? { color: v, dx: x.shadow?.dx ?? 3, dy: x.shadow?.dy ?? 3 } : undefined))} />
      </Row>
      {s.shadow && (
        <Row label={t('editor.prop.shadowOffset')}>
          <Num value={s.shadow.dx} min={-200} max={200} onChange={(v) => set((x) => x.shadow && (x.shadow.dx = v))} />
          <Num value={s.shadow.dy} min={-200} max={200} onChange={(v) => set((x) => x.shadow && (x.shadow.dy = v))} />
        </Row>
      )}
    </>
  );
}

function ElementProps({ store, e, upd, fonts, typo }: { store: EditorStore; e: SlideElement; upd: Upd; fonts: FontOption[]; typo: Typography }) {
  const t = useT();
  const sample = elementText(e);
  const setStyle = (fn: (s: TextStyle) => void): void =>
    upd((x) => {
      if ('style' in x && x.style) fn(x.style);
    });
  return (
    <div className="props">
      <Row label={t('editor.prop.name')}>
        <input className="input input-sm" value={e.name} placeholder={t(`editor.el.${e.type}`)} onChange={(ev) => upd((x) => (x.name = ev.target.value))} />
      </Row>
      {isTextLike(e) && (
        <>
          <TextFormat store={store} elements={[e]} fonts={fonts} />
          <TextStyleFields s={e.style} set={setStyle} fonts={fonts} sample={sample} basics={false} />
        </>
      )}
      {!isTextLike(e) && <h3>{t(`editor.el.${e.type}`)}</h3>}
      {(e.type === 'clock' || e.type === 'ticker') && <h3>{t(`editor.el.${e.type}`)}</h3>}
      {e.type === 'text' && (
        <>
          <Row label={t('editor.prop.valign')}>
            <select className="input input-sm" value={e.valign} onChange={(ev) => upd((x) => x.type === 'text' && (x.valign = ev.target.value as 'top'))}>
              {['top', 'middle', 'bottom'].map((v) => (
                <option key={v} value={v}>
                  {t(`editor.valign.${v}`)}
                </option>
              ))}
            </select>
          </Row>
          <Row label={t('editor.prop.fill')}>
            <ColorInput value={e.fill} allowNone noneLabel={t('editor.none')} onChange={(v) => upd((x) => x.type === 'text' && (x.fill = v))} />
          </Row>
          <Row label={t('editor.prop.padding')}>
            <Num value={e.padding?.[0] ?? 0} min={0} onChange={(v) => upd((x) => x.type === 'text' && (x.padding = [v, v, v, v]))} />
          </Row>
          <Row label={t('editor.prop.autoShrink')}>
            <input type="checkbox" checked={!!e.autoShrink} onChange={(ev) => upd((x) => x.type === 'text' && (x.autoShrink = ev.target.checked))} />
          </Row>
        </>
      )}
      {(e.type === 'image' || e.type === 'video') && (
        <>
          <Row label={t('editor.prop.file')}>
            <MediaPickerButton kind={e.type} value={e.assetId} onChange={(id) => upd((x) => (x.type === 'image' || x.type === 'video') && (x.assetId = id))} />
          </Row>
          <Row label={t('editor.prop.fit')}>
            <select className="input input-sm" value={e.fit} onChange={(ev) => upd((x) => (x.type === 'image' || x.type === 'video') && (x.fit = ev.target.value as 'contain'))}>
              {['contain', 'cover', 'fill'].map((v) => (
                <option key={v} value={v}>
                  {t(`admin.fit.${v}`)}
                </option>
              ))}
            </select>
          </Row>
        </>
      )}
      {e.type === 'video' && (
        <>
          <Row label={t('editor.prop.loop')}>
            <input type="checkbox" checked={e.loop ?? true} onChange={(ev) => upd((x) => x.type === 'video' && (x.loop = ev.target.checked))} />
          </Row>
          <Row label={t('editor.prop.muted')}>
            <input type="checkbox" checked={e.muted ?? true} onChange={(ev) => upd((x) => x.type === 'video' && (x.muted = ev.target.checked))} />
          </Row>
        </>
      )}
      {e.type === 'audio' && (
        <>
          <Row label={t('editor.prop.file')}>
            <MediaPickerButton kind="audio" value={e.assetId} onChange={(id) => upd((x) => x.type === 'audio' && (x.assetId = id))} />
          </Row>
          <Row label={t('editor.prop.volume')}>
            <Num value={Math.round(e.volume * 100)} min={0} max={100} onChange={(v) => upd((x) => x.type === 'audio' && (x.volume = v / 100))} suffix="%" />
          </Row>
          <Row label={t('editor.prop.loop')}>
            <input type="checkbox" checked={e.loop ?? false} onChange={(ev) => upd((x) => x.type === 'audio' && (x.loop = ev.target.checked))} />
          </Row>
        </>
      )}
      {e.type === 'shape' && (
        <>
          {(e.shape === 'rect' || e.shape === 'ellipse') && (
            <Row label={t('editor.prop.fill')}>
              <ColorInput value={e.fill} allowNone noneLabel={t('editor.none')} onChange={(v) => upd((x) => x.type === 'shape' && (x.fill = v))} />
            </Row>
          )}
          <Row label={t('editor.prop.stroke')}>
            <ColorInput value={e.stroke.color} onChange={(v) => upd((x) => x.type === 'shape' && (x.stroke.color = v ?? '#ffffff'))} />
          </Row>
          <Row label={t('editor.prop.strokeWidth')}>
            <Num value={e.stroke.width} min={0} max={500} onChange={(v) => upd((x) => x.type === 'shape' && (x.stroke.width = v))} />
          </Row>
          <Row label={t('editor.prop.dash')}>
            <select className="input input-sm" value={e.stroke.dash} onChange={(ev) => upd((x) => x.type === 'shape' && (x.stroke.dash = ev.target.value as 'solid'))}>
              {['solid', 'dashed', 'dotted'].map((v) => (
                <option key={v} value={v}>
                  {t(`editor.dash.${v}`)}
                </option>
              ))}
            </select>
          </Row>
          {e.shape === 'rect' && (
            <Row label={t('editor.prop.radius')}>
              <Num value={e.radius ?? 0} min={0} onChange={(v) => upd((x) => x.type === 'shape' && (x.radius = v))} />
            </Row>
          )}
        </>
      )}
      {e.type === 'clock' && (
        <>
          <Row label={t('editor.prop.format')}>
            <select className="input input-sm" value={e.format} onChange={(ev) => upd((x) => x.type === 'clock' && (x.format = ev.target.value as 'HH:mm'))}>
              {['HH:mm', 'HH:mm:ss', 'h:mm a', 'h:mm:ss a'].map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          </Row>
          <Row label={t('editor.prop.date')}>
            <select className="input input-sm" value={e.date} onChange={(ev) => upd((x) => x.type === 'clock' && (x.date = ev.target.value as 'none'))}>
              {['none', 'short', 'long'].map((v) => (
                <option key={v} value={v}>
                  {t(`editor.date.${v}`)}
                </option>
              ))}
            </select>
          </Row>
        </>
      )}
      {e.type === 'ticker' && (
        <>
          <Row label={t('editor.prop.tickerSource')}>
            <select className="input input-sm" value={e.tickerSource} onChange={(ev) => upd((x) => x.type === 'ticker' && (x.tickerSource = ev.target.value as 'text'))}>
              <option value="zone">{t('editor.ticker.zone')}</option>
              <option value="text">{t('editor.ticker.text')}</option>
            </select>
          </Row>
          {e.tickerSource === 'text' && (
            <Row label={t('editor.prop.text')}>
              <input className="input input-sm" value={e.text ?? ''} onChange={(ev) => upd((x) => x.type === 'ticker' && (x.text = ev.target.value))} />
            </Row>
          )}
          <Row label={t('editor.prop.fill')}>
            <ColorInput value={e.fill} allowNone noneLabel={t('editor.none')} onChange={(v) => upd((x) => x.type === 'ticker' && (x.fill = v))} />
          </Row>
          <Row label={t('editor.prop.speed')}>
            <Num value={e.speed} min={10} max={2000} onChange={(v) => upd((x) => x.type === 'ticker' && (x.speed = v))} />
          </Row>
          <Row label={t('editor.prop.direction')}>
            <select className="input input-sm" value={e.direction} onChange={(ev) => upd((x) => x.type === 'ticker' && (x.direction = ev.target.value as 'left'))}>
              <option value="left">{t('editor.direction.left')}</option>
              <option value="right">{t('editor.direction.right')}</option>
            </select>
          </Row>
        </>
      )}
      {(e.type === 'productList' || e.type === 'product') && (
        <>
          <ProductProps e={e} upd={upd} />
          <ProductTypography e={e} upd={upd} typo={typo} fonts={fonts} />
        </>
      )}
      <h3>{t('editor.panel.layout')}</h3>
      <Row label={t('editor.prop.position')}>
        <Num value={e.x} onChange={(v) => upd((x) => (x.x = v))} suffix="x" />
        <Num value={e.y} onChange={(v) => upd((x) => (x.y = v))} suffix="y" />
      </Row>
      <Row label={t('editor.prop.size')}>
        <Num value={e.w} min={1} onChange={(v) => upd((x) => (x.w = v))} suffix="w" />
        <Num value={e.h} min={1} onChange={(v) => upd((x) => (x.h = v))} suffix="h" />
      </Row>
      <Row label={t('editor.prop.rotation')}>
        <Num value={e.rotation} min={-360} max={360} onChange={(v) => upd((x) => (x.rotation = v))} suffix="°" />
      </Row>
      <Row label={t('editor.prop.opacity')}>
        <Num value={Math.round(e.opacity * 100)} min={0} max={100} onChange={(v) => upd((x) => (x.opacity = v / 100))} suffix="%" />
      </Row>
      <Row label={t('editor.prop.pinned')}>
        <input type="checkbox" checked={e.pinned} onChange={(ev) => upd((x) => (x.pinned = ev.target.checked))} title={t('editor.prop.pinnedHint')} />
      </Row>
    </div>
  );
}

/** A property row whose value can be reset to the inherited (presentation) value. */
function OverrideRow({ label, overridden, resetLabel, onReset, children }: { label: string; overridden: boolean; resetLabel: string; onReset: () => void; children: ReactNode }) {
  return (
    <div className={`prop-row ${overridden ? 'overridden' : ''}`}>
      <span>{label}</span>
      <div className="prop-ctrl">
        {children}
        {overridden && (
          <button type="button" className="icon-btn ovr-reset" title={resetLabel} aria-label={`${resetLabel}: ${label}`} onClick={onReset}>
            <RotateCcw size={13} />
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * Typography of one product list / product: per text role font, size, color, bold, italic
 * and underline on top of the presentation typography. Unchanged values are inherited.
 */
function ProductTypography({ e, upd, typo, fonts }: { e: Extract<SlideElement, { type: 'productList' | 'product' }>; upd: Upd; typo: Typography; fonts: FontOption[] }) {
  const t = useT();
  const roles = e.type === 'productList' ? LIST_ROLES : PRODUCT_ROLES;
  const write = (fn: (o: ProductOverrides | undefined) => ProductOverrides | undefined): void =>
    upd((x) => {
      if (x.type !== 'productList' && x.type !== 'product') return;
      const o = fn(x.overrides);
      if (o) x.overrides = o;
      else delete x.overrides;
    });
  const set = <K extends OverrideKey>(role: OverrideRole, key: K, value: Marks[K] | undefined): void => write((o) => setOverride(o, role, key, value));
  const resetLabel = t('editor.ovr.reset');
  return (
    <>
      <h3>{t('editor.ovr.title')}</h3>
      <div className="faint ovr-hint">{t('editor.ovr.hint')}</div>
      {roles.map((role) => {
        const st = effectiveStyle(typo, e.overrides, role);
        const ov = (k: OverrideKey): boolean => isOverridden(e.overrides, role, k);
        const changed = isOverridden(e.overrides, role);
        return (
          <details key={role} className="typo">
            <summary>
              {t(`editor.typo.${role}`)}
              {changed && <span className="ovr-dot" title={t('editor.ovr.changed')} />}
            </summary>
            <div className="props typo-body">
              <OverrideRow label={t('editor.prop.font')} overridden={ov('font')} resetLabel={resetLabel} onReset={() => set(role, 'font', undefined)}>
                <FontSelect value={st.font} fonts={fonts} missingLabel={t('editor.font.missingChars')} onChange={(f) => set(role, 'font', f)} />
              </OverrideRow>
              <OverrideRow label={t('editor.prop.size')} overridden={ov('size')} resetLabel={resetLabel} onReset={() => set(role, 'size', undefined)}>
                <Num value={st.size} min={1} max={2000} onChange={(v) => set(role, 'size', v)} />
              </OverrideRow>
              <OverrideRow label={t('editor.prop.color')} overridden={ov('color')} resetLabel={resetLabel} onReset={() => set(role, 'color', undefined)}>
                <ColorInput value={st.color} onChange={(v) => set(role, 'color', v ?? typo[role].color)} />
              </OverrideRow>
              <OverrideRow
                label={t('editor.ovr.style')}
                overridden={STYLE_FLAGS.some((f) => ov(f.key))}
                resetLabel={resetLabel}
                onReset={() => write((o) => STYLE_FLAGS.reduce((acc, f) => setOverride(acc, role, f.key, undefined), o))}
              >
                <StyleToggles value={(k) => st[k]} onToggle={(k) => set(role, k, !st[k])} />
              </OverrideRow>
              {changed && (
                <button type="button" className="btn btn-secondary btn-sm" onClick={() => write((o) => clearRole(o, role))}>
                  {t('editor.ovr.resetRole')}
                </button>
              )}
            </div>
          </details>
        );
      })}
      <button type="button" className="btn btn-secondary btn-sm" disabled={!e.overrides} onClick={() => write(() => undefined)}>
        {t('editor.ovr.resetAll')}
      </button>
    </>
  );
}

/** Category or subcategory headings: shown, hidden but keeping their space, or hidden. */
function HeadingMode({ label, show, space, onChange }: { label: string; show: boolean; space: boolean; onChange: (show: boolean, space: boolean) => void }) {
  const t = useT();
  const mode = show ? 'show' : space ? 'space' : 'hide';
  return (
    <Row label={label}>
      <select className="input input-sm" value={mode} onChange={(ev) => onChange(ev.target.value === 'show', ev.target.value === 'space')}>
        {(['show', 'space', 'hide'] as const).map((m) => (
          <option key={m} value={m}>
            {t(`editor.headingMode.${m}`)}
          </option>
        ))}
      </select>
    </Row>
  );
}

function ProductProps({ e, upd }: { e: Extract<SlideElement, { type: 'productList' | 'product' }>; upd: Upd }) {
  const t = useT();
  return (
    <>
      {e.type === 'productList' && (
        <>
          <Row label={t('editor.prop.source')}>
            <select className="input input-sm" value={e.source.kind} onChange={(ev) => upd((x) => x.type === 'productList' && (x.source = { kind: ev.target.value as 'all', ids: [] }))}>
              {['all', 'categories', 'products'].map((v) => (
                <option key={v} value={v}>
                  {t(`editor.source.${v}`)}
                </option>
              ))}
            </select>
          </Row>
          {e.source.kind !== 'all' && <SourcePicker kind={e.source.kind} ids={e.source.ids} onChange={(ids) => upd((x) => x.type === 'productList' && (x.source.ids = ids))} />}
          <Row label={t('editor.prop.columns')}>
            <Num integer value={e.columns} min={1} max={4} onChange={(v) => upd((x) => x.type === 'productList' && (x.columns = v))} />
          </Row>
          <HeadingMode label={t('editor.typo.heading')} show={!!e.categoryHeadings} space={!!e.categorySpace} onChange={(show, space) => upd((x) => x.type === 'productList' && ((x.categoryHeadings = show), (x.categorySpace = space)))} />
          <HeadingMode label={t('editor.typo.subheading')} show={!!e.subcategoryHeadings} space={!!e.subcategorySpace} onChange={(show, space) => upd((x) => x.type === 'productList' && ((x.subcategoryHeadings = show), (x.subcategorySpace = space)))} />
          <Row label={t('editor.prop.gaps')}>
            <Num value={e.rowGap ?? 0} min={0} onChange={(v) => upd((x) => x.type === 'productList' && (x.rowGap = v))} />
            <Num value={e.columnGap ?? 0} min={0} onChange={(v) => upd((x) => x.type === 'productList' && (x.columnGap = v))} />
          </Row>
        </>
      )}
      {e.type === 'product' && <SourcePicker kind="products" single ids={e.productId ? [e.productId] : []} onChange={(ids) => upd((x) => x.type === 'product' && (x.productId = ids[0] ?? ''))} />}
      <Row label={t('editor.prop.fields')}>
        {(['name', 'quantity', 'description', 'variants', 'price'] as const).map((f) => (
          <label key={f} className="checkbox small">
            {/* Variants are shown unless switched off (older elements have no setting). */}
            <input type="checkbox" checked={e.fields[f] !== false && (f === 'variants' || !!e.fields[f])} onChange={(ev) => upd((x) => (x.type === 'productList' || x.type === 'product') && (x.fields[f] = ev.target.checked))} />
            <span>{t(`editor.field.${f}`)}</span>
          </label>
        ))}
      </Row>
      <Row label={t('editor.prop.scale')}>
        <Num value={Math.round(e.scale * 100)} min={25} max={400} onChange={(v) => upd((x) => (x.type === 'productList' || x.type === 'product') && (x.scale = v / 100))} suffix="%" />
      </Row>
      <Row label={t('editor.prop.priceAlign')}>
        <select className="input input-sm" value={e.priceAlign} onChange={(ev) => upd((x) => (x.type === 'productList' || x.type === 'product') && (x.priceAlign = ev.target.value as 'left'))}>
          <option value="right">{t('editor.direction.right')}</option>
          <option value="left">{t('editor.direction.left')}</option>
        </select>
      </Row>
    </>
  );
}


function backgroundOf(type: Background['type'], prev: Background): Background {
  switch (type) {
    case 'color':
      return { type, color: prev.color };
    case 'gradient':
      return { type, color: prev.color, angle: 180, stops: [{ color: '#201c18', at: 0 }, { color: '#a63d00', at: 1 }] };
    case 'image':
      return { type, color: prev.color, assetId: '', fit: 'cover' };
    case 'video':
      return { type, color: prev.color, assetId: '', fit: 'cover' };
  }
}

/** Background settings of a slide (side panel and the "Slide background…" dialog). */
function BackgroundFields({ store, slide }: { store: EditorStore; slide: Slide }) {
  const t = useT();
  const up = (fn: (s: Slide) => void): void => store.updateSlide(slide.id, fn);
  const b = slide.background;
  return (
    <>
      <Row label={t('editor.slide.background')}>
        <select className="input input-sm" value={b.type} onChange={(ev) => up((s) => {
                s.background = backgroundOf(ev.target.value as Background['type'], s.background);
                // "Until the video ends" needs a video background.
                if (s.background.type !== 'video' && s.duration.mode === 'video') s.duration = { mode: 'fixed', ms: 10000 };
              })}>
          {['color', 'gradient', 'image', 'video'].map((v) => (
            <option key={v} value={v}>
              {t(`editor.bg.${v}`)}
            </option>
          ))}
        </select>
      </Row>
      <Row label={t('editor.prop.color')}>
        <ColorInput value={b.color} onChange={(v) => up((s) => (s.background.color = v ?? '#000000'))} />
      </Row>
      {b.type === 'gradient' && (
        <>
          <Row label={t('editor.bg.angle')}>
            <Num value={b.angle} min={-360} max={360} onChange={(v) => up((s) => s.background.type === 'gradient' && (s.background.angle = v))} suffix="°" />
          </Row>
          {b.stops.map((st, i) => (
            <Row key={i} label={`${t('editor.bg.stop')} ${i + 1}`}>
              <ColorInput value={st.color} onChange={(v) => up((s) => s.background.type === 'gradient' && (s.background.stops[i]!.color = v ?? '#000000'))} />
            </Row>
          ))}
        </>
      )}
      {(b.type === 'image' || b.type === 'video') && (
        <>
          <Row label={t('editor.prop.file')}>
            <MediaPickerButton
              kind={b.type}
              value={b.assetId}
              onChange={(id) =>
                up((s) => {
                  if (s.background.type === 'image' || s.background.type === 'video') s.background.assetId = id;
                })
              }
            />
          </Row>
          <Row label={t('editor.prop.fit')}>
            <select
              className="input input-sm"
              value={b.fit}
              onChange={(ev) =>
                up((s) => {
                  if (s.background.type === 'image' || s.background.type === 'video') s.background.fit = ev.target.value as 'cover';
                })
              }
            >
              {['contain', 'cover', 'fill'].map((v) => (
                <option key={v} value={v}>
                  {t(`admin.fit.${v}`)}
                </option>
              ))}
            </select>
          </Row>
        </>
      )}
      <button
        type="button"
        className="btn btn-secondary btn-sm"
        onClick={() =>
          store.apply((d) => {
            for (const s of d.slides) s.background = structuredClone(b);
          })
        }
      >
        {t('editor.bg.applyAll')}
      </button>
    </>
  );
}

/** "Slide background…" from the context menus. */
export function SlideBackgroundDialog({ store, slideId, onClose }: { store: EditorStore; slideId: string; onClose: () => void }) {
  const t = useT();
  const snap = useEditor(store);
  const slide = snap.doc.slides.find((s) => s.id === slideId);
  if (!slide) return null;
  return (
    <Dialog
      title={t('editor.slide.background')}
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('common.action.close')}
        </Button>
      }
    >
      <div className="props bg-dialog">
        <BackgroundFields store={store} slide={slide} />
      </div>
    </Dialog>
  );
}

function SlideProps({ store, slide, index }: { store: EditorStore; slide: Slide; index: number }) {
  const t = useT();
  const up = (fn: (s: Slide) => void): void => store.updateSlide(slide.id, fn);
  const b = slide.background;
  return (
    <div className="props">
      <h3>{t('editor.slide.number', { n: index + 1 })}</h3>
      <Row label={t('editor.prop.name')}>
        <input className="input input-sm" value={slide.name} onChange={(ev) => up((s) => (s.name = ev.target.value))} />
      </Row>
      <Row label={t('editor.slide.duration')}>
        <select
          className="input input-sm"
          value={slide.duration.mode}
          onChange={(ev) => up((s) => (s.duration = ev.target.value === 'video' ? { mode: 'video' } : { mode: 'fixed', ms: 10000 }))}
          disabled={b.type !== 'video' && slide.duration.mode !== 'video'}
        >
          <option value="fixed">{t('editor.slide.fixed')}</option>
          <option value="video">{t('editor.slide.untilVideoEnds')}</option>
        </select>
        {slide.duration.mode === 'fixed' && <Num value={slide.duration.ms / 1000} min={1} max={86400} onChange={(v) => up((s) => (s.duration = { mode: 'fixed', ms: Math.round(v * 1000) }))} suffix="s" />}
      </Row>
      <BackgroundFields store={store} slide={slide} />
      <Row label={t('editor.slide.transition')}>
        <select className="input input-sm" value={slide.transition.type} onChange={(ev) => up((s) => (s.transition = { ...s.transition, type: ev.target.value as 'fade', direction: ev.target.value === 'slide' ? s.transition.direction ?? 'left' : undefined }))}>
          {['cut', 'fade', 'slide', 'zoom'].map((v) => (
            <option key={v} value={v}>
              {t(`editor.transition.${v}`)}
            </option>
          ))}
        </select>
      </Row>
      {slide.transition.type !== 'cut' && (
        <Row label={t('editor.slide.transitionMs')}>
          <Num integer value={slide.transition.ms} min={100} max={5000} step={50} onChange={(v) => up((s) => (s.transition.ms = v))} suffix="ms" />
        </Row>
      )}
      {slide.transition.type === 'slide' && (
        <Row label={t('editor.prop.direction')}>
          <select className="input input-sm" value={slide.transition.direction ?? 'left'} onChange={(ev) => up((s) => (s.transition.direction = ev.target.value as 'left'))}>
            {['left', 'right', 'up', 'down'].map((v) => (
              <option key={v} value={v}>
                {t(`editor.direction.${v}`)}
              </option>
            ))}
          </select>
        </Row>
      )}
    </div>
  );
}

function PresentationProps({ store, fonts }: { store: EditorStore; fonts: FontOption[] }) {
  const t = useT();
  const { doc } = useEditor(store);
  const [w, setW] = useState(doc.width);
  const [h, setH] = useState(doc.height);
  const apply = (nw: number, nh: number): void => {
    if (nw === doc.width && nh === doc.height) return;
    store.apply((d) => resizePresentation(d, nw, nh));
  };
  const setTypo = (k: keyof Typography) => (fn: (s: TextStyle) => void) => store.apply((d) => fn(d.typography[k]));
  return (
    <div className="props">
      <Row label={t('editor.prop.name')}>
        <input className="input input-sm" value={doc.name} maxLength={200} onChange={(ev) => {
          const v = ev.target.value;
          store.apply((d) => (d.name = v));
        }} />
      </Row>
      <h3>{t('editor.pres.format')}</h3>
      <Row label={t('editor.pres.preset')}>
        <select
          className="input input-sm"
          value=""
          onChange={(ev) => {
            const p = RESOLUTION_PRESETS[Number(ev.target.value)];
            if (p) {
              setW(p.w);
              setH(p.h);
              apply(p.w, p.h);
            }
          }}
        >
          <option value="">{`${doc.width}×${doc.height} (${aspectLabel(doc.width, doc.height)})`}</option>
          {RESOLUTION_PRESETS.map((p, i) => (
            <option key={p.label} value={i}>
              {`${p.label} (${p.w}×${p.h})`}
            </option>
          ))}
        </select>
      </Row>
      <Row label={t('editor.pres.aspect')}>
        <select
          className="input input-sm"
          value=""
          onChange={(ev) => {
            const a = ASPECTS[Number(ev.target.value)];
            if (a) {
              const r = resolutionForAspect(a.w, a.h);
              setW(r.w);
              setH(r.h);
              apply(r.w, r.h);
            }
          }}
        >
          <option value="">{aspectLabel(doc.width, doc.height)}</option>
          {ASPECTS.map((a, i) => (
            <option key={a.label} value={i}>
              {a.label}
            </option>
          ))}
        </select>
      </Row>
      <Row label={t('editor.pres.custom')}>
        <Num integer value={w} min={16} max={7680} onChange={setW} suffix="w" />
        <Num integer value={h} min={16} max={7680} onChange={setH} suffix="h" />
        <button type="button" className="btn btn-secondary btn-sm" onClick={() => apply(w, h)}>
          {t('editor.apply')}
        </button>
      </Row>
      <h3>{t('editor.pres.typography')}</h3>
      {(['heading', 'subheading', 'item', 'description', 'price'] as const).map((k) => (
        <details key={k} className="typo">
          <summary>{t(`editor.typo.${k}`)}</summary>
          <TextStyleFields s={doc.typography[k]} set={setTypo(k)} fonts={fonts} sample="" />
        </details>
      ))}
    </div>
  );
}

function Layers({ store }: { store: EditorStore }) {
  const t = useT();
  const snap = useEditor(store);
  const slide = snap.doc.slides.find((s) => s.id === snap.slideId);
  const [dragId, setDragId] = useState<string | null>(null);
  const [overIdx, setOverIdx] = useState<number | null>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);
  if (!slide) return null;
  const items = [...slide.elements].reverse(); // top first
  const openMenu = (ev: React.MouseEvent, id: string): void => {
    ev.preventDefault();
    if (!store.getSnapshot().selection.includes(id)) store.select([id]);
    const native = ev.nativeEvent as MouseEvent & { pointerType?: string };
    const p = native.pointerType === '' || (ev.clientX === 0 && ev.clientY === 0) ? menuPointFor(ev.currentTarget) : { x: ev.clientX, y: ev.clientY };
    setMenu({ ...p, items: elementMenu(store, t) });
  };
  return (
    <div className="layers" onDragEnd={() => (setDragId(null), setOverIdx(null))}>
      {items.map((e, i) => {
        const idx = slide.elements.length - 1 - i;
        return (
          <div
            key={e.id}
            className={`layer ${snap.selection.includes(e.id) ? 'active' : ''} ${overIdx === idx ? 'drop' : ''}`}
            draggable
            onDragStart={() => setDragId(e.id)}
            onDragOver={(ev) => {
              ev.preventDefault();
              setOverIdx(idx);
            }}
            onDrop={(ev) => {
              ev.preventDefault();
              if (dragId) store.moveElement(dragId, idx);
              setDragId(null);
              setOverIdx(null);
            }}
            onClick={(ev) => store.select([e.id], ev.shiftKey)}
            onContextMenu={(ev) => openMenu(ev, e.id)}
            tabIndex={0}
            onKeyDown={(ev) => {
              if (ev.target !== ev.currentTarget || (ev.key !== 'Enter' && ev.key !== ' ')) return;
              ev.preventDefault();
              store.select([e.id], ev.shiftKey);
            }}
          >
            <GripVertical size={14} className="grip" />
            <span className="layer-name">{e.name || t(`editor.el.${e.type}`)}</span>
            {e.pinned && <Pin size={13} className="muted" />}
            <button type="button" className="icon-btn" title={t('editor.layer.visible')} onClick={(ev) => (ev.stopPropagation(), store.updateElements([e.id], (x) => (x.hidden = !x.hidden)))}>
              {e.hidden ? <EyeOff size={14} /> : <Eye size={14} />}
            </button>
            <button type="button" className="icon-btn" title={t('editor.layer.lock')} onClick={(ev) => (ev.stopPropagation(), store.updateElements([e.id], (x) => (x.locked = !x.locked)))}>
              {e.locked ? <Lock size={14} /> : <Unlock size={14} />}
            </button>
          </div>
        );
      })}
      {items.length === 0 && <div className="faint pad">{t('editor.layer.empty')}</div>}
      {menu && <ContextMenu menu={menu} onClose={() => setMenu(null)} label={t('editor.menu.label')} />}
    </div>
  );
}

type PanelTab = 'presentation' | 'slide' | 'element' | 'layers';

export function Panel({ store, fonts }: { store: EditorStore; fonts: FontOption[] }) {
  const t = useT();
  const snap = useEditor(store);
  // The tab follows the selection (presentation → slide → element); the layer list stays
  // open until another tab is chosen.
  const [layers, setLayers] = useState(false);
  const slideIndex = snap.doc.slides.findIndex((s) => s.id === snap.slideId);
  const slide = snap.doc.slides[slideIndex];
  const selected = slide?.elements.filter((e) => snap.selection.includes(e.id)) ?? [];
  const single = selected.length === 1 ? selected[0]! : null;
  const tab: PanelTab = layers ? 'layers' : selected.length ? 'element' : snap.focus;
  const upd: Upd = (fn) => store.updateElements(snap.selection, fn);
  const tabs: { value: PanelTab; label: string; icon: ReactNode; disabled?: boolean; select: () => void }[] = [
    { value: 'presentation', label: t('editor.pres.title'), icon: <PresentationIcon size={16} />, select: () => (setLayers(false), store.focusPresentation()) },
    { value: 'slide', label: t('editor.slide.title'), icon: <RectangleHorizontal size={16} />, select: () => (setLayers(false), store.focusSlide()) },
    { value: 'element', label: t('editor.panel.element'), icon: <SquareMousePointer size={16} />, disabled: !selected.length, select: () => setLayers(false) },
    { value: 'layers', label: t('editor.panel.layers'), icon: <LayersIcon size={16} />, select: () => setLayers(true) },
  ];
  return (
    <aside className="editor-panel">
      <div className="panel-tabs" role="tablist">
        {tabs.map((x) => (
          <button
            key={x.value}
            type="button"
            role="tab"
            className={`ptab ${tab === x.value ? 'active' : ''}`}
            aria-selected={tab === x.value}
            title={x.label}
            disabled={x.disabled}
            onClick={x.select}
          >
            {x.icon}
            <span>{x.label}</span>
          </button>
        ))}
      </div>
      <div className="panel-body" role="tabpanel">
        {tab === 'presentation' && <PresentationProps store={store} fonts={fonts} />}
        {tab === 'slide' && slide && <SlideProps store={store} slide={slide} index={slideIndex} />}
        {tab === 'element' && single && <ElementProps store={store} e={single} upd={upd} fonts={fonts} typo={snap.doc.typography} />}
        {tab === 'element' && selected.length > 1 && (
          <div className="props">
            <div className="faint">{t('editor.multi', { count: selected.length })}</div>
            <TextFormat store={store} elements={selected} fonts={fonts} />
          </div>
        )}
        {tab === 'layers' && <Layers store={store} />}
      </div>
    </aside>
  );
}

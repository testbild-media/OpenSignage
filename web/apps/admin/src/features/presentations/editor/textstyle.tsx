// Text style controls shared by the editor panel and Settings → Defaults (typography of new
// presentations). Outside the editor they need a container with the class "ed-controls".
import { Bold, Italic, Underline } from 'lucide-react';
import type { TextStyle } from '@opensignage/contracts';
import { useT } from '../../../lib/hooks';
import { ColorInput, FontSelect, Num, Row } from './controls';
import type { FontOption } from './env';
import './editor.css';

/** Bold, italic and underline of a text style (product typography). No strikethrough: a
 *  struck-through product means sold out. */
export const STYLE_FLAGS = [
  { key: 'bold', icon: Bold, label: 'editor.bold' },
  { key: 'italic', icon: Italic, label: 'editor.italic' },
  { key: 'underline', icon: Underline, label: 'editor.underline' },
] as const;
type StyleFlag = (typeof STYLE_FLAGS)[number]['key'];

export function StyleToggles({
  value,
  onToggle,
}: {
  value: (k: StyleFlag) => boolean;
  onToggle: (k: StyleFlag) => void;
}) {
  const t = useT();
  return (
    <>
      {STYLE_FLAGS.map(({ key, icon: Icon, label }) => (
        <button
          key={key}
          type="button"
          className={`tog ${value(key) ? 'active' : ''}`}
          aria-pressed={value(key)}
          title={t(label)}
          aria-label={t(label)}
          onClick={() => onToggle(key)}
        >
          <Icon size={14} />
        </button>
      ))}
    </>
  );
}

/** Text style rows; basics (font, size, color, style) only for the presentation typography –
 *  elements have them in TextFormat, which also formats marked characters. */
export function TextStyleFields({
  s,
  set,
  fonts,
  sample,
  basics = true,
}: {
  s: TextStyle;
  set: (fn: (s: TextStyle) => void) => void;
  fonts: FontOption[];
  sample: string;
  basics?: boolean;
}) {
  const t = useT();
  return (
    <>
      {basics && (
        <>
          <Row label={t('editor.prop.font')}>
            <FontSelect
              value={s.font}
              fonts={fonts}
              sampleText={sample}
              missingLabel={t('editor.font.missingChars')}
              onChange={(f) => set((x) => (x.font = f))}
            />
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
        <Num
          value={s.lineHeight}
          min={0.5}
          max={5}
          step={0.05}
          onChange={(v) => set((x) => (x.lineHeight = v))}
        />
      </Row>
      <Row label={t('editor.prop.letterSpacing')}>
        <Num
          value={s.letterSpacing}
          min={-100}
          max={1000}
          onChange={(v) => set((x) => (x.letterSpacing = v))}
        />
      </Row>
      <Row label={t('editor.prop.outline')}>
        <ColorInput
          value={s.outline?.color}
          allowNone
          noneLabel={t('editor.none')}
          onChange={(v) =>
            set((x) => (x.outline = v ? { color: v, width: x.outline?.width ?? 2 } : undefined))
          }
        />
      </Row>
      {s.outline && (
        <Row label={t('editor.prop.outlineWidth')}>
          <Num
            value={s.outline.width}
            min={0}
            max={100}
            onChange={(v) => set((x) => x.outline && (x.outline.width = v))}
          />
        </Row>
      )}
      <Row label={t('editor.prop.shadow')}>
        <ColorInput
          value={s.shadow?.color}
          allowNone
          noneLabel={t('editor.none')}
          onChange={(v) =>
            set(
              (x) => (x.shadow = v ? { color: v, dx: x.shadow?.dx ?? 3, dy: x.shadow?.dy ?? 3 } : undefined),
            )
          }
        />
      </Row>
      {s.shadow && (
        <Row label={t('editor.prop.shadowOffset')}>
          <Num
            value={s.shadow.dx}
            min={-200}
            max={200}
            onChange={(v) => set((x) => x.shadow && (x.shadow.dx = v))}
          />
          <Num
            value={s.shadow.dy}
            min={-200}
            max={200}
            onChange={(v) => set((x) => x.shadow && (x.shadow.dy = v))}
          />
        </Row>
      )}
    </>
  );
}

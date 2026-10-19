// Settings → Defaults: start values of new presentations, their new slides and new texts.
// The controls are those of the editor (font list, color, typography).
import type { TextStyle, TransitionType, Typography } from '@opensignage/contracts';
import { useT } from '../../lib/hooks';
import { Field, Select, TextInput } from '../../components/ui';
import { ColorInput, FontSelect } from '../presentations/editor/controls';
import { useFonts } from '../presentations/editor/env';
import { RESOLUTION_PRESETS, type PresentationDefaults } from '../presentations/editor/factory';
import { TextStyleFields } from '../presentations/editor/textstyle';

const ROLES = ['heading', 'subheading', 'item', 'description', 'price'] as const;
const TRANSITIONS: TransitionType[] = ['cut', 'fade', 'slide', 'zoom'];
const DIRECTIONS = ['left', 'right', 'up', 'down'] as const;

export function PresentationDefaultsCard({
  value,
  slideMs,
  onChange,
  onSlideMs,
}: {
  value: PresentationDefaults;
  slideMs: number;
  onChange: (v: PresentationDefaults) => void;
  onSlideMs: (ms: number) => void;
}) {
  const t = useT();
  const fonts = useFonts();
  const set = (patch: Partial<PresentationDefaults>): void => onChange({ ...value, ...patch });
  const setTypo =
    (k: keyof Typography) =>
    (fn: (s: TextStyle) => void): void => {
      const typography = structuredClone(value.typography);
      fn(typography[k]);
      set({ typography });
    };
  const tr = value.transition;
  const size = `${value.width}x${value.height}`;
  const preset = RESOLUTION_PRESETS.some((p) => `${p.w}x${p.h}` === size);
  return (
    <div className="card form ed-controls">
      <h2>{t('admin.defaults.presentations')}</h2>
      <p className="muted">{t('admin.defaults.presentationsHint')}</p>
      <Select
        label={t('editor.pres.format')}
        value={preset ? size : ''}
        onChange={(e) => {
          const p = RESOLUTION_PRESETS.find((x) => `${x.w}x${x.h}` === e.target.value);
          if (p) set({ width: p.w, height: p.h });
        }}
        options={[
          ...(preset
            ? []
            : [{ value: '', label: `${t('editor.pres.custom')} (${value.width}×${value.height})` }]),
          ...RESOLUTION_PRESETS.map((p) => ({ value: `${p.w}x${p.h}`, label: `${p.label} (${p.w}×${p.h})` })),
        ]}
      />
      <div className="form-row">
        <TextInput
          label={t('fields.width')}
          type="number"
          min={16}
          max={7680}
          value={value.width}
          onChange={(e) => set({ width: Number(e.target.value) })}
        />
        <TextInput
          label={t('fields.height')}
          type="number"
          min={16}
          max={7680}
          value={value.height}
          onChange={(e) => set({ height: Number(e.target.value) })}
        />
      </div>
      <TextInput
        label={t('admin.defaults.slideSec')}
        type="number"
        min={1}
        value={Math.round(slideMs / 1000)}
        onChange={(e) => onSlideMs(Number(e.target.value) * 1000)}
      />
      <Field label={t('admin.defaults.slideBackground')}>
        <ColorInput value={value.background} onChange={(v) => set({ background: v ?? '#000000' })} />
      </Field>
      <div className="form-row">
        <Select
          label={t('editor.slide.transition')}
          value={tr.type}
          onChange={(e) => {
            const type = e.target.value as TransitionType;
            set({
              transition: {
                type,
                ms: type !== 'cut' && tr.ms < 100 ? 600 : tr.ms,
                direction: type === 'slide' ? (tr.direction ?? 'left') : undefined,
              },
            });
          }}
          options={TRANSITIONS.map((v) => ({ value: v, label: t(`editor.transition.${v}`) }))}
        />
        {tr.type !== 'cut' && (
          <TextInput
            label={`${t('editor.slide.transitionMs')} (ms)`}
            type="number"
            min={100}
            max={5000}
            step={50}
            value={tr.ms}
            onChange={(e) => set({ transition: { ...tr, ms: Number(e.target.value) } })}
          />
        )}
      </div>
      {tr.type === 'slide' && (
        <Select
          label={t('editor.prop.direction')}
          value={tr.direction ?? 'left'}
          onChange={(e) =>
            set({ transition: { ...tr, direction: e.target.value as (typeof DIRECTIONS)[number] } })
          }
          options={DIRECTIONS.map((v) => ({ value: v, label: t(`editor.direction.${v}`) }))}
        />
      )}
      <div className="form-row">
        <Field label={t('admin.defaults.textFont')}>
          <FontSelect
            value={value.text.font}
            fonts={fonts}
            missingLabel={t('editor.font.missingChars')}
            onChange={(font) => set({ text: { ...value.text, font } })}
          />
        </Field>
        <Field label={t('admin.defaults.textColor')}>
          <ColorInput
            value={value.text.color}
            onChange={(color) => set({ text: { ...value.text, color: color ?? '#ffffff' } })}
          />
        </Field>
      </div>
      <h3>{t('editor.pres.typography')}</h3>
      <p className="muted">{t('admin.defaults.typographyHint')}</p>
      <div>
        {ROLES.map((k) => (
          <details key={k} className="typo">
            <summary>{t(`editor.typo.${k}`)}</summary>
            <div className="props typo-body">
              <TextStyleFields s={value.typography[k]} set={setTypo(k)} fonts={fonts} sample="" />
            </div>
          </details>
        ))}
      </div>
    </div>
  );
}

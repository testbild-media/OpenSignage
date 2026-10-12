// Display mode picker: the modes the screen reports, "automatic"
// (the screen's preferred mode) and a clearly marked custom mode that forces a resolution
// the screen does not report.
import type { DisplayConfig, DisplayModeGroup } from '@opensignage/contracts';
import { useT } from '../../lib/hooks';
import { Select, TextInput } from '../../components/ui';
import { AUTO, CUSTOM, customModeValid, displayFor, modeLabel, modeOptions, pickerValue } from './model';

export function DisplayPicker({
  value,
  modes,
  onChange,
}: {
  value: DisplayConfig;
  modes: DisplayModeGroup[] | undefined;
  onChange: (d: DisplayConfig) => void;
}) {
  const t = useT();
  const options = modeOptions(modes);
  const selected = pickerValue(value, options);
  const preferred = options.find((o) => o.preferred);
  const tags = (o: (typeof options)[number]): string => {
    const list = [o.preferred && t('devices.mode.preferred'), o.current && t('devices.mode.current')].filter(
      Boolean,
    );
    return list.length ? ` (${list.join(', ')})` : '';
  };
  const custom = selected === CUSTOM;
  const num = (v: string): number => {
    const n = Number(v.replace(',', '.'));
    return Number.isFinite(n) ? n : 0;
  };
  return (
    <>
      <Select
        label={t('devices.display')}
        hint={options.length === 0 ? t('devices.mode.none') : t('devices.mode.hint')}
        value={selected}
        onChange={(e) => onChange(displayFor(e.target.value, options, value))}
        options={[
          {
            value: AUTO,
            label: preferred
              ? t('devices.mode.autoPreferred', {
                  mode: modeLabel(preferred.width, preferred.height, preferred.refresh),
                })
              : t('devices.displayAuto'),
          },
          ...options.map((o) => ({
            value: o.value,
            label: modeLabel(o.width, o.height, o.refresh) + tags(o),
          })),
          { value: CUSTOM, label: t('devices.mode.custom') },
        ]}
      />
      {custom && (
        <div className="field">
          <div className="mode-custom">
            <TextInput
              label={t('devices.mode.width')}
              inputMode="numeric"
              value={value.width || ''}
              onChange={(e) => onChange({ ...value, width: Math.round(num(e.target.value)), custom: true })}
            />
            <TextInput
              label={t('devices.mode.height')}
              inputMode="numeric"
              value={value.height || ''}
              onChange={(e) => onChange({ ...value, height: Math.round(num(e.target.value)), custom: true })}
            />
            <TextInput
              label={t('devices.mode.refresh')}
              inputMode="decimal"
              value={value.refresh || ''}
              onChange={(e) => onChange({ ...value, refresh: num(e.target.value), custom: true })}
            />
          </div>
          {customModeValid(value) ? (
            <div className="field-hint">{t('devices.mode.customHint')}</div>
          ) : (
            <div className="field-error">{t('devices.mode.invalid')}</div>
          )}
        </div>
      )}
    </>
  );
}

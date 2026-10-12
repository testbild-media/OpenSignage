// Shared i18n for all web apps and the player. Messages live in /locales/<lang>/<ns>.json
// (flat keys); the full key is "<ns>.<key>". English is bundled eagerly as fallback, the
// other languages are loaded on demand. Placeholders use {name}. No plural forms by
// design: texts are phrased so a {count} placeholder suffices.

export const LANGUAGES = [
  { code: 'bg', name: 'Български' },
  { code: 'cs', name: 'Čeština' },
  { code: 'da', name: 'Dansk' },
  { code: 'de', name: 'Deutsch' },
  { code: 'el', name: 'Ελληνικά' },
  { code: 'en', name: 'English' },
  { code: 'es', name: 'Español' },
  { code: 'et', name: 'Eesti' },
  { code: 'fi', name: 'Suomi' },
  { code: 'fr', name: 'Français' },
  { code: 'ga', name: 'Gaeilge' },
  { code: 'hr', name: 'Hrvatski' },
  { code: 'hu', name: 'Magyar' },
  { code: 'it', name: 'Italiano' },
  { code: 'lt', name: 'Lietuvių' },
  { code: 'lv', name: 'Latviešu' },
  { code: 'mt', name: 'Malti' },
  { code: 'nl', name: 'Nederlands' },
  { code: 'pl', name: 'Polski' },
  { code: 'pt', name: 'Português' },
  { code: 'ro', name: 'Română' },
  { code: 'sk', name: 'Slovenčina' },
  { code: 'sl', name: 'Slovenščina' },
  { code: 'sv', name: 'Svenska' },
] as const;

export type LanguageCode = (typeof LANGUAGES)[number]['code'];
export const DEFAULT_LANGUAGE: LanguageCode = 'en';

type Messages = Record<string, string>;

const english = import.meta.glob('../../../../locales/en/*.json', { eager: true, import: 'default' }) as Record<
  string,
  Record<string, string>
>;
const lazy = import.meta.glob('../../../../locales/*/*.json', { import: 'default' }) as Record<
  string,
  () => Promise<Record<string, string>>
>;

function flatten(files: Record<string, Record<string, string>>): Messages {
  const out: Messages = {};
  for (const [path, msgs] of Object.entries(files)) {
    const ns = path.split('/').pop()!.replace(/\.json$/, '');
    for (const [k, v] of Object.entries(msgs)) out[`${ns}.${k}`] = v;
  }
  return out;
}

const fallback: Messages = flatten(english);
let namespaces: string[] | null = null;

/** Restricts lazy loading to some namespaces (the player only needs common + player). */
export function restrictNamespaces(ns: string[]): void {
  namespaces = ns;
}
let current: Messages = fallback;
let currentLang: LanguageCode = DEFAULT_LANGUAGE;
const listeners = new Set<() => void>();

export function isLanguage(code: string | null | undefined): code is LanguageCode {
  return !!code && LANGUAGES.some((l) => l.code === code);
}

/** Loads a language and makes it current. Unknown codes fall back to English. */
export async function setLanguage(code: string): Promise<void> {
  const lang: LanguageCode = isLanguage(code) ? code : DEFAULT_LANGUAGE;
  if (lang === DEFAULT_LANGUAGE) {
    current = fallback;
  } else {
    const files: Record<string, Record<string, string>> = {};
    await Promise.all(
      Object.entries(lazy)
        .filter(([p]) => p.includes(`/locales/${lang}/`))
        .filter(([p]) => !namespaces || namespaces.includes(p.split('/').pop()!.replace(/\.json$/, '')))
        .map(async ([p, load]) => {
          files[p] = await load();
        }),
    );
    current = flatten(files);
  }
  currentLang = lang;
  document.documentElement.lang = lang;
  for (const l of listeners) l();
}

export function getLanguage(): LanguageCode {
  return currentLang;
}

export function onLanguageChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export type Params = Record<string, string | number>;

/** Translates key with {placeholders}. Missing keys return the key itself (visible in tests). */
export function t(key: string, params?: Params): string {
  let s = current[key] || fallback[key];
  if (s === undefined) {
    if (import.meta.env?.DEV) console.warn(`missing translation: ${key}`);
    return key;
  }
  if (params) {
    for (const [k, v] of Object.entries(params)) s = s.split(`{${k}}`).join(String(v));
  }
  return s;
}

/** True when a key exists (used to translate server error codes with a generic fallback). */
export function has(key: string): boolean {
  return key in fallback;
}

/** Formats money in minor units using the current UI language. */
export function formatMoney(amount: number, currency: string, lang: string = currentLang): string {
  try {
    return new Intl.NumberFormat(lang, { style: 'currency', currency }).format(amount / 100);
  } catch {
    return `${(amount / 100).toFixed(2)} ${currency}`;
  }
}

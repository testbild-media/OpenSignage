// Own account: every user changes the own password and the own
// language. Shared by the admin profile page and the account menu of the operator UI, so
// both use the same API calls and the same rules.
import { LANGUAGES, setLanguage } from '@opensignage/i18n';
import { patch, post } from './api';
import { storeLanguage } from './session';

/** Changes the own password; the server ends all other sessions of the user. */
export async function changeOwnPassword(current: string, password: string, passwordConfirm: string): Promise<void> {
  await post('/api/v1/auth/password', { current, password, passwordConfirm });
}

/**
 * Saves the own language ('' = server default) on the account and in this browser, then
 * applies it. The language only changes on save, never on selection.
 */
export async function saveOwnLanguage(lang: string, refresh: () => Promise<void>): Promise<void> {
  await patch('/api/v1/auth/profile', { language: lang });
  storeLanguage(lang || null);
  await setLanguage(lang || 'en');
  await refresh();
}

/** Options of the language select: server default first, then every UI language. */
export function languageOptions(serverDefault: string): { value: string; label: string }[] {
  return [{ value: '', label: serverDefault }, ...LANGUAGES.map((l) => ({ value: l.code, label: l.name }))];
}

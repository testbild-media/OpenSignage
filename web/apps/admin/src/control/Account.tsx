// Account menu of the operator UI: every user, including the
// role "user" that never sees the administration, changes the own password and the own
// language here and logs out. Same API, rules and texts as the admin profile page.
import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDown, KeyRound, Languages, Loader2, LogOut, UserRound } from 'lucide-react';
import { getLanguage, LANGUAGES } from '@opensignage/i18n';
import { useAction, useT } from '../lib/hooks';
import { useSession } from '../lib/session';
import { changeOwnPassword, languageOptions, saveOwnLanguage } from '../lib/account';
import { useToast } from '../components/feedback';
import { Button, Dialog, ErrorText, PasswordPair, passwordPairValid, Select, TextInput } from '../components/ui';

function PasswordDialog({ onClose }: { onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const save = useAction(async () => {
    await changeOwnPassword(current, pw, pw2);
    toast(t('admin.profile.passwordChanged'));
    onClose();
  });
  return (
    <Dialog
      title={t('admin.profile.changePassword')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!current || !passwordPairValid(pw, pw2)} onClick={() => void save.run()}>
            {t('admin.profile.changePassword')}
          </Button>
        </>
      }
    >
      <div className="form">
        <TextInput type="password" label={t('admin.profile.current')} value={current} autoComplete="current-password" onChange={(e) => setCurrent(e.target.value)} />
        <PasswordPair value={pw} confirm={pw2} onChange={setPw} onConfirmChange={setPw2} />
        <ErrorText error={save.error} />
      </div>
    </Dialog>
  );
}

function LanguageDialog({ onClose }: { onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const { user, refresh } = useSession();
  const [lang, setLang] = useState(user?.language || '');
  const save = useAction(async () => {
    await saveOwnLanguage(lang, refresh);
    toast(t('admin.saved'));
    onClose();
  });
  return (
    <Dialog
      title={t('admin.settings.language')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} onClick={() => void save.run()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <div className="form">
        <Select label={t('admin.settings.language')} value={lang} onChange={(e) => setLang(e.target.value)} options={languageOptions(t('admin.profile.serverDefault'))} />
        <ErrorText error={save.error} />
      </div>
    </Dialog>
  );
}

/** Account button in the top bar with a touch menu: password, language, log out. */
export function AccountMenu() {
  const t = useT();
  const { user, logout } = useSession();
  const out = useAction(logout);
  const [open, setOpen] = useState(false);
  const [dialog, setDialog] = useState<'password' | 'language' | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  // A tap outside the menu or Escape closes it.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent): void => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('pointerdown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  if (!user) return null;
  const language = LANGUAGES.find((l) => l.code === getLanguage())?.name ?? '';
  const show = (d: 'password' | 'language'): void => {
    setOpen(false);
    setDialog(d);
  };
  const close = (): void => setDialog(null);
  return (
    <div className="ctl-account" ref={ref}>
      <button
        type="button"
        className={`ctl-account-btn ${open ? 'open' : ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('admin.profile.title')}
        title={t('admin.profile.title')}
        onClick={() => setOpen((o) => !o)}
      >
        <UserRound size={22} />
        <span className="ctl-hide-tablet ellipsis">{user.displayName}</span>
        <ChevronDown size={16} className="ctl-hide-tablet" />
      </button>
      {open && (
        <div className="ctl-menu" role="menu" aria-label={t('admin.profile.title')}>
          <div className="ctl-menu-head">
            <div className="ctl-menu-name ellipsis">{user.displayName}</div>
            <div className="ctl-menu-sub ellipsis">
              {user.username} · {t(`admin.role.${user.role}`)}
            </div>
          </div>
          <button type="button" role="menuitem" className="ctl-menu-item" onClick={() => show('password')}>
            <KeyRound size={20} />
            <span className="ctl-menu-label">{t('admin.profile.changePassword')}</span>
          </button>
          <button type="button" role="menuitem" className="ctl-menu-item" onClick={() => show('language')}>
            <Languages size={20} />
            <span className="ctl-menu-label">{t('admin.settings.language')}</span>
            <span className="ctl-menu-value ellipsis">{language}</span>
          </button>
          <button type="button" role="menuitem" className="ctl-menu-item" disabled={out.busy} onClick={() => void out.run()}>
            {out.busy ? <Loader2 size={20} className="spin" /> : <LogOut size={20} />}
            <span className="ctl-menu-label">{t('control.logout')}</span>
          </button>
        </div>
      )}
      {/* Dialogs live outside the sticky top bar so they cover the whole page. */}
      {dialog === 'password' && createPortal(<PasswordDialog onClose={close} />, document.body)}
      {dialog === 'language' && createPortal(<LanguageDialog onClose={close} />, document.body)}
    </div>
  );
}

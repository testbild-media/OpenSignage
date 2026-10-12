// Name dialog (create/rename with explicit save) and the destination chooser for moves:
// the target is chosen by really clicking through the folders, never
// from a flat indented list.
import { type FormEvent, useState } from 'react';
import { ChevronRight, Folder, FolderPlus } from 'lucide-react';
import { post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { Button, DataState, Dialog, TextInput } from '../../components/ui';
import { Breadcrumb } from '../../components/MediaPicker';
import { listingPath, type MediaListing, mediaErrorText, ROOT } from './model';

/** Same rules as the server (cleanLabel): 1–120 characters, no slashes. */
export function validName(name: string): boolean {
  const s = name.trim();
  return [...s].length >= 1 && [...s].length <= 120 && !/[/\\]/.test(s) && s !== '.' && s !== '..';
}

export function NameDialog({
  title,
  label,
  initial = '',
  confirmLabel,
  onSubmit,
  onClose,
}: {
  title: string;
  label: string;
  initial?: string;
  confirmLabel: string;
  onSubmit: (name: string) => Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const [name, setName] = useState(initial);
  const save = useAction(() => onSubmit(name.trim()));
  const valid = validName(name);
  const changed = name.trim() !== initial.trim();
  const submit = (e?: FormEvent): void => {
    e?.preventDefault();
    if (valid && changed) void save.run();
  };
  return (
    <Dialog
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" busy={save.busy} disabled={!valid || !changed} onClick={() => submit()}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <TextInput
          label={label}
          value={name}
          maxLength={120}
          onChange={(e) => {
            setName(e.target.value);
            save.clearError();
          }}
          error={name.trim() && !valid ? t('media.nameRule') : null}
        />
        {!!save.error && (
          <div className="form-error" role="alert">
            {mediaErrorText(save.error)}
          </div>
        )}
      </form>
    </Dialog>
  );
}

/**
 * Destination chooser. `blocked` folders (the folder being moved) cannot be entered;
 * `source` is the current location (moving there is pointless), undefined when mixed.
 */
export function FolderChooserDialog({
  title,
  start,
  blocked = [],
  source,
  confirmLabel,
  onConfirm,
  onClose,
}: {
  title: string;
  start: string;
  blocked?: string[];
  source?: string | null;
  confirmLabel?: string;
  onConfirm: (target: string | null) => Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const [folder, setFolder] = useState(start);
  const list = useQuery<MediaListing>(listingPath({ folder, limit: 1 }));
  const [newName, setNewName] = useState<string | null>(null);
  const target = folder === ROOT ? null : folder;
  const here = target === null ? t('media.root') : (list.data?.breadcrumb.at(-1)?.name ?? '…');
  const same = source !== undefined && (source ?? null) === target;
  const create = useAction(async () => {
    const f = await post<{ id: string }>('/api/v1/media/folders', {
      name: (newName ?? '').trim(),
      parentId: target,
    });
    setNewName(null);
    setFolder(f.id);
  });
  const move = useAction(() => onConfirm(target));
  return (
    <Dialog
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button
            variant="primary"
            busy={move.busy}
            disabled={same || !list.data}
            onClick={() => void move.run()}
          >
            {confirmLabel ?? t('media.move.here', { name: here })}
          </Button>
        </>
      }
    >
      <Breadcrumb path={list.data?.breadcrumb ?? []} onOpen={(id) => setFolder(id ?? ROOT)} />
      <div className="mm-chooser" aria-busy={list.loading || undefined}>
        <DataState
          loading={list.loading && !list.data}
          error={list.error}
          onRetry={list.reload}
          empty={!!list.data && list.data.folders.length === 0}
          emptyText={t('media.move.noSubfolders')}
        >
          <div className="mm-list">
            {list.data?.folders.map((f) => {
              const no = blocked.includes(f.id);
              return (
                <button
                  key={f.id}
                  type="button"
                  className="mm-row"
                  disabled={no}
                  title={no ? t('media.move.intoItself') : f.name}
                  onClick={() => setFolder(f.id)}
                >
                  <Folder size={16} className="mm-folder-icon" aria-hidden />
                  <span className="mm-row-name">{f.name}</span>
                  <ChevronRight size={14} aria-hidden />
                </button>
              );
            })}
          </div>
        </DataState>
      </div>
      {newName === null ? (
        <Button className="btn-sm" icon={<FolderPlus size={14} />} onClick={() => setNewName('')}>
          {t('media.folder.newHere')}
        </Button>
      ) : (
        <form
          className="mm-new-folder"
          onSubmit={(e) => {
            e.preventDefault();
            if (validName(newName)) void create.run();
          }}
        >
          <TextInput
            label={t('media.folder.name')}
            value={newName}
            maxLength={120}
            autoFocus
            onChange={(e) => setNewName(e.target.value)}
          />
          <Button type="submit" variant="secondary" busy={create.busy} disabled={!validName(newName)}>
            {t('media.folder.create')}
          </Button>
          <Button onClick={() => setNewName(null)}>{t('common.action.cancel')}</Button>
        </form>
      )}
      {same && <p className="faint">{t('media.move.alreadyHere')}</p>}
      {!!(create.error ?? move.error) && (
        <div className="form-error" role="alert">
          {mediaErrorText(create.error ?? move.error)}
        </div>
      )}
    </Dialog>
  );
}

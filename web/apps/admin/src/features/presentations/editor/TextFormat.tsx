// Text formatting of the selected text, clock and ticker elements (panel, "Element"): font,
// size, marks, colors and paragraph. While a text is being edited it formats the marked
// characters, otherwise the whole element(s).
import type { ReactNode } from 'react';
import { AlignCenter, AlignJustify, AlignLeft, AlignRight, Bold, CaseUpper, Italic, List, ListOrdered, Rows3, Strikethrough, Underline } from 'lucide-react';
import type { HAlign, Marks, SlideElement, TextStyle } from '@opensignage/contracts';
import { useT } from '../../../lib/hooks';
import type { EditorStore } from './store';
import { applyParagraph, applyTextMark, selectionMark, textSession, useTextEditing } from './TextEditor';
import { clearMarkEverywhere, elementText } from './richtext';
import { ColorInput, FontSelect, Num, Row } from './controls';
import type { FontOption } from './env';

type TextLike = SlideElement & { style: TextStyle };

export function isTextLike(e: SlideElement): e is TextLike {
  return e.type === 'text' || e.type === 'clock' || e.type === 'ticker';
}

/** Applies a text property: marks while editing text, else the style of the elements. */
function useTextFormat(store: EditorStore) {
  return (name: keyof Marks, value: unknown): void => {
    if (textSession.view && applyTextMark(name, value)) return;
    const ids = store.selected.filter(isTextLike).map((e) => e.id);
    if (!ids.length) return;
    store.updateElements(ids, (e) => {
      if (!('style' in e) || !e.style) return;
      (e.style as unknown as Record<string, unknown>)[name] = value;
      if (e.type === 'text') e.doc = clearMarkEverywhere(e.doc, name);
    });
  };
}

/** Toggle button that keeps the text selection while editing (no focus change). */
function Tog({ label, active, disabled, onClick, children }: { label: string; active?: boolean; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      className={`tog ${active ? 'active' : ''}`}
      aria-pressed={active}
      title={label}
      aria-label={label}
      disabled={disabled}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

export function TextFormat({ store, elements, fonts }: { store: EditorStore; elements: SlideElement[]; fonts: FontOption[] }) {
  const t = useT();
  const format = useTextFormat(store);
  const editing = useTextEditing();
  const texts = elements.filter(isTextLike);
  const first = texts[0];
  if (!first) return null;
  const s = first.style;
  const ids = texts.map((e) => e.id);
  // Yes/no marks: while editing the state of the marked text (it can differ from the element),
  // toggled for exactly that text; back to the element's value means no own mark.
  type Flag = 'bold' | 'italic' | 'underline' | 'strike';
  const flag = (name: Flag): boolean => (editing ? selectionMark(name, s[name]) : s[name]);
  const toggle = (name: Flag): void => {
    const next = !flag(name);
    if (editing) applyTextMark(name, next === s[name] ? null : next);
    else format(name, next);
  };
  const align = (a: HAlign): void => {
    if (editing) applyParagraph({ align: a });
    else store.updateElements(ids, (e) => 'style' in e && e.style && (e.style.align = a));
  };
  return (
    <>
      <h3>{t('editor.group.text')}</h3>
      <Row label={t('editor.prop.font')}>
        <FontSelect value={s.font} fonts={fonts} sampleText={elementText(first)} missingLabel={t('editor.font.missingChars')} onChange={(f) => format('font', f)} />
      </Row>
      <Row label={t('editor.prop.size')}>
        <Num value={s.size} min={1} max={2000} onChange={(v) => format('size', v)} />
      </Row>
      <div className="tog-row" role="group" aria-label={t('editor.group.text')}>
        <Tog label={t('editor.bold')} active={flag('bold')} onClick={() => toggle('bold')}>
          <Bold size={15} />
        </Tog>
        <Tog label={t('editor.italic')} active={flag('italic')} onClick={() => toggle('italic')}>
          <Italic size={15} />
        </Tog>
        <Tog label={t('editor.underline')} active={flag('underline')} onClick={() => toggle('underline')}>
          <Underline size={15} />
        </Tog>
        <Tog label={t('editor.strike')} active={flag('strike')} onClick={() => toggle('strike')}>
          <Strikethrough size={15} />
        </Tog>
        <Tog label={t('editor.uppercase')} active={!!s.uppercase} onClick={() => store.updateElements(ids, (e) => 'style' in e && e.style && (e.style.uppercase = !e.style.uppercase))}>
          <CaseUpper size={15} />
        </Tog>
      </div>
      <Row label={t('editor.prop.color')}>
        <ColorInput value={s.color} onChange={(v) => format('color', v ?? '#ffffff')} />
      </Row>
      <Row label={t('editor.highlight')}>
        <ColorInput value={s.highlight} allowNone noneLabel={t('editor.none')} onChange={(v) => format('highlight', v ?? null)} />
      </Row>
      <h3>{t('editor.group.paragraph')}</h3>
      <div className="tog-row" role="group" aria-label={t('editor.group.paragraph')}>
        {(['left', 'center', 'right', 'justify'] as HAlign[]).map((a) => {
          const Icon = { left: AlignLeft, center: AlignCenter, right: AlignRight, justify: AlignJustify }[a];
          return (
            <Tog key={a} label={t(`editor.align.${a}`)} active={s.align === a} onClick={() => align(a)}>
              <Icon size={15} />
            </Tog>
          );
        })}
        <span className="tog-sep" />
        {/* Lists work on the paragraphs being edited (double-click into the text first). */}
        <Tog label={t('editor.bullets')} disabled={!editing} onClick={() => applyParagraph({ bullet: 'disc' })}>
          <List size={15} />
        </Tog>
        <Tog label={t('editor.numbering')} disabled={!editing} onClick={() => applyParagraph({ bullet: 'number' })}>
          <ListOrdered size={15} />
        </Tog>
        <Tog label={t('editor.noBullets')} disabled={!editing} onClick={() => applyParagraph({ bullet: null })}>
          <Rows3 size={15} />
        </Tog>
      </div>
    </>
  );
}

// ProseMirror schema that maps 1:1 onto the slide RichDoc format, plus lossless
// conversion in both directions. Rendering uses the same span styles as the player.
import { type Mark, type Node as PMNode, Schema } from 'prosemirror-model';
import type { HAlign, Marks, RichDoc, Run, SlideElement } from '@opensignage/contracts';
import { applyMarks } from '@opensignage/render';

function markDom(name: keyof Marks, value: unknown): HTMLElement {
  const span = document.createElement('span');
  applyMarks(span, { [name]: value } as Marks);
  span.dataset.mark = name;
  return span;
}

const valueMark = (name: keyof Marks) => ({
  attrs: { value: {} },
  toDOM: (m: Mark) => markDom(name, m.attrs.value),
  parseDOM: [],
});

export const richSchema = new Schema({
  nodes: {
    doc: { content: 'paragraph+' },
    paragraph: {
      content: 'text*',
      attrs: { align: { default: null }, bullet: { default: null } },
      toDOM: (n) => {
        const el = document.createElement('div');
        el.className = 'os-p';
        if (n.attrs.align) el.style.textAlign = n.attrs.align as string;
        if (n.attrs.bullet && n.attrs.bullet !== 'none') el.dataset.bullet = n.attrs.bullet as string;
        return { dom: el, contentDOM: el };
      },
      parseDOM: [{ tag: 'div.os-p' }, { tag: 'p' }],
    },
    text: {},
  },
  marks: {
    bold: valueMark('bold'),
    italic: valueMark('italic'),
    underline: valueMark('underline'),
    strike: valueMark('strike'),
    color: valueMark('color'),
    highlight: valueMark('highlight'),
    font: valueMark('font'),
    size: valueMark('size'),
    letterSpacing: valueMark('letterSpacing'),
  },
});

const MARK_NAMES: (keyof Marks)[] = ['bold', 'italic', 'underline', 'strike', 'color', 'highlight', 'font', 'size', 'letterSpacing'];

export function toPM(doc: RichDoc): PMNode {
  const paras = (doc.paragraphs.length ? doc.paragraphs : [{ runs: [] }]).map((p) => {
    const content = p.runs
      .filter((r) => r.text !== '')
      .map((r) => {
        const marks: Mark[] = [];
        for (const name of MARK_NAMES) {
          const v = r.marks?.[name];
          if (v !== undefined) marks.push(richSchema.marks[name]!.create({ value: v }));
        }
        return richSchema.text(r.text, marks);
      });
    return richSchema.nodes.paragraph!.create({ align: p.align ?? null, bullet: p.bullet ?? null }, content);
  });
  return richSchema.nodes.doc!.create(null, paras);
}

export function fromPM(node: PMNode): RichDoc {
  const paragraphs: RichDoc['paragraphs'] = [];
  node.forEach((p) => {
    const runs: Run[] = [];
    p.forEach((t) => {
      const marks: Marks = {};
      for (const m of t.marks) (marks as Record<string, unknown>)[m.type.name] = m.attrs.value;
      const run: Run = { text: t.text ?? '' };
      if (Object.keys(marks).length) run.marks = marks;
      // Merge with the previous run if the marks are identical.
      const last = runs[runs.length - 1];
      if (last && JSON.stringify(last.marks ?? {}) === JSON.stringify(run.marks ?? {})) last.text += run.text;
      else runs.push(run);
    });
    const para: RichDoc['paragraphs'][number] = { runs };
    if (p.attrs.align) para.align = p.attrs.align as HAlign;
    if (p.attrs.bullet) para.bullet = p.attrs.bullet as 'none' | 'disc' | 'number';
    paragraphs.push(para);
  });
  return { paragraphs };
}

/** The text an element shows (for the font coverage warning). */
export function elementText(e: SlideElement): string {
  if (e.type === 'text') return e.doc.paragraphs.map((p) => p.runs.map((r) => r.text).join('')).join('\n');
  if (e.type === 'ticker') return e.text ?? '';
  return '';
}

/** Plain text → RichDoc (one paragraph per line). */
export function plainDoc(text: string): RichDoc {
  return { paragraphs: text.split('\n').map((line) => ({ runs: line ? [{ text: line }] : [] })) };
}

/** Removes one mark from every run, so the element style applies to the whole text. */
export function clearMarkEverywhere(doc: RichDoc, name: keyof Marks): RichDoc {
  return {
    paragraphs: doc.paragraphs.map((p) => ({
      ...p,
      runs: p.runs.map((r) => {
        if (!r.marks || !(name in r.marks)) return r;
        const marks = { ...r.marks };
        delete marks[name];
        return Object.keys(marks).length ? { ...r, marks } : { text: r.text };
      }),
    })),
  };
}

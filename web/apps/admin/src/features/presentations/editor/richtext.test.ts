import { describe, expect, it } from 'vitest';
import type { RichDoc } from '@opensignage/contracts';
import { fromPM, plainDoc, toPM } from './richtext';

describe('rich text conversion', () => {
  it('round-trips mixed formatting losslessly', () => {
    const doc: RichDoc = {
      paragraphs: [
        { align: 'center', runs: [{ text: 'Happy ' }, { text: 'Hour', marks: { bold: true, color: '#ff8a00', size: 72 } }] },
        { runs: [] },
        { bullet: 'disc', runs: [{ text: 'Cocktails', marks: { italic: true, font: 'Lobster' } }, { text: ' 5 €', marks: { strike: true } }] },
      ],
    };
    expect(fromPM(toPM(doc))).toEqual(doc);
  });

  it('builds paragraphs from plain text', () => {
    expect(plainDoc('a\n\nb').paragraphs).toEqual([{ runs: [{ text: 'a' }] }, { runs: [] }, { runs: [{ text: 'b' }] }]);
  });
});

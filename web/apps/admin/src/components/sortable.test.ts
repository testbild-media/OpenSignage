import { describe, expect, it } from 'vitest';
import { moveId, shiftId } from './sortable';
import { joinColor, splitColor, validColor } from './controls';

describe('moveId', () => {
  const ids = ['a', 'b', 'c', 'd'];
  it('moves before and after a target', () => {
    expect(moveId(ids, 'd', 'a', 'before')).toEqual(['d', 'a', 'b', 'c']);
    expect(moveId(ids, 'a', 'c', 'after')).toEqual(['b', 'c', 'a', 'd']);
    expect(moveId(ids, 'a', 'd', 'after')).toEqual(['b', 'c', 'd', 'a']);
    expect(moveId(ids, 'b', 'c', 'before')).toEqual(['a', 'b', 'c', 'd']);
  });
  it('ignores unknown ids and self drops', () => {
    expect(moveId(ids, 'x', 'a', 'before')).toBe(ids);
    expect(moveId(ids, 'a', 'a', 'after')).toBe(ids);
  });
});

describe('shiftId', () => {
  it('moves by one and clamps', () => {
    expect(shiftId(['a', 'b', 'c'], 'b', -1)).toEqual(['b', 'a', 'c']);
    expect(shiftId(['a', 'b', 'c'], 'b', 1)).toEqual(['a', 'c', 'b']);
    expect(shiftId(['a', 'b', 'c'], 'a', -1)).toEqual(['a', 'b', 'c']);
  });
});

describe('colours', () => {
  it('validates hex colours', () => {
    expect(validColor('#ff8a00')).toBe(true);
    expect(validColor('#000000cc')).toBe(true);
    expect(validColor('#000000cc', false)).toBe(false);
    expect(validColor('red')).toBe(false);
  });
  it('splits and joins opacity', () => {
    expect(splitColor('#000000cc')).toEqual({ base: '#000000', opacity: 80 });
    expect(splitColor('#FFFFFF')).toEqual({ base: '#ffffff', opacity: 100 });
    expect(joinColor('#000000', 80)).toBe('#000000cc');
    expect(joinColor('#ABCDEF', 100)).toBe('#abcdef');
    expect(joinColor('#000000', 0)).toBe('#00000000');
  });
});

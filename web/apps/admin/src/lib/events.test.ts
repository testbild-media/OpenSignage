import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { isConnected, startEvents, stopEvents } from './events';

// Minimal WebSocket stand-in: the test decides when a socket opens or closes.
class FakeSocket {
  static all: FakeSocket[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  open(): void {
    this.onopen?.();
  }
  close(): void {
    this.closed = true;
  }
  serverClosed(): void {
    this.onclose?.();
  }
}

beforeEach(() => {
  FakeSocket.all = [];
  vi.useFakeTimers();
  vi.stubGlobal('WebSocket', FakeSocket);
});

afterEach(() => {
  stopEvents();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('admin event socket', () => {
  it('reconnects after a lost connection', () => {
    startEvents();
    FakeSocket.all[0]!.open();
    expect(isConnected()).toBe(true);
    FakeSocket.all[0]!.serverClosed();
    expect(isConnected()).toBe(false);
    vi.advanceTimersByTime(1000);
    expect(FakeSocket.all).toHaveLength(2);
    FakeSocket.all[1]!.open();
    expect(isConnected()).toBe(true);
  });

  it('does not reconnect after stop (logout)', () => {
    startEvents();
    FakeSocket.all[0]!.open();
    stopEvents();
    expect(FakeSocket.all[0]!.closed).toBe(true);
    expect(isConnected()).toBe(false);
    FakeSocket.all[0]!.serverClosed(); // the close event arrives later
    vi.advanceTimersByTime(60_000);
    expect(FakeSocket.all).toHaveLength(1);
  });

  it('a late close of a replaced socket does not hide the new connection', () => {
    startEvents(); // e.g. React StrictMode: start, stop, start
    stopEvents();
    startEvents();
    expect(FakeSocket.all).toHaveLength(2);
    FakeSocket.all[1]!.open();
    FakeSocket.all[0]!.serverClosed();
    expect(isConnected()).toBe(true);
    vi.advanceTimersByTime(60_000);
    expect(FakeSocket.all).toHaveLength(2);
  });
});

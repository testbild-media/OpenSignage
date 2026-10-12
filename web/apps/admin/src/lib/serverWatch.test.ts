import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearUpdated, reloadForUpdate, updatedTo, watchServerVersion } from './serverWatch';

describe('server version watch', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('reloads when the server reports another version, not for the same one', async () => {
    let version: string | null = '1.0.0';
    const onChange = vi.fn();
    const stop = watchServerVersion('1.0.0', onChange, async () => version);
    await vi.advanceTimersByTimeAsync(61000);
    expect(onChange).not.toHaveBeenCalled();
    version = null; // server restarting: unreachable
    await vi.advanceTimersByTimeAsync(61000);
    expect(onChange).not.toHaveBeenCalled();
    version = '1.1.0';
    await vi.advanceTimersByTimeAsync(61000);
    expect(onChange).toHaveBeenCalledWith('1.1.0');
    await vi.advanceTimersByTimeAsync(180000);
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
  });

  it('checks at once when the page becomes visible again', async () => {
    const onChange = vi.fn();
    const stop = watchServerVersion('1.0.0', onChange, async () => '2.0.0');
    document.dispatchEvent(new Event('visibilitychange'));
    await vi.advanceTimersByTimeAsync(10);
    expect(onChange).toHaveBeenCalledWith('2.0.0');
    stop();
  });

  it('remembers the new version for the login page and never reloads in a loop', () => {
    const reload = vi.fn();
    reloadForUpdate('1.0.0', '1.1.0', reload);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(updatedTo()).toBe('1.1.0');
    // The same change again right after the reload (stale page): no second reload.
    reloadForUpdate('1.0.0', '1.1.0', reload);
    expect(reload).toHaveBeenCalledTimes(1);
    // Later on, the same change is allowed again (e.g. update, rollback, update).
    vi.advanceTimersByTime(31000);
    reloadForUpdate('1.0.0', '1.1.0', reload);
    expect(reload).toHaveBeenCalledTimes(2);
    clearUpdated();
    expect(updatedTo()).toBeNull();
  });
});

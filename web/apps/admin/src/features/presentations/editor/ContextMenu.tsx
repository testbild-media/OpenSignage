// Right-click menu of the editor (canvas, slide rail, layers, guides). Own design, never the
// browser menu. Keyboard: arrows move, Enter/Space activates, → opens and ← / Esc closes a
// submenu, Esc / Tab close the menu. A click outside, resizing or leaving the window closes it.
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { Check, ChevronRight } from 'lucide-react';

export interface MenuAction {
  label: string;
  icon?: ReactNode;
  shortcut?: string;
  disabled?: boolean;
  danger?: boolean;
  /** Toggle entries show a check mark (menuitemcheckbox). */
  checked?: boolean;
  onSelect?: () => void;
  submenu?: MenuEntry[];
}

export type MenuEntry = MenuAction | 'separator';

export interface MenuState {
  x: number;
  y: number;
  items: MenuEntry[];
}

/** Screen position for a menu opened from the keyboard (context menu key, Shift+F10). */
export function menuPointFor(el: Element | null): { x: number; y: number } {
  const r = el?.getBoundingClientRect();
  return r ? { x: Math.round(r.left + Math.min(24, r.width / 2)), y: Math.round(r.top + Math.min(24, r.height / 2)) } : { x: 40, y: 40 };
}

/** True for the keys that open a context menu from the keyboard. */
export function isMenuKey(e: { key: string; shiftKey: boolean }): boolean {
  return e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10');
}

function enabled(items: MenuEntry[], i: number): boolean {
  const it = items[i];
  return !!it && it !== 'separator' && !it.disabled;
}

function step(items: MenuEntry[], from: number, dir: 1 | -1): number {
  for (let k = 1; k <= items.length; k++) {
    const i = (from + dir * k + items.length * 2) % items.length;
    if (enabled(items, i)) return i;
  }
  return from;
}

function MenuList({
  items,
  x,
  y,
  flipX,
  level,
  focusFirst,
  label,
  onClose,
  onBack,
}: {
  items: MenuEntry[];
  x: number;
  y: number;
  /** Right edge to use when the menu does not fit to the right of x. */
  flipX: number;
  level: number;
  focusFirst: boolean;
  label?: string;
  onClose: () => void;
  onBack?: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const [active, setActive] = useState(-1);
  const [sub, setSub] = useState<{ index: number; focus: boolean } | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    let left = x;
    let top = y;
    if (left + r.width > vw - 4) left = Math.max(4, flipX - r.width);
    if (top + r.height > vh - 4) top = Math.max(4, vh - r.height - 4);
    setPos({ left, top });
  }, [x, y, flipX]);

  useEffect(() => {
    if (!focusFirst) return;
    const first = step(items, -1, 1);
    if (enabled(items, first)) {
      setActive(first);
      itemRefs.current[first]?.focus();
    } else {
      ref.current?.focus();
    }
    // Only when the menu opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const focusItem = (i: number): void => {
    setActive(i);
    itemRefs.current[i]?.focus();
  };

  const activate = (i: number, viaKeyboard: boolean): void => {
    const it = items[i];
    if (!it || it === 'separator' || it.disabled) return;
    if (it.submenu) {
      setSub({ index: i, focus: viaKeyboard });
      return;
    }
    onClose();
    it.onSelect?.();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    // The innermost menu handles the key; the editor shortcuts never see it.
    e.stopPropagation();
    switch (e.key) {
      case 'ArrowDown':
        focusItem(step(items, active, 1));
        break;
      case 'ArrowUp':
        focusItem(step(items, active < 0 ? 0 : active, -1));
        break;
      case 'Home':
        focusItem(step(items, -1, 1));
        break;
      case 'End':
        focusItem(step(items, items.length, -1));
        break;
      case 'Enter':
      case ' ':
        if (active >= 0) activate(active, true);
        break;
      case 'ArrowRight': {
        const it = items[active];
        if (it && it !== 'separator' && it.submenu && !it.disabled) setSub({ index: active, focus: true });
        break;
      }
      case 'ArrowLeft':
        if (level > 0) onBack?.();
        break;
      case 'Escape':
        if (level > 0) onBack?.();
        else onClose();
        break;
      case 'Tab':
        onClose();
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  const subItem = sub ? items[sub.index] : undefined;
  const subRect = sub ? itemRefs.current[sub.index]?.getBoundingClientRect() : undefined;
  return (
    <div
      ref={ref}
      className="ctx-menu"
      role="menu"
      aria-label={label}
      tabIndex={-1}
      // Placed at the click point first and moved into the viewport before the first paint
      // (layout effect); never hidden, so the first item can take the focus right away.
      style={pos ? { left: pos.left, top: pos.top } : { left: x, top: y }}
      onKeyDown={onKeyDown}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((it, i) => {
        if (it === 'separator') return <div key={i} className="ctx-sep" role="separator" />;
        const toggle = it.checked !== undefined;
        return (
          <button
            key={i}
            ref={(el) => {
              itemRefs.current[i] = el;
            }}
            type="button"
            role={toggle ? 'menuitemcheckbox' : 'menuitem'}
            aria-checked={toggle ? it.checked : undefined}
            aria-disabled={it.disabled || undefined}
            aria-haspopup={it.submenu ? 'menu' : undefined}
            aria-expanded={it.submenu ? sub?.index === i : undefined}
            tabIndex={i === active ? 0 : -1}
            className={`ctx-item ${it.danger ? 'danger' : ''} ${i === active ? 'active' : ''}`}
            onMouseEnter={() => {
              if (it.disabled) return;
              focusItem(i);
              setSub(it.submenu ? { index: i, focus: false } : null);
            }}
            onClick={() => activate(i, false)}
          >
            <span className="ctx-icon">{toggle ? it.checked && <Check size={15} /> : it.icon}</span>
            <span className="ctx-label">{it.label}</span>
            {it.shortcut && <span className="ctx-key">{it.shortcut}</span>}
            {it.submenu && <ChevronRight size={14} className="ctx-more" />}
          </button>
        );
      })}
      {sub && subItem && subItem !== 'separator' && subItem.submenu && subRect && (
        <MenuList
          key={`${sub.index}-${sub.focus}`}
          items={subItem.submenu}
          x={subRect.right - 2}
          y={subRect.top - 5}
          flipX={subRect.left + 2}
          level={level + 1}
          focusFirst={sub.focus}
          label={subItem.label}
          onClose={onClose}
          onBack={() => {
            setSub(null);
            focusItem(sub.index);
          }}
        />
      )}
    </div>
  );
}

export function ContextMenu({ menu, onClose, label }: { menu: MenuState; onClose: () => void; label?: string }) {
  const rootRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const prev = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const inside = (t: EventTarget | null): boolean => t instanceof Node && !!rootRef.current?.contains(t);
    const close = (): void => closeRef.current();
    const onDown = (e: PointerEvent): void => {
      if (!inside(e.target)) close();
    };
    // While open, a context menu event that reaches the page comes from the keyboard
    // (a right click closes this menu first): never show the browser menu for it.
    const onCtx = (e: MouseEvent): void => {
      e.preventDefault();
      if (!inside(e.target)) e.stopPropagation();
    };
    const onWheel = (e: WheelEvent): void => {
      if (!inside(e.target)) close();
    };
    document.addEventListener('pointerdown', onDown, true);
    document.addEventListener('contextmenu', onCtx, true);
    document.addEventListener('wheel', onWheel, { capture: true, passive: true });
    window.addEventListener('resize', close);
    window.addEventListener('blur', close);
    return () => {
      document.removeEventListener('pointerdown', onDown, true);
      document.removeEventListener('contextmenu', onCtx, true);
      document.removeEventListener('wheel', onWheel, { capture: true });
      window.removeEventListener('resize', close);
      window.removeEventListener('blur', close);
      // Give the focus back (keyboard users continue where they opened the menu).
      const now = document.activeElement;
      if ((!now || now === document.body) && prev?.isConnected) prev.focus({ preventScroll: true });
    };
  }, []);

  return createPortal(
    <div ref={rootRef} className="ctx-root">
      <MenuList items={menu.items} x={menu.x} y={menu.y} flipX={menu.x} level={0} focusFirst label={label} onClose={onClose} />
    </div>,
    document.body,
  );
}

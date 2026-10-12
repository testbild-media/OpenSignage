// Confirm dialogs and toasts in the product design (never window.confirm / alert).
import { Component, createContext, type ErrorInfo, type ReactNode, useCallback, useContext, useRef, useState } from 'react';
import { Button, Dialog } from './ui';
import { useT } from '../lib/hooks';
import { errorText } from '../lib/api';

export interface ConfirmOptions {
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
}

type ConfirmFn = (o: ConfirmOptions) => Promise<boolean>;
type ToastFn = (text: string, tone?: 'success' | 'error' | 'info') => void;

const ConfirmCtx = createContext<ConfirmFn>(() => Promise.resolve(false));
const ToastCtx = createContext<ToastFn>(() => {});

export const useConfirm = (): ConfirmFn => useContext(ConfirmCtx);
export const useToast = (): ToastFn => useContext(ToastCtx);

interface Toast {
  id: number;
  text: string;
  tone: 'success' | 'error' | 'info';
}

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const t = useT();
  const [pending, setPending] = useState<(ConfirmOptions & { resolve: (v: boolean) => void }) | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);

  const confirm = useCallback<ConfirmFn>((o) => new Promise((resolve) => setPending({ ...o, resolve })), []);
  const toast = useCallback<ToastFn>((text, tone = 'success') => {
    const id = next.current++;
    setToasts((ts) => [...ts.slice(-3), { id, text, tone }]);
    window.setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== id)), tone === 'error' ? 8000 : 3500);
  }, []);

  const close = (v: boolean): void => {
    pending?.resolve(v);
    setPending(null);
  };

  return (
    <ConfirmCtx.Provider value={confirm}>
      <ToastCtx.Provider value={toast}>
        {children}
        {pending && (
          <Dialog
            title={pending.title}
            onClose={() => close(false)}
            footer={
              <>
                <Button onClick={() => close(false)}>{t('common.action.cancel')}</Button>
                <Button variant={pending.danger ? 'danger' : 'primary'} onClick={() => close(true)}>
                  {pending.confirmLabel ?? t('common.action.confirm')}
                </Button>
              </>
            }
          >
            <div className="confirm-text">{pending.message}</div>
          </Dialog>
        )}
        <div className="toasts" aria-live="polite">
          {toasts.map((x) => (
            <div key={x.id} className={`toast toast-${x.tone}`}>
              {x.text}
            </div>
          ))}
        </div>
      </ToastCtx.Provider>
    </ConfirmCtx.Provider>
  );
}

/** Catches render errors so the app never turns into a white page. */
export class ErrorBoundary extends Component<{ children: ReactNode; label: string; retryLabel: string }, { error: unknown }> {
  override state = { error: null as unknown };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error('render error', error, info.componentStack);
  }

  override render() {
    if (this.state.error) {
      return (
        <div className="state state-error" role="alert">
          <div>{this.props.label}</div>
          <div className="faint">{errorText(this.state.error)}</div>
          <Button variant="primary" onClick={() => this.setState({ error: null })}>
            {this.props.retryLabel}
          </Button>
        </div>
      );
    }
    return this.props.children;
  }
}

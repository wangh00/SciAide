type RuntimeEventCallback = (...data: unknown[]) => void;

declare global {
  interface Window {
    runtime?: {
      EventsOnMultiple: (
        eventName: string,
        callback: RuntimeEventCallback,
        maxCallbacks: number,
      ) => () => void;
	  WindowMinimise?: () => void;
	  WindowToggleMaximise?: () => void;
	  Quit?: () => void;
	  BrowserOpenURL?: (url: string) => void;
	  ClipboardSetText?: (text: string) => Promise<boolean>;
	  OnFileDrop?: (callback: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean) => void;
	  OnFileDropOff?: () => void;
    };
  }
}

export function minimiseWindow(): void { window.runtime?.WindowMinimise?.(); }
export function toggleMaximiseWindow(): void { window.runtime?.WindowToggleMaximise?.(); }
export function quitApplication(): void { window.runtime?.Quit?.(); }
export function openDefaultBrowser(url: string): void {
  const parsed = new URL(url);
  if (!["https:", "http:"].includes(parsed.protocol) || parsed.username || parsed.password) {
    throw new Error("无效的网页地址");
  }
  const open = window.runtime?.BrowserOpenURL;
  if (!open) throw new Error("请在 SciAide 桌面程序中打开此链接");
  open(url);
}
export async function setClipboardText(text: string): Promise<boolean> {
  const write = window.runtime?.ClipboardSetText;
  return write ? write(text) : false;
}
export function onFileDrop(callback: (paths: string[]) => void): () => void {
  const runtime = window.runtime;
  if (!runtime?.OnFileDrop) return () => undefined;
  runtime.OnFileDrop((_x, _y, paths) => callback(paths), true);
  return () => runtime.OnFileDropOff?.();
}

// Wails injects window.runtime before the React application starts. Keeping
// this tiny bridge in source control means TypeScript and Vite do not depend on
// frontend/wailsjs, which is generated during `wails build` and intentionally
// ignored by Git.
export function eventsOn<T>(
  eventName: string,
  callback: (payload: T) => void,
): () => void {
  const subscribe = window.runtime?.EventsOnMultiple;
  if (!subscribe) {
    // Browser-only previews and frontend tests have no Wails runtime.
    return () => undefined;
  }
  return subscribe(eventName, (payload: unknown) => callback(payload as T), -1);
}

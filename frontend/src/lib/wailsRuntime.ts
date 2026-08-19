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
	  OnFileDrop?: (callback: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean) => void;
	  OnFileDropOff?: () => void;
    };
  }
}

export function minimiseWindow(): void { window.runtime?.WindowMinimise?.(); }
export function toggleMaximiseWindow(): void { window.runtime?.WindowToggleMaximise?.(); }
export function quitApplication(): void { window.runtime?.Quit?.(); }
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

type RuntimeEventCallback = (...data: unknown[]) => void;

declare global {
  interface Window {
    runtime?: {
      EventsOnMultiple: (
        eventName: string,
        callback: RuntimeEventCallback,
        maxCallbacks: number,
      ) => () => void;
    };
  }
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


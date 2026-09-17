export function shareSnapshot<T>(previous: T, next: T): T;
export function singleFlight(): <T>(key: string, load: () => Promise<T>) => Promise<T>;
export function conditionalViews(capacity?: number): <T>(key: string, load: (revision: string) => Promise<{revision: string; unchanged: boolean; value?: T; patch?: Partial<T>; removed?: string[]}>) => Promise<T>;

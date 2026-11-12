export function startUIHeartbeat(surface?: Window & typeof globalThis): () => void;
import type { Attachment } from './App';
export type UIView = {projectId: string; conversationId: string; mode: 'chat' | 'research'; draft: string; attachments: Array<Attachment | {id: string; originalName: string; draftPath: string}>};
export function readUIView(surface?: Window & typeof globalThis): UIView | null;
export function saveUIView(value: UIView, surface?: Window & typeof globalThis): void;

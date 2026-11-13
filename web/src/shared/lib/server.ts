import type { State } from '../workbench';
import { CallError, parseCallError } from './call-error';
import { dispatchLiveAudio, type LiveAudioEvent } from './live-audio';

// 网页版传输层：浏览器直接连本机服务，调用 HTTP 接口并订阅状态事件流。
// 与桌面宿主的消息通道、浏览器预览的模拟后端并列，界面按当前环境选一条路。

declare global { interface Window { __yovoiceServer?: boolean } }

// 服务端在入口页注入的标记：页面由 Go 服务提供时，界面走真实接口而不是预览的模拟后端。
export const isServer = typeof window !== 'undefined' && !!window.__yovoiceServer;

// serverCall 调用本机服务的方法：POST /api/call，服务端回 { id, result } 或 { id, error }。
export async function serverCall<T>(method: string, data: unknown): Promise<T> {
  const response = await fetch('/api/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: crypto.randomUUID(), method, data }) });
  const reply = await response.json().catch(() => ({})) as { result?: T; error?: unknown };
  if (reply.error) throw parseCallError(reply.error);
  if (!response.ok) throw new CallError('@yovoice.error.unknown', { detail: `call ${response.status}` });
  return reply.result as T;
}

// subscribeServer 订阅状态事件流：服务端先推完整状态，之后只推变化的顶层字段。
export function subscribeServer(listener: (state: State) => void): () => void {
  const source = new EventSource('/api/state-events');
  let latest: State | null = null;
  source.onmessage = event => {
    let data: { event: string; state?: State; patch?: Partial<State>; removed?: string[] };
    try { data = JSON.parse(event.data); } catch { return; }
    if (data.event === 'state' && data.state) { latest = data.state; listener(latest); return; }
    if (data.event === 'audio' || data.event === 'audio-request-end' || data.event === 'audio-disconnected') { dispatchLiveAudio(data as unknown as LiveAudioEvent); return; }
    if (data.event === 'patch' && latest) {
      const next = { ...latest, ...data.patch } as Record<string, unknown>;
      for (const key of data.removed ?? []) delete next[key];
      latest = next as unknown as State;
      listener(latest);
    }
  };
  return () => source.close();
}

// serverMediaUrl 给出服务上的媒体地址，与本机服务的 /media 路由一致。
export function serverMediaUrl(kind: 'voices' | 'outputs', file: string): string {
  return `/media/${kind}/${encodeURIComponent(file)}`;
}

// serverExportURL 给出待保存导出文件的下载地址：id 来自导出接口的回复。
export function serverExportURL(id: string): string {
  return `/api/export-file?id=${encodeURIComponent(id)}`;
}

// 实时音频事件：桌面宿主的消息通道与网页版的状态事件流共用同一份订阅表。

export interface LiveAudioEvent { event: 'audio' | 'audio-request-end' | 'audio-disconnected'; id?: string; projectId?: string; sequence?: number; audio?: string; start?: boolean; end?: boolean; cancelled?: boolean }

const listeners = new Set<(event: LiveAudioEvent) => void>();

export function subscribeLiveAudio(listener: (event: LiveAudioEvent) => void) { listeners.add(listener); return () => { listeners.delete(listener); }; }

// dispatchLiveAudio 把一条实时音频事件送给所有订阅者。
export function dispatchLiveAudio(event: LiveAudioEvent) { listeners.forEach(fn => fn(event)); }

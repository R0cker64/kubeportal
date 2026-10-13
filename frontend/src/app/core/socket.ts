import { Observable } from 'rxjs';

/** Error emitted when the server closes a socket abnormally. */
export class SocketClosedError extends Error {
  constructor(readonly code: number) {
    super(`socket closed with code ${code}`);
  }
}

/**
 * Opens a same-origin WebSocket and emits each JSON message. The session
 * cookie travels with the handshake; unsubscribing closes the socket.
 */
export function openSocket<T>(path: string): Observable<T> {
  return new Observable<T>((subscriber) => {
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${scheme}//${location.host}${path}`);
    ws.onmessage = (event: MessageEvent<string>) => {
      try {
        subscriber.next(JSON.parse(event.data) as T);
      } catch {
        subscriber.error(new Error('invalid message'));
      }
    };
    ws.onclose = (event: CloseEvent) => {
      if (event.code === 1000) {
        subscriber.complete();
      } else {
        subscriber.error(new SocketClosedError(event.code));
      }
    };
    return () => {
      ws.onmessage = null;
      ws.onclose = null;
      ws.close(1000);
    };
  });
}

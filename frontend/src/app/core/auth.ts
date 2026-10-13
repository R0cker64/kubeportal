import { HttpErrorResponse } from '@angular/common/http';
import { inject, Injectable, signal } from '@angular/core';
import { Api } from './api';
import { Me } from './models';

export type AuthState = 'loading' | 'anonymous' | 'authenticated' | 'error';

/** Session state. The session itself is an HttpOnly cookie the app never sees. */
@Injectable({ providedIn: 'root' })
export class Auth {
  private readonly api = inject(Api);

  readonly state = signal<AuthState>('loading');
  readonly me = signal<Me | null>(null);

  load(): void {
    this.api.me().subscribe({
      next: (me) => {
        this.me.set(me);
        this.state.set('authenticated');
      },
      error: (err: unknown) => {
        this.me.set(null);
        this.state.set(err instanceof HttpErrorResponse && err.status === 401 ? 'anonymous' : 'error');
      },
    });
  }

  login(): void {
    location.assign('/auth/login');
  }

  logout(): void {
    this.api.logout().subscribe({
      next: ({ logoutUrl }) => location.assign(safeRedirect(logoutUrl)),
      error: () => location.assign('/'),
    });
  }
}

/** Only absolute http(s) URLs or the app root are followed after logout. */
export function safeRedirect(url: string | undefined): string {
  if (!url) {
    return '/';
  }
  try {
    const parsed = new URL(url);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.toString() : '/';
  } catch {
    return '/';
  }
}

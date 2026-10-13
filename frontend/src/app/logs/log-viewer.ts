import { DatePipe } from '@angular/common';
import { ChangeDetectionStrategy, Component, DestroyRef, effect, inject, input, signal, untracked } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subscription } from 'rxjs';
import { Api } from '../core/api';
import { LogLine, ServiceRef } from '../core/models';
import { SocketClosedError } from '../core/socket';

export const LOG_RANGES: Record<string, number> = {
  '15m': 15 * 60_000,
  '1h': 60 * 60_000,
  '6h': 6 * 60 * 60_000,
  '24h': 24 * 60 * 60_000,
  '7d': 7 * 24 * 60 * 60_000,
};
export const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const;
/** Lines kept in memory in live mode. */
export const LIVE_BUFFER = 1000;
const MAX_SEARCH = 256;

/** Prepends live lines (newest first) and trims the buffer. */
export function mergeLive(current: LogLine[], incoming: LogLine[], cap = LIVE_BUFFER): LogLine[] {
  return [...[...incoming].reverse(), ...current].slice(0, cap);
}

/**
 * Searches and tails the logs of one service. Lines are always rendered as
 * text through interpolation, never as HTML (CLAUDE.md rule 15).
 */
@Component({
  selector: 'app-log-viewer',
  imports: [DatePipe],
  templateUrl: './log-viewer.html',
  styleUrl: './log-viewer.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogViewer {
  private readonly api = inject(Api);
  private readonly destroyRef = inject(DestroyRef);

  readonly service = input.required<ServiceRef>();

  protected readonly ranges = Object.keys(LOG_RANGES);
  protected readonly levels = LOG_LEVELS;
  protected readonly maxSearch = MAX_SEARCH;

  protected readonly range = signal('1h');
  protected readonly text = signal('');
  protected readonly level = signal('');
  protected readonly lines = signal<LogLine[]>([]);
  protected readonly loading = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly live = signal(false);
  protected readonly truncated = signal(false);

  private request?: Subscription;

  constructor() {
    // Search again whenever the service changes.
    effect(() => {
      this.service();
      untracked(() => {
        this.stopLive();
        this.search();
      });
    });
    this.destroyRef.onDestroy(() => this.request?.unsubscribe());
  }

  protected onText(event: Event): void {
    this.text.set((event.target as HTMLInputElement).value.slice(0, MAX_SEARCH));
  }

  protected onRange(event: Event): void {
    this.range.set((event.target as HTMLSelectElement).value);
  }

  protected onLevel(event: Event): void {
    this.level.set((event.target as HTMLSelectElement).value);
  }

  protected submit(event: Event): void {
    event.preventDefault();
    if (this.live()) {
      this.startLive();
    } else {
      this.search();
    }
  }

  protected toggleLive(): void {
    if (this.live()) {
      this.stopLive();
    } else {
      this.startLive();
    }
  }

  private search(): void {
    this.request?.unsubscribe();
    const end = new Date();
    const start = new Date(end.getTime() - (LOG_RANGES[this.range()] ?? LOG_RANGES['1h']));
    this.loading.set(true);
    this.error.set(null);
    this.request = this.api
      .logs(this.service(), {
        start: start.toISOString().replace(/\.\d+Z$/, 'Z'),
        end: end.toISOString().replace(/\.\d+Z$/, 'Z'),
        q: this.text(),
        level: this.level(),
      })
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe({
        next: (res) => {
          this.lines.set(res.lines);
          this.truncated.set(res.lines.length >= res.limit);
          this.loading.set(false);
        },
        error: () => {
          this.lines.set([]);
          this.loading.set(false);
          this.error.set('No se pudieron cargar los logs. Revisa los filtros e inténtalo de nuevo.');
        },
      });
  }

  private startLive(): void {
    this.request?.unsubscribe();
    this.live.set(true);
    this.error.set(null);
    this.lines.set([]);
    this.truncated.set(false);
    this.request = this.api
      .liveLogs(this.service(), { q: this.text(), level: this.level() })
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe({
        next: (msg) => this.lines.update((cur) => mergeLive(cur, msg.lines)),
        error: (err: unknown) => {
          this.live.set(false);
          this.error.set(
            err instanceof SocketClosedError && err.code === 1008
              ? 'La sesión terminó. Vuelve a iniciar sesión.'
              : 'Se interrumpió el modo en vivo (puede que tengas demasiadas pestañas en vivo abiertas).',
          );
        },
        complete: () => this.live.set(false),
      });
  }

  private stopLive(): void {
    if (this.live()) {
      this.request?.unsubscribe();
      this.live.set(false);
    }
  }
}

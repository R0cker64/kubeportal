import { DatePipe } from '@angular/common';
import { ChangeDetectionStrategy, Component, computed, DestroyRef, inject, input, OnInit, signal } from '@angular/core';
import { takeUntilDestroyed, toObservable } from '@angular/core/rxjs-interop';
import { RouterLink } from '@angular/router';
import { catchError, combineLatest, map, of, switchMap, timer } from 'rxjs';
import { Api } from '../core/api';
import { KIND_LABELS, reasonLabel, STATUS_LABELS } from '../core/labels';
import { MetricsResponse, Series, ServiceDetail, ServiceRef } from '../core/models';
import { LogViewer } from '../logs/log-viewer';
import { ChartLine, MetricChart } from './metric-chart';

const DETAIL_REFRESH_MS = 15_000;
export const METRIC_RANGES = ['15m', '1h', '6h', '24h', '7d'] as const;

function line(series: Series[], name: Series['name'], label: string, kind: ChartLine['kind']): ChartLine {
  return { label, kind, points: series.find((s) => s.name === name)?.points ?? [] };
}

/** Health, pods, events, CPU/RAM and logs of one service. */
@Component({
  selector: 'app-service-detail',
  imports: [DatePipe, RouterLink, MetricChart, LogViewer],
  templateUrl: './service-detail.html',
  styleUrl: './service-detail.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ServiceDetailPage implements OnInit {
  private readonly api = inject(Api);
  private readonly destroyRef = inject(DestroyRef);

  // Route parameters (withComponentInputBinding).
  readonly environment = input.required<string>();
  readonly kind = input.required<string>();
  readonly name = input.required<string>();

  protected readonly statusLabels = STATUS_LABELS;
  protected readonly kindLabels = KIND_LABELS;
  protected readonly reasonLabel = reasonLabel;
  protected readonly ranges = METRIC_RANGES;

  protected readonly ref = computed<ServiceRef>(() => ({
    environment: this.environment(),
    kind: this.kind(),
    name: this.name(),
  }));

  protected readonly detail = signal<ServiceDetail | null>(null);
  protected readonly notFound = signal(false);
  protected readonly metrics = signal<MetricsResponse | null>(null);
  protected readonly metricsError = signal(false);
  protected readonly range = signal<string>('1h');

  protected readonly window = computed(() => {
    const m = this.metrics();
    return m
      ? { start: Date.parse(m.start) / 1000, end: Date.parse(m.end) / 1000 }
      : { start: 0, end: 1 };
  });

  protected readonly cpu = computed(() => {
    const s = this.metrics()?.series ?? [];
    return [
      line(s, 'cpu', 'Uso', 'usage'),
      line(s, 'cpuRequest', 'Request', 'request'),
      line(s, 'cpuLimit', 'Limit', 'limit'),
    ];
  });

  protected readonly memory = computed(() => {
    const s = this.metrics()?.series ?? [];
    return [
      line(s, 'memory', 'Uso', 'usage'),
      line(s, 'memoryRequest', 'Request', 'request'),
      line(s, 'memoryLimit', 'Limit', 'limit'),
    ];
  });

  private readonly ref$ = toObservable(this.ref);
  private readonly range$ = toObservable(this.range);

  ngOnInit(): void {
    this.ref$
      .pipe(
        switchMap((ref) =>
          timer(0, DETAIL_REFRESH_MS).pipe(
            switchMap(() =>
              this.api.service(ref).pipe(
                catchError(() => {
                  this.notFound.set(true);
                  return of(null);
                }),
              ),
            ),
          ),
        ),
        takeUntilDestroyed(this.destroyRef),
      )
      .subscribe((d) => {
        if (d) {
          this.notFound.set(false);
          this.detail.set(d);
        }
      });

    combineLatest([this.ref$, this.range$])
      .pipe(
        switchMap(([ref, range]) =>
          this.api.metrics(ref, range).pipe(
            map((m) => ({ m, error: false })),
            catchError(() => of({ m: null, error: true })),
          ),
        ),
        takeUntilDestroyed(this.destroyRef),
      )
      .subscribe(({ m, error }) => {
        this.metrics.set(m);
        this.metricsError.set(error);
      });
  }
}

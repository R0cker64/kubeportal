import { DatePipe } from '@angular/common';
import { ChangeDetectionStrategy, Component, DestroyRef, inject, OnInit, signal } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { catchError, of, switchMap, timer } from 'rxjs';
import { Api } from '../core/api';
import { NodeView } from '../core/models';
import { formatValue } from '../services/metric-chart';

const REFRESH_MS = 30_000;

/** Percentage of used over total, for <meter>; 0 when total is unknown. */
export function ratio(used: number | undefined, total: number): number {
  return used !== undefined && total > 0 ? Math.min(used / total, 1) : 0;
}

/** Cluster nodes with live usage vs requests vs allocatable capacity. */
@Component({
  selector: 'app-admin-nodes',
  imports: [DatePipe],
  templateUrl: './nodes.html',
  styleUrl: './admin.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AdminNodes implements OnInit {
  private readonly api = inject(Api);
  private readonly destroyRef = inject(DestroyRef);

  protected readonly nodes = signal<NodeView[] | null>(null);
  protected readonly usageAvailable = signal(true);
  protected readonly loadError = signal(false);
  protected readonly ratio = ratio;
  protected readonly format = formatValue;

  ngOnInit(): void {
    timer(0, REFRESH_MS)
      .pipe(
        switchMap(() => this.api.nodes().pipe(catchError(() => of(null)))),
        takeUntilDestroyed(this.destroyRef),
      )
      .subscribe((res) => {
        if (!res) {
          this.loadError.set(true);
          return;
        }
        this.loadError.set(false);
        this.nodes.set(res.nodes);
        this.usageAvailable.set(res.usageAvailable);
      });
  }
}

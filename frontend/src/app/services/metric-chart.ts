import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { Point } from '../core/models';

export interface ChartLine {
  label: string;
  points: Point[];
  /** CSS class: usage, request or limit. */
  kind: 'usage' | 'request' | 'limit';
}

const WIDTH = 600;
const HEIGHT = 160;
const PAD = 4;

/** Formats a value in the chart unit. */
export function formatValue(value: number, unit: 'cores' | 'bytes'): string {
  if (unit === 'cores') {
    return value < 1 ? `${Math.round(value * 1000)}m` : `${value.toFixed(2)} cores`;
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = value;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

/** Builds an SVG polyline "points" attribute scaled to the chart box. */
export function toPolyline(points: Point[], start: number, end: number, max: number): string {
  const span = Math.max(end - start, 1);
  const top = max > 0 ? max : 1;
  return points
    .map(([t, v]) => {
      const x = PAD + ((t - start) / span) * (WIDTH - 2 * PAD);
      const y = HEIGHT - PAD - (v / top) * (HEIGHT - 2 * PAD);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
}

/** Minimal line chart rendered as SVG, compatible with the strict CSP. */
@Component({
  selector: 'app-metric-chart',
  template: `
    <figure class="chart">
      <figcaption>
        <strong>{{ title() }}</strong>
        @for (l of lines(); track l.label) {
          <span class="legend" [class]="l.kind">
            {{ l.label }}: {{ last(l) }}
          </span>
        }
      </figcaption>
      @if (hasData()) {
        <svg [attr.viewBox]="viewBox" preserveAspectRatio="none" role="img" [attr.aria-label]="title()">
          @for (l of scaled(); track l.label) {
            <polyline [attr.points]="l.points" [class]="l.kind" />
          }
        </svg>
        <div class="axis muted"><span>máx. {{ maxLabel() }}</span></div>
      } @else {
        <p class="muted">Sin datos en este rango.</p>
      }
    </figure>
  `,
  styles: `
    .chart {
      margin: 0;
    }
    figcaption {
      display: flex;
      flex-wrap: wrap;
      gap: 0.75rem;
      margin-bottom: 0.25rem;
    }
    svg {
      width: 100%;
      height: 10rem;
      background: var(--kp-code-bg);
      border-radius: 4px;
    }
    polyline {
      fill: none;
      stroke-width: 2;
      vector-effect: non-scaling-stroke;
    }
    .usage {
      stroke: var(--kp-accent);
      color: var(--kp-accent);
    }
    .request {
      stroke: var(--kp-ok);
      color: var(--kp-ok);
      stroke-dasharray: 6 4;
    }
    .limit {
      stroke: var(--kp-bad);
      color: var(--kp-bad);
      stroke-dasharray: 2 4;
    }
    .legend {
      font-size: 0.85rem;
    }
    .axis {
      font-size: 0.8rem;
      text-align: right;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MetricChart {
  readonly title = input.required<string>();
  readonly unit = input.required<'cores' | 'bytes'>();
  readonly lines = input.required<ChartLine[]>();
  /** Range in Unix seconds. */
  readonly start = input.required<number>();
  readonly end = input.required<number>();

  protected readonly viewBox = `0 0 ${WIDTH} ${HEIGHT}`;

  private readonly max = computed(() =>
    Math.max(0, ...this.lines().flatMap((l) => l.points.map(([, v]) => v))) * 1.1,
  );

  protected readonly hasData = computed(() => this.lines().some((l) => l.points.length > 0));

  protected readonly scaled = computed(() =>
    this.lines().map((l) => ({
      label: l.label,
      kind: l.kind,
      points: toPolyline(l.points, this.start(), this.end(), this.max()),
    })),
  );

  protected readonly maxLabel = computed(() => formatValue(this.max(), this.unit()));

  protected last(line: ChartLine): string {
    const p = line.points.at(-1);
    return p ? formatValue(p[1], this.unit()) : '—';
  }
}

import { ChangeDetectionStrategy, Component, computed, DestroyRef, inject, OnInit, signal } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { RouterLink } from '@angular/router';
import { retry, timer } from 'rxjs';
import { Api } from '../core/api';
import { Auth } from '../core/auth';
import { KIND_LABELS, reasonLabel, STATUS_LABELS } from '../core/labels';
import { ServiceSummary } from '../core/models';

/** Services of the user's teams, updated live over a WebSocket. */
@Component({
  selector: 'app-service-list',
  imports: [RouterLink],
  templateUrl: './service-list.html',
  styleUrl: './service-list.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ServiceList implements OnInit {
  private readonly api = inject(Api);
  private readonly destroyRef = inject(DestroyRef);
  protected readonly auth = inject(Auth);

  protected readonly statusLabels = STATUS_LABELS;
  protected readonly kindLabels = KIND_LABELS;
  protected readonly reasonLabel = reasonLabel;

  protected readonly services = signal<ServiceSummary[] | null>(null);
  protected readonly live = signal(false);
  protected readonly environment = signal('');
  protected readonly search = signal('');

  protected readonly environments = computed(() => this.auth.me()?.environments ?? []);

  /** Visible services grouped by environment, in the configured order. */
  protected readonly groups = computed(() => {
    const all = this.services() ?? [];
    const text = this.search().trim().toLowerCase();
    return this.environments()
      .filter((env) => !this.environment() || env === this.environment())
      .map((env) => ({
        environment: env,
        services: all.filter(
          (s) =>
            s.environment === env &&
            (!text || s.name.toLowerCase().includes(text) || s.team.toLowerCase().includes(text)),
        ),
      }))
      .filter((g) => g.services.length > 0);
  });

  protected readonly counts = computed(() => {
    const all = this.services() ?? [];
    return {
      total: all.length,
      degraded: all.filter((s) => s.health.status === 'degraded').length,
      down: all.filter((s) => s.health.status === 'down').length,
    };
  });

  ngOnInit(): void {
    this.api
      .watchServices()
      .pipe(
        retry({
          delay: () => {
            this.live.set(false);
            return timer(5000);
          },
        }),
        takeUntilDestroyed(this.destroyRef),
      )
      .subscribe((msg) => {
        this.services.set(msg.services);
        this.live.set(true);
      });
  }

  protected setSearch(event: Event): void {
    this.search.set((event.target as HTMLInputElement).value);
  }
}

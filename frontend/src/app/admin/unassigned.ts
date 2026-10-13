import { ChangeDetectionStrategy, Component, inject, OnInit, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { Api } from '../core/api';
import { STATUS_LABELS } from '../core/labels';
import { ServiceSummary } from '../core/models';

interface Section {
  title: string;
  hint: string;
  items: ServiceSummary[];
}

/** Workloads only an administrator can see: no team label, or an unregistered team. */
@Component({
  selector: 'app-admin-unassigned',
  imports: [RouterLink],
  templateUrl: './unassigned.html',
  styleUrl: './admin.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AdminUnassigned implements OnInit {
  private readonly api = inject(Api);

  protected readonly statusLabels = STATUS_LABELS;
  protected readonly sections = signal<Section[] | null>(null);
  protected readonly loadError = signal(false);

  ngOnInit(): void {
    this.api.unassigned().subscribe({
      next: (res) =>
        this.sections.set([
          {
            title: 'Sin label team',
            hint: 'Solo los ve el administrador. Añade el label team al workload y al template de sus pods.',
            items: res.unlabelled,
          },
          {
            title: 'Con un equipo no registrado',
            hint: 'Tienen label team, pero ningún equipo con ese identificador: créalo en Equipos.',
            items: res.unknownTeam,
          },
        ]),
      error: () => this.loadError.set(true),
    });
  }
}

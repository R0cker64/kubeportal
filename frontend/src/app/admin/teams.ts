import { HttpErrorResponse } from '@angular/common/http';
import { ChangeDetectionStrategy, Component, inject, OnInit, signal } from '@angular/core';
import { Api } from '../core/api';
import { Team } from '../core/models';

/** Splits a free-text list of groups (commas or new lines) into names. */
export function parseGroups(text: string): string[] {
  return [...new Set(text.split(/[,\n]/).map((g) => g.trim()).filter((g) => g !== ''))];
}

/** Spanish message for a failed team request. */
export function teamErrorMessage(err: unknown): string {
  const status = err instanceof HttpErrorResponse ? err.status : 0;
  switch (status) {
    case 400:
      return 'Datos no válidos. El identificador debe ser minúsculas, números y guiones (máx. 63) y coincidir con el label team; los grupos, sin saltos de línea.';
    case 404:
      return 'El equipo ya no existe.';
    case 409:
      return 'Ya existe un equipo con ese identificador.';
    case 403:
      return 'No tienes permisos de administración.';
    default:
      return 'No se pudo guardar. Inténtalo de nuevo.';
  }
}

/** Team management: the group -> team mapping that drives authorization. */
@Component({
  selector: 'app-admin-teams',
  templateUrl: './teams.html',
  styleUrl: './admin.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AdminTeams implements OnInit {
  private readonly api = inject(Api);

  protected readonly teams = signal<Team[] | null>(null);
  protected readonly adminGroup = signal('');
  protected readonly loadError = signal(false);

  /** Name of the team being edited; '' when creating. */
  protected readonly editing = signal<string | null>(null);
  protected readonly name = signal('');
  protected readonly displayName = signal('');
  protected readonly groups = signal('');
  protected readonly saving = signal(false);
  protected readonly message = signal<{ text: string; error: boolean } | null>(null);

  ngOnInit(): void {
    this.load();
  }

  private load(): void {
    this.api.teams().subscribe({
      next: (res) => {
        this.teams.set(res.teams);
        this.adminGroup.set(res.adminGroup);
        this.loadError.set(false);
      },
      error: () => this.loadError.set(true),
    });
  }

  protected startCreate(): void {
    this.editing.set('');
    this.name.set('');
    this.displayName.set('');
    this.groups.set('');
    this.message.set(null);
  }

  protected startEdit(team: Team): void {
    this.editing.set(team.name);
    this.name.set(team.name);
    this.displayName.set(team.displayName);
    this.groups.set(team.groups.join('\n'));
    this.message.set(null);
  }

  protected cancel(): void {
    this.editing.set(null);
  }

  protected input(target: 'name' | 'displayName' | 'groups', event: Event): void {
    const value = (event.target as HTMLInputElement | HTMLTextAreaElement).value;
    this[target].set(value);
  }

  protected save(event: Event): void {
    event.preventDefault();
    const editing = this.editing();
    if (editing === null) {
      return;
    }
    const body = { displayName: this.displayName().trim(), groups: parseGroups(this.groups()) };
    const request =
      editing === ''
        ? this.api.createTeam({ name: this.name().trim(), ...body })
        : this.api.updateTeam(editing, body);
    this.saving.set(true);
    request.subscribe({
      next: (team) => {
        this.saving.set(false);
        this.editing.set(null);
        this.message.set({ text: `Equipo «${team.name}» guardado. Sus miembros ya ven sus servicios.`, error: false });
        this.load();
      },
      error: (err: unknown) => {
        this.saving.set(false);
        this.message.set({ text: teamErrorMessage(err), error: true });
      },
    });
  }

  protected remove(team: Team): void {
    if (!confirm(`¿Eliminar el equipo «${team.name}»? Sus miembros dejarán de ver sus servicios.`)) {
      return;
    }
    this.api.deleteTeam(team.name).subscribe({
      next: () => {
        this.message.set({ text: `Equipo «${team.name}» eliminado.`, error: false });
        this.load();
      },
      error: (err: unknown) => this.message.set({ text: teamErrorMessage(err), error: true }),
    });
  }
}

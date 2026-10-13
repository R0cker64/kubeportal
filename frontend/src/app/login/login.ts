import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { Auth } from '../core/auth';

/** Login screen. A failed login comes back as /?error=login&ref=<request id>. */
@Component({
  selector: 'app-login',
  template: `
    <section class="card login">
      <h1>Iniciar sesión</h1>
      <p>Accede con tu cuenta corporativa para ver los logs y la salud de los servicios de tu equipo.</p>
      @if (errorRef) {
        <p class="error" role="alert">
          No se pudo iniciar sesión. Si el problema persiste, comparte esta referencia con el administrador:
          <code>{{ errorRef }}</code>
        </p>
      }
      <button type="button" class="button primary" (click)="auth.login()">Entrar</button>
    </section>
  `,
  styles: `
    .login {
      max-width: 28rem;
      margin: 4rem auto;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Login {
  protected readonly auth = inject(Auth);
  protected readonly errorRef = loginErrorRef(location.search);
}

/** Extracts the correlation id of a failed login, accepting only hex ids. */
export function loginErrorRef(search: string): string | null {
  const params = new URLSearchParams(search);
  if (params.get('error') !== 'login') {
    return null;
  }
  const ref = params.get('ref') ?? '';
  return /^[0-9a-f]{1,64}$/.test(ref) ? ref : 'desconocida';
}

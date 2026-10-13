import { ChangeDetectionStrategy, Component } from '@angular/core';

@Component({
  selector: 'app-home',
  template: `<p>Bienvenido. El inicio de sesión llegará en la fase 2.</p>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Home {}

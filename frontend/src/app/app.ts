import { ChangeDetectionStrategy, Component, inject, OnInit } from '@angular/core';
import { RouterLink, RouterOutlet } from '@angular/router';
import { Auth } from './core/auth';
import { Login } from './login/login';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, Login],
  templateUrl: './app.html',
  styleUrl: './app.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App implements OnInit {
  protected readonly productName = 'KubePortal';
  protected readonly auth = inject(Auth);

  ngOnInit(): void {
    this.auth.load();
  }
}

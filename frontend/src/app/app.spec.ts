import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { App } from './app';

describe('App', () => {
  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [App],
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
  });

  it('renders the product name and shows the login screen without a session', async () => {
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne('/api/me').flush(null, { status: 401, statusText: 'Unauthorized' });
    await fixture.whenStable();
    const compiled = fixture.nativeElement as HTMLElement;
    expect(compiled.querySelector('.brand')?.textContent).toContain('KubePortal');
    expect(compiled.textContent).toContain('Iniciar sesión');
    http.verify();
  });

  it('tells users without a team that they see nothing', async () => {
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne('/api/me').flush({ username: 'carol', teams: [], admin: false, environments: ['dev'] });
    await fixture.whenStable();
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Sin equipo asignado');
  });
});

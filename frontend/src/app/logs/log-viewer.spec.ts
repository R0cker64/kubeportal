import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { LogLine } from '../core/models';
import { LogViewer, mergeLive } from './log-viewer';

describe('LogViewer', () => {
  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [LogViewer],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
  });

  it('queries the backend with parameters only and renders lines as text', async () => {
    const fixture = TestBed.createComponent(LogViewer);
    fixture.componentRef.setInput('service', { environment: 'prod', kind: 'deployment', name: 'payments-api' });
    fixture.detectChanges();

    const http = TestBed.inject(HttpTestingController);
    const req = http.expectOne((r) => r.url === '/api/services/prod/deployment/payments-api/logs');
    expect(req.request.params.has('query')).toBe(false);
    expect(req.request.params.get('start')).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
    req.flush({
      start: '',
      end: '',
      limit: 500,
      lines: [{ time: '2026-10-13T10:00:00Z', pod: 'p1', level: 'error', line: '<img src=x onerror=alert(1)>' }],
    });
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('select')?.value).toBe('1h');
    expect(el.querySelector('img')).toBeNull();
    expect(el.querySelector('.text')?.textContent).toBe('<img src=x onerror=alert(1)>');
    http.verify();
  });
});

describe('mergeLive', () => {
  const l = (n: number): LogLine => ({ time: String(n), pod: 'p', line: String(n) });

  it('puts the newest lines first and caps the buffer', () => {
    expect(mergeLive([l(2), l(1)], [l(3), l(4)]).map((x) => x.line)).toEqual(['4', '3', '2', '1']);
    expect(mergeLive([l(1)], [l(2), l(3)], 2).map((x) => x.line)).toEqual(['3', '2']);
  });
});

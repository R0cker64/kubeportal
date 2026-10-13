import { HttpClient, provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { loginErrorRef } from '../login/login';
import { safeRedirect } from './auth';
import { CSRF_HEADER, CSRF_VALUE, csrfInterceptor } from './csrf.interceptor';
import { reasonLabel } from './labels';

describe('csrfInterceptor', () => {
  it('adds the CSRF header to same-origin requests only', () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(withInterceptors([csrfInterceptor])), provideHttpClientTesting()],
    });
    const http = TestBed.inject(HttpClient);
    const ctrl = TestBed.inject(HttpTestingController);

    http.post('/auth/logout', null).subscribe();
    expect(ctrl.expectOne('/auth/logout').request.headers.get(CSRF_HEADER)).toBe(CSRF_VALUE);

    http.get('https://elsewhere.example.com/x').subscribe();
    expect(ctrl.expectOne('https://elsewhere.example.com/x').request.headers.has(CSRF_HEADER)).toBe(false);
    ctrl.verify();
  });
});

describe('safeRedirect', () => {
  it('only follows http(s) URLs', () => {
    expect(safeRedirect('https://sso.example.com/logout?x=1')).toBe('https://sso.example.com/logout?x=1');
    expect(safeRedirect('javascript:alert(1)')).toBe('/');
    expect(safeRedirect('data:text/html,x')).toBe('/');
    expect(safeRedirect('')).toBe('/');
    expect(safeRedirect(undefined)).toBe('/');
  });
});

describe('loginErrorRef', () => {
  it('accepts only hex correlation ids', () => {
    expect(loginErrorRef('?error=login&ref=abc123')).toBe('abc123');
    expect(loginErrorRef('?error=login&ref=<img src=x>')).toBe('desconocida');
    expect(loginErrorRef('?foo=bar')).toBeNull();
  });
});

describe('reasonLabel', () => {
  it('translates known codes and passes unknown ones through', () => {
    expect(reasonLabel('crash_loop_back_off')).toContain('CrashLoopBackOff');
    expect(reasonLabel('something_new')).toBe('something_new');
  });
});

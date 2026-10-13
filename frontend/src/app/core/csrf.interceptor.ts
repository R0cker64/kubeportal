import { HttpInterceptorFn } from '@angular/common/http';

/** Header the backend requires on state-changing requests (CLAUDE.md rule 7). */
export const CSRF_HEADER = 'X-Requested-With';
export const CSRF_VALUE = 'kubeportal';

/** Adds the CSRF header to every same-origin API call. */
export const csrfInterceptor: HttpInterceptorFn = (req, next) => {
  if (/^https?:\/\//i.test(req.url)) {
    // The frontend only talks to its own origin (CLAUDE.md rule 17).
    return next(req);
  }
  return next(req.clone({ setHeaders: { [CSRF_HEADER]: CSRF_VALUE } }));
};

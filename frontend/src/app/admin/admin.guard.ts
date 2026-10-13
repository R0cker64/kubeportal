import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { Auth } from '../core/auth';

/**
 * Hides the admin pages from non-admins. This is only UX: the backend
 * enforces the admin check on every /api/admin request.
 */
export const adminGuard: CanActivateFn = () =>
  inject(Auth).me()?.admin === true || inject(Router).parseUrl('/');

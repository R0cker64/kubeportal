import { HttpClient, HttpParams } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';
import { Observable } from 'rxjs';
import {
  LogLine,
  LogsResponse,
  Me,
  MetricsResponse,
  ServiceDetail,
  ServiceRef,
  ServiceSummary,
} from './models';
import { openSocket } from './socket';

export interface LogQuery {
  start?: string;
  end?: string;
  q?: string;
  level?: string;
  limit?: number;
}

function servicePath(ref: ServiceRef): string {
  const parts = [ref.environment, ref.kind, ref.name].map(encodeURIComponent);
  return `/api/services/${parts.join('/')}`;
}

function params(values: Record<string, string | number | undefined>): HttpParams {
  let p = new HttpParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== '') {
      p = p.set(key, String(value));
    }
  }
  return p;
}

/** Client for the portal backend. All URLs are same-origin. */
@Injectable({ providedIn: 'root' })
export class Api {
  private readonly http = inject(HttpClient);

  me(): Observable<Me> {
    return this.http.get<Me>('/api/me');
  }

  logout(): Observable<{ logoutUrl: string }> {
    return this.http.post<{ logoutUrl: string }>('/auth/logout', null);
  }

  services(): Observable<{ services: ServiceSummary[] }> {
    return this.http.get<{ services: ServiceSummary[] }>('/api/services');
  }

  watchServices(): Observable<{ services: ServiceSummary[] }> {
    return openSocket<{ services: ServiceSummary[] }>('/api/services/watch');
  }

  service(ref: ServiceRef): Observable<ServiceDetail> {
    return this.http.get<ServiceDetail>(servicePath(ref));
  }

  metrics(ref: ServiceRef, range: string): Observable<MetricsResponse> {
    return this.http.get<MetricsResponse>(servicePath(ref) + '/metrics', { params: params({ range }) });
  }

  logs(ref: ServiceRef, query: LogQuery): Observable<LogsResponse> {
    return this.http.get<LogsResponse>(servicePath(ref) + '/logs', { params: params({ ...query }) });
  }

  liveLogs(ref: ServiceRef, query: Pick<LogQuery, 'q' | 'level'>): Observable<{ lines: LogLine[] }> {
    const qs = params({ ...query }).toString();
    return openSocket<{ lines: LogLine[] }>(servicePath(ref) + '/logs/live' + (qs ? '?' + qs : ''));
  }
}

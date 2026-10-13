// API contracts with the backend (internal/httpapi). Keep in sync.

export interface Me {
  username: string;
  teams: string[];
  admin: boolean;
  environments: string[];
}

export type HealthStatus = 'healthy' | 'degraded' | 'down';

export interface Health {
  status: HealthStatus;
  desired: number;
  ready: number;
  restarts: number;
  reasons: string[];
}

export interface ServiceSummary {
  environment: string;
  kind: 'deployment' | 'statefulset';
  name: string;
  team: string;
  app: string;
  health: Health;
}

export interface ContainerInfo {
  name: string;
  ready: boolean;
  restarts: number;
  state: string;
  reason?: string;
  lastTerminationReason?: string;
  lastTerminatedAt?: string;
}

export interface PodInfo {
  name: string;
  phase: string;
  ready: boolean;
  restarts: number;
  node?: string;
  startedAt?: string;
  containers: ContainerInfo[];
}

export interface ServiceEvent {
  reason: string;
  message: string;
  object: string;
  count: number;
  lastSeen: string;
}

export interface ServiceDetail extends ServiceSummary {
  createdAt: string;
  pods: PodInfo[];
  events: ServiceEvent[];
}

export type Point = [number, number];

export interface Series {
  name: 'cpu' | 'cpuRequest' | 'cpuLimit' | 'memory' | 'memoryRequest' | 'memoryLimit';
  points: Point[];
}

export interface MetricsResponse {
  start: string;
  end: string;
  series: Series[];
}

export interface LogLine {
  time: string;
  pod: string;
  level?: string;
  line: string;
}

export interface LogsResponse {
  start: string;
  end: string;
  limit: number;
  lines: LogLine[];
}

export interface ServiceRef {
  environment: string;
  kind: string;
  name: string;
}

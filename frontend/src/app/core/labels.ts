import { HealthStatus } from './models';

// Spanish labels for the stable codes returned by the backend.

export const STATUS_LABELS: Record<HealthStatus, string> = {
  healthy: 'Sano',
  degraded: 'Degradado',
  down: 'Caído',
};

const REASON_LABELS: Record<string, string> = {
  no_ready_replicas: 'Ninguna réplica lista',
  replicas_not_ready: 'Réplicas sin estar listas',
  crash_loop_back_off: 'Reinicios en bucle (CrashLoopBackOff)',
  image_pull_error: 'Error al descargar la imagen',
  config_error: 'Error de configuración del contenedor',
  oom_killed: 'Terminado por falta de memoria (OOMKilled)',
  pending: 'Pods pendientes de programar',
  recent_restarts: 'Reinicios recientes',
  pod_failed: 'Pods fallidos',
  rollout_in_progress: 'Despliegue en curso',
};

export function reasonLabel(code: string): string {
  return REASON_LABELS[code] ?? code;
}

export const KIND_LABELS: Record<string, string> = {
  deployment: 'Deployment',
  statefulset: 'StatefulSet',
};

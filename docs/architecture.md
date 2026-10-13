# Arquitectura

KubePortal es un portal web donde cada equipo ve solo los logs y la salud de sus servicios. El backend en Go es el único componente que habla con Kubernetes, Loki, Prometheus y Keycloak, y aplica el filtro por equipo en cada consulta.

## Componentes

```
                 ┌────────────────────┐
                 │      Keycloak      │
                 │  (OIDC, grupos)    │
                 └─────────▲──────────┘
       login (redirect)    │ code → tokens (servidor a servidor)
┌──────────────┐           │
│  Navegador   │  cookie   │
│  (Angular)   ├──────────►┌────────────────────────────────────────┐
└──────────────┘  mismo    │          Backend Go (BFF)              │
                  origen   │  - sesiones y OIDC                     │
                           │  - authz: grupos → equipos → filtros   │
                           │  - API REST + WebSocket                │
                           │  - sirve los estáticos de Angular      │
                           └───┬──────────┬───────────┬─────────┬───┘
                               │          │           │         │
                   solo lectura│     LogQL│      PromQL│      SQL│
                               ▼          ▼           ▼         ▼
                         API de K8s     Loki    Prometheus  PostgreSQL
```

| Componente | Responsabilidad | Expuesto a |
|---|---|---|
| Backend Go | Autenticación (BFF), autorización, consultas, API y estáticos | Usuarios (v1: `kubectl port-forward`) |
| Frontend Angular | UI; solo habla con el backend, mismo origen | Servido por el backend |
| Keycloak | Identidad, grupos | Navegador (login) y backend |
| PostgreSQL | Equipos, mapeo grupo → equipo, sesiones | Solo backend |
| Loki, Prometheus | Fuentes existentes del cluster | Solo backend (NetworkPolicy) |

## Modelo de permisos

Los namespaces son por ambiente (`dev`, `staging`, `prod`) y mezclan apps de varios equipos. Por eso el RBAC de Kubernetes no puede separar equipos y el permiso se basa en el label `team` de cada workload (ver ADR 0003).

1. El usuario inicia sesión en Keycloak. El backend recibe los tokens (nunca llegan al navegador) y lee el claim `groups`.
2. `internal/authz` traduce grupos a equipos con la tabla de mapeo en PostgreSQL.
3. Toda consulta se construye en el servidor con `team ∈ equipos del usuario`.
4. Cada equipo ve sus apps en todos los ambientes.

| Rol | Ve | Puede |
|---|---|---|
| Miembro de equipo | Workloads, logs y métricas con `team` = su equipo | Solo leer |
| Administrador (grupo `kubeportal-admin`) | Todo el cluster, incluidos nodos y workloads sin label | Gestionar el mapeo grupo → equipo |

## Módulo de logs

- La UI envía: ambiente, app (de una lista ya filtrada), rango de tiempo, texto, nivel.
- `authz` genera el selector; `logs` arma la consulta:

  ```
  {team="pagos", namespace="prod", app="api-pagos"} |= "timeout"
  ```

- El texto se escapa como literal; nunca se concatena LogQL del cliente.
- Límites configurables: rango máximo (7 días por defecto, hasta confirmar la retención de Loki), líneas máximas por consulta.
- Modo en vivo: el backend usa `tail` de Loki y reenvía las líneas por WebSocket aplicando el mismo filtro.

## Módulo de salud

Estado por servicio (Sano, Degradado, Caído) a partir de:

| Señal | Fuente |
|---|---|
| Réplicas listas vs deseadas, reinicios, CrashLoopBackOff, OOMKilled, Pending | Informers de Kubernetes |
| Eventos Warning | Informers de Kubernetes |
| CPU y RAM vs requests/limits, con histórico | Prometheus (cAdvisor + kube-state-metrics) |

Los cambios llegan por informers y se envían a Angular por WebSocket, sin polling al API server.

## Administración (v1)

- **Nodos:** estado y condiciones, node pool, zona, versión de kubelet, CPU/RAM real vs requests vs capacidad asignable, pods por nodo.
- **Equipos:** alta de equipos con su valor de label, mapeo de grupos de Keycloak, lista de workloads sin label `team`.

## Prerrequisitos en el cluster

- Label `team` en Deployments y en el template de sus pods (con Kustomize: `labels` con `includeTemplates: true`; no usar `commonLabels`, cambia los selectores).
- El colector de logs (Promtail/Alloy) copia el label `team` del pod como label de Loki.
- kube-state-metrics con `--metric-labels-allowlist=pods=[team],deployments=[team]`.
- Keycloak instalado (no existe aún) con realm, client confidencial y grupos.
- Confirmar la retención de Loki (`limits_config.retention_period` y `compactor.retention_enabled`).

## Despliegue

| Etapa | Acceso |
|---|---|
| Desarrollo | Cluster kind local con todas las dependencias y apps de ejemplo |
| v1 en el cluster real | Chart de Helm vía Flux; acceso con `kubectl port-forward` |
| Futuro | Exposición con dominio y TLS (Gateway/Ingress o Cloudflare Tunnel) |

Keycloak tendrá un hostname fijo configurado (`KC_HOSTNAME`). El backend valida el `iss` contra esa URL pública aunque descargue las llaves (JWKS) por la URL interna del Service. Así funciona igual con port-forward y con dominio.

## Escala prevista

1 a 3 equipos y menos de 20 apps. Un solo réplica del backend, sin caché ni colas. No optimizar antes de tener la necesidad.

## Fuera de la v1

Acciones de usuario (reiniciar, escalar, exec), métricas HTTP (errores, latencia), dashboards personalizables, multi-cluster con agente, aviso de recursos gestionados por Flux, audit log completo, Cloudflare Tunnel.

# Roadmap

Cada fase se cierra solo cuando se cumple su criterio de cierre.

| Fase | Contenido | Criterio de cierre |
|---|---|---|
| 0. Preparación | Label `team` en las apps; colector de Loki y kube-state-metrics propagando `team`; confirmar retención de Loki | `{team="<equipo>"}` devuelve logs en Loki y `kube_pod_labels{label_team="<equipo>"}` devuelve pods |
| 1. Esqueleto | Repo Go + Angular + Helm; entorno kind con Loki, Prometheus, Keycloak y apps demo; CI con lint, tests y escaneos de seguridad | `make dev-up` levanta todo desde cero y el CI pasa en verde |
| 2. MVP de lectura | Login BFF con Keycloak; lista de servicios del equipo; salud; logs con búsqueda y modo en vivo | Tests de aislamiento entre equipos pasan en todos los endpoints |
| 3. Administración | Vista de nodos; gestión de equipos y mapeo de grupos; workloads sin label | Un admin crea un equipo y sus miembros ven sus apps sin tocar el cluster |
| 4. Publicación | Chart instalado en el cluster real vía Flux; README, capturas, demo; repo público | Instalación con un solo `helm install` y documentación completa |

## Después de la v1

- Exposición con dominio y TLS (Gateway/Ingress o Cloudflare Tunnel)
- Acciones: reiniciar y escalar servicios (con aviso de recursos gestionados por Flux)
- Métricas HTTP (tasa de errores, latencia)
- Dashboards personalizables
- Audit log completo
- Multi-cluster con agente

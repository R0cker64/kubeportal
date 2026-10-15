# Architecture Decision Records

Una decisión de arquitectura por archivo, numerada. Un ADR aceptado no se edita: si la decisión cambia, se escribe uno nuevo que lo reemplaza.

Formato: Contexto · Decisión · Consecuencias · Estado.

| # | Decisión | Estado |
|---|---|---|
| [0001](0001-web-app-with-central-backend.md) | App web con backend central | Aceptada |
| [0002](0002-go-backend-angular-frontend.md) | Go en el backend, Angular en el frontend | Aceptada |
| [0003](0003-team-label-authorization.md) | Autorización por label `team` aplicada en el backend | Aceptada |
| [0004](0004-keycloak-bff-sessions.md) | Keycloak con patrón BFF (tokens solo en el servidor) | Aceptada |
| [0005](0005-read-only-v1.md) | Usuarios de solo lectura en la v1 | Aceptada |
| [0006](0006-proxy-loki-prometheus.md) | Proxy a Loki y Prometheus en vez de embeber Grafana | Aceptada |
| [0007](0007-kind-local-development.md) | Desarrollo local con kind | Aceptada |
| [0008](0008-csp-nonce-for-angular-styles.md) | Nonce de CSP para los estilos de componentes de Angular | Aceptada |
| [0009](0009-sessions-without-persisted-tokens.md) | Sesiones del BFF sin tokens persistidos | Aceptada |
| [0010](0010-release-ghcr-cosign.md) | Publicación en GHCR firmada con cosign keyless | Aceptada |
| [0011](0011-keycloak-operator-cloud-sql.md) | Keycloak con el operador oficial y Cloud SQL por IP privada | Aceptada |

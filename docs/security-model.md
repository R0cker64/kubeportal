# Modelo de seguridad

El riesgo principal es que un usuario vea datos de otro equipo. El segundo es que la app se convierta en una puerta de entrada al cluster. Todo el diseño apunta a reducir esos dos riesgos.

## Activos

| Activo | Por qué importa |
|---|---|
| Logs de las aplicaciones | Pueden contener datos personales, IDs de clientes o errores con información interna |
| Credenciales del ServiceAccount del backend | Dan lectura a todo el cluster |
| Sesiones de usuario | Permiten actuar como el usuario |
| Mapeo grupo → equipo | Si se altera, cambia quién ve qué |

## Amenazas y controles

| # | Amenaza | Control |
|---|---|---|
| T1 | Usuario del equipo A pide datos del equipo B cambiando parámetros | `internal/authz` es el único que construye filtros; validación por allowlist; tests de aislamiento obligatorios por endpoint |
| T2 | Inyección de LogQL/PromQL desde la búsqueda | El cliente nunca envía consultas; el texto se escapa como literal; longitud máxima |
| T3 | Robo de tokens por XSS | Patrón BFF: tokens solo en el servidor; cookie `HttpOnly`; CSP estricta; Angular sin `innerHTML` ni `bypassSecurityTrust*` |
| T4 | CSRF | Cookie `SameSite=Strict` + cabecera personalizada obligatoria en peticiones que cambian estado |
| T5 | Secuestro de WebSocket entre sitios | Validación de `Origin` y de la sesión al conectar |
| T6 | Tokens falsificados o expirados | Validación de firma (JWKS), `iss`, `aud`, `exp`, `nbf`; allowlist de algoritmos |
| T7 | App comprometida usada para atacar el cluster | ServiceAccount solo lectura, sin Secrets ni ConfigMaps; imagen sin shell; pod `restricted`; NetworkPolicy de salida limitada |
| T8 | Denegación de servicio sobre Loki/Prometheus | Rango máximo, líneas máximas, timeouts, límite de conexiones de tail por usuario |
| T9 | Dependencia vulnerable o maliciosa | Dependencias mínimas; lockfiles; govulncheck, npm audit, trivy; Dependabot; Actions fijadas por SHA |
| T10 | Secretos filtrados en el repositorio | `.gitignore`, gitleaks en CI y pre-commit, solo `.env.example` |
| T11 | Filtración de información en errores | Mensajes genéricos al cliente con id de correlación; detalle solo en logs del servidor |
| T12 | Tokens o datos sensibles en los logs del backend | Prohibido registrar `Authorization`, cookies y tokens; revisión en code review |

## Controles de la cadena de CI (cuando exista el código)

| Herramienta | Qué revisa | Bloquea el merge |
|---|---|---|
| golangci-lint (incluye gosec) | Errores y patrones inseguros en Go | Sí |
| govulncheck | Vulnerabilidades en dependencias Go realmente usadas | Sí |
| eslint + `npm audit --audit-level=high` | Frontend y dependencias npm | Sí |
| gitleaks | Secretos en el código y el historial | Sí |
| trivy (imagen y config) | CVEs en la imagen; malas configuraciones en Helm | Sí, con severidad HIGH o CRITICAL |
| CodeQL | Análisis estático de Go y TypeScript | Sí |
| Dependabot | Actualizaciones semanales de Go, npm, Docker y Actions | No (abre PRs) |

## Endurecimiento del despliegue

- Imagen distroless `nonroot`, base fijada por digest, SBOM generado en CI.
- Pod Security Standard `restricted`; `automountServiceAccountToken: false` en todo pod que no sea el backend.
- NetworkPolicy: denegación por defecto; el backend solo sale hacia API server, Loki, Prometheus, Keycloak, PostgreSQL y DNS.
- PostgreSQL sin exposición fuera del namespace; credenciales en Secret.

## Riesgos aceptados en la v1

- Acceso por `kubectl port-forward`: requiere que el usuario tenga permiso de port-forward en el cluster. Se acepta mientras los usuarios sean pocos; se elimina al exponer con dominio.
- El backend tiene lectura de todo el cluster (excepto Secrets y ConfigMaps) y el aislamiento entre equipos depende de su código. Se mitiga con `authz` centralizado y tests de aislamiento.

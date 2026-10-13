# CLAUDE.md

Guía para Claude Code al trabajar en este repositorio. Léela completa antes de cualquier cambio.

## Qué es este proyecto

KubePortal (nombre provisional) es un portal web de autoservicio para Kubernetes. Cada equipo inicia sesión y ve **solo** los logs y la salud de sus servicios, sin acceso a Grafana, `kubectl` ni kubeconfig. El administrador ve además los nodos y gestiona qué grupo de usuarios corresponde a qué equipo.

Documentación de diseño (fuente de verdad, actualizarla si una decisión cambia):

- `docs/architecture.md` — componentes, flujos y alcance de la v1
- `docs/security-model.md` — modelo de amenazas y controles obligatorios
- `docs/roadmap.md` — fases y criterios de cierre
- `docs/adr/` — decisiones de arquitectura; una decisión nueva o revertida = un ADR nuevo

## Stack

| Capa | Tecnología |
|---|---|
| Backend | Go (última estable), `client-go` con informers, `net/http` estándar |
| Frontend | Angular (última estable) + TypeScript estricto |
| Autenticación | Keycloak (OIDC). El backend actúa como BFF: los tokens nunca llegan al navegador |
| Logs | Loki (API HTTP, LogQL) |
| Métricas | Prometheus (API HTTP, PromQL) + kube-state-metrics |
| Persistencia | PostgreSQL |
| Empaquetado | Imagen distroless nonroot + chart de Helm |
| Desarrollo local | kind (cluster local con Loki, Prometheus, Keycloak y apps de ejemplo) |

## Estructura del repositorio

```
backend/                Módulo Go
  cmd/server/           Punto de entrada (solo wiring, sin lógica)
  internal/config/      Carga y validación de configuración (falla al arrancar si falta algo)
  internal/auth/        OIDC con Keycloak, sesiones del BFF, cookies
  internal/authz/       ÚNICO lugar que decide qué equipos ve un usuario y construye filtros
  internal/k8s/         Informers y lectura de la API de Kubernetes
  internal/logs/        Cliente de Loki (consultas construidas a partir de authz)
  internal/metrics/     Cliente de Prometheus (consultas construidas a partir de authz)
  internal/store/       PostgreSQL (equipos, mapeo de grupos, sesiones)
  internal/httpapi/     Handlers, middleware, cabeceras de seguridad, WebSocket
frontend/               Aplicación Angular
deploy/helm/kubeportal/ Chart de Helm
deploy/kind/            Entorno local: config de kind, values de Loki/Prometheus/Keycloak, apps demo
docs/                   Diseño, seguridad, ADRs
```

## Comandos

`make help` lista todos los objetivos. Detalle del entorno local en `docs/development.md`.

```
make dev-up        # crea el cluster kind con todas las dependencias (kubeconfig propio en .kind/)
make dev-down      # elimina el cluster kind
make dev-verify    # comprueba el label team en Loki y kube-state-metrics
make dev-deploy    # construye la imagen y la instala en kind con el chart
make backend-run   # corre el backend en local (lee .env si existe)
make frontend-run  # ng serve con proxy al backend
make test          # tests de backend y frontend
make lint          # golangci-lint + eslint + helm lint
make security      # govulncheck + npm audit + gitleaks + trivy
make image         # construye la imagen
```

## Reglas de seguridad (no negociables)

El objetivo es un proyecto con muy pocas vulnerabilidades desde el inicio. Si una tarea choca con alguna de estas reglas, detente y pregunta; no la rompas "temporalmente".

### Autorización y aislamiento entre equipos
1. Toda consulta a Kubernetes, Loki o Prometheus que devuelva datos de un equipo se construye en `internal/authz`. Ningún handler arma selectores, LogQL ni PromQL por su cuenta.
2. Denegar por defecto: un usuario sin equipo mapeado no ve nada; un workload sin label `team` solo lo ve el administrador.
3. El cliente nunca envía LogQL, PromQL ni label selectors. Envía parámetros (ambiente, app, rango, texto) que se validan con allowlists:
   - Nombres de Kubernetes: regex RFC 1123, longitud máxima 63.
   - Ambientes: deben estar en la lista configurada.
   - Texto de búsqueda: se escapa como literal de LogQL; longitud máxima configurada.
   - Rangos de tiempo: límite máximo configurado (por defecto 7 días).
4. Cada endpoint nuevo que devuelva datos de equipo lleva un test de aislamiento: un usuario del equipo A no obtiene nada del equipo B.

### Autenticación y sesiones
5. Patrón BFF: el backend hace el flujo Authorization Code + PKCE como client confidencial. El navegador solo recibe una cookie de sesión `HttpOnly`, `Secure`, `SameSite=Strict`, con prefijo `__Host-`. Nunca guardar tokens en `localStorage` ni `sessionStorage`.
6. Validar en el backend: firma (JWKS), `iss`, `aud`, `exp`, `nbf` y algoritmo en allowlist (RS256/ES256). Nunca aceptar `alg: none`.
7. Las peticiones que cambian estado requieren protección CSRF (SameSite + cabecera personalizada verificada). Los WebSocket validan `Origin` y la sesión al conectar.

### Backend Go
8. El servidor HTTP siempre con timeouts (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) y límite de tamaño de body.
9. Toda llamada saliente (Kubernetes, Loki, Prometheus, Keycloak) con `context` con timeout.
10. Cabeceras de seguridad en todas las respuestas: CSP estricta (sin `unsafe-inline` ni `unsafe-eval`), `X-Content-Type-Options`, `Referrer-Policy`, `frame-ancestors 'none'`, HSTS cuando haya TLS.
11. Errores al cliente: mensaje genérico + id de correlación. El detalle solo en logs del servidor.
12. Nunca registrar tokens, cookies, cabeceras `Authorization` ni secretos. Logs estructurados con `log/slog`.
13. SQL solo con consultas parametrizadas. Prohibido `os/exec`, `unsafe`, `reflect` para lógica de negocio y `InsecureSkipVerify`.
14. El ServiceAccount del backend es de solo lectura (`get`, `list`, `watch`) y **no** tiene acceso a Secrets ni ConfigMaps. No añadir verbos ni recursos sin un ADR.

### Frontend Angular
15. Prohibido `innerHTML` con datos dinámicos, `bypassSecurityTrust*` y `eval`. Las líneas de log se renderizan siempre como texto.
16. TypeScript en modo `strict`. Compatible con CSP estricta (sin scripts ni estilos inline).
17. Ninguna URL de Loki, Prometheus o Keycloak en el frontend: solo habla con su propio backend (mismo origen).

### Dependencias y cadena de suministro
18. Antes de añadir una dependencia: comprobar que está mantenida, que es necesaria (preferir la librería estándar) y que no tiene vulnerabilidades conocidas. Justificarlo en el PR.
19. Versiones fijadas: `go.sum` y `package-lock.json` siempre commiteados; imágenes base por digest; GitHub Actions fijadas por SHA de commit.
20. Antes de dar por terminada una tarea: `make lint`, `make test` y `make security` deben pasar.

### Contenedores y Kubernetes
21. Imagen distroless `nonroot`, sin shell. Pod con `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`. Cumple Pod Security Standard `restricted`.
22. NetworkPolicy con denegación por defecto; solo se abren los flujos documentados en `docs/architecture.md`.
23. Requests y limits de CPU y memoria en todos los contenedores.

### Secretos
24. Nunca commitear secretos, tokens, kubeconfigs ni `.env`. Solo `.env.example` con valores ficticios. En el cluster, los secretos van en Kubernetes Secrets referenciados por el chart.

## Reglas de trabajo para Claude

- **Nunca ejecutar comandos contra un cluster que no sea de kind.** Antes de cualquier `kubectl` o `helm`, verificar que `kubectl config current-context` empiece por `kind-`. Si no, detenerse y preguntar. Preferir los scripts de `deploy/kind/` (o `--kubeconfig .kind/kubeconfig --context kind-kubeportal`), que nunca usan el contexto actual.
- No ejecutar comandos de escritura (`apply`, `delete`, `patch`, `helm install/upgrade`) fuera del entorno kind.
- Cambios pequeños y enfocados; un tema por commit (Conventional Commits en inglés: `feat:`, `fix:`, `docs:`, `chore:`, `test:`, `sec:`).
- Código, identificadores y comentarios en inglés. Documentación en español.
- Si un cambio contradice `docs/architecture.md` o un ADR, proponer primero un ADR nuevo.
- No añadir funciones fuera del alcance de la fase actual (`docs/roadmap.md`).

# Desarrollo local

Todo el desarrollo usa un cluster kind (ADR 0007). Los scripts de `deploy/kind/` usan un kubeconfig propio en `.kind/kubeconfig` y el contexto explícito `kind-kubeportal`: **no leen ni cambian el contexto actual de `kubectl`**, así que no pueden tocar un cluster real por accidente.

## Requisitos

Go (versión de `backend/go.mod`), Node.js 24 o 26 (la imagen se construye con 26), Docker (con permiso para el usuario), kind, kubectl, Helm 4, openssl, golangci-lint v2, govulncheck, gitleaks y trivy.

## Límites de inotify (una sola vez por máquina)

kind necesita límites de inotify más altos que los de muchas distribuciones. Sin ellos, el streaming de logs falla con `too many open files`:

```bash
echo -e 'fs.inotify.max_user_instances=512\nfs.inotify.max_user_watches=524288' | sudo tee /etc/sysctl.d/99-kind.conf
sudo sysctl --system
```

Si tu usuario acaba de entrar al grupo `docker`, cierra sesión y vuelve a entrar (o usa `sg docker -c "make dev-up"`).

## Entorno kind

```bash
make dev-up       # crea el cluster y despliega todo (idempotente)
make dev-verify   # comprueba {team="payments"} en Loki y label_team en kube-state-metrics
make dev-deploy   # construye la imagen, la carga en kind, instala el chart y carga los equipos de ejemplo
make dev-forward  # port-forward del portal (:8080) y de Keycloak (:8180) a la vez (se corta tras dev-deploy: vuelve a lanzarlo)
make dev-portal   # port-forward del portal en http://localhost:8080
make dev-keycloak # port-forward de Keycloak en http://localhost:8180
make dev-down     # elimina el cluster
```

| Namespace | Contenido |
|---|---|
| `monitoring` | Loki (single binary), Alloy (copia el label `team` a Loki), Prometheus + kube-state-metrics (`label_team`) + node-exporter |
| `keycloak` | Keycloak en modo `start-dev` con el realm `kubeportal` importado |
| `kubeportal` | PostgreSQL y, tras `make dev-deploy`, el portal |
| `dev`, `staging`, `prod` | Apps de ejemplo |

Los tres namespaces de ambiente y `kubeportal` aplican el Pod Security Standard `restricted`.

### Apps de ejemplo

| App | Equipo | Comportamiento |
|---|---|---|
| `payments-api`, `checkout-web` | `payments` | Logs JSON con niveles info/warn/error |
| `catalog-api` | `catalog` | Igual |
| `catalog-worker` | `catalog` | Termina con error cada 1–3 minutos (reinicios, CrashLoopBackOff) |
| `legacy-batch` | sin label | Solo debe verlo el administrador |
| `search-api` | `search` | El equipo `search` no existe al arrancar: lo crea un administrador desde Equipos (criterio de la fase 3) |

### Usuarios de Keycloak

| Usuario | Grupo |
|---|---|
| `admin` | `kubeportal-admin` |
| `alice` | `team-payments` |
| `bob` | `team-catalog` |
| `dave` | `team-search` (no ve nada hasta que un admin crea el equipo `search` con ese grupo) |
| `carol` | ninguno (no debe ver nada) |

El mapeo de grupos a equipos de ejemplo (`team-payments` → `payments`, `team-catalog` → `catalog`) se carga con `deploy/kind/manifests/seed-teams.sql` al final de `make dev-deploy`. El resto se gestiona desde Equipos en la vista de administración (usuario `admin`).

Para probar el portal en el navegador: `make dev-forward` (los dos port-forwards son necesarios: el login redirige a Keycloak en `localhost:8180`), abrir http://localhost:8080 y entrar con uno de los usuarios (la contraseña es `DEMO_USER_PASSWORD` de `.kind/secrets.env`).

Las contraseñas, el secreto del client OIDC y la contraseña de PostgreSQL se generan al azar la primera vez en `.kind/secrets.env` (ignorado por git) y se cargan como Secrets de Kubernetes. No hay credenciales en el repositorio.

## Backend y frontend fuera del cluster

El backend necesita PostgreSQL, Keycloak, Loki, Prometheus y la API de Kubernetes. Para correrlo en local contra kind, abre port-forwards a esos servicios y define en `.env` las variables `KUBEPORTAL_*` (ver `.env.example` y `backend/internal/config/config.go`). Con `KUBEPORTAL_KUBECONFIG` hay que fijar también `KUBEPORTAL_KUBE_CONTEXT=kind-kubeportal`: el backend nunca usa el contexto actual del kubeconfig.

```bash
make backend-run   # backend en :8080 (lee .env si existe)
make frontend-run  # ng serve en :4200 con proxy de /api (incluido WebSocket), /auth y /healthz al backend
```

Con `ng serve`, `KUBEPORTAL_PUBLIC_URL` debe ser `http://localhost:4200` (el realm de kind acepta ese redirect).

Los tests de `internal/store` usan un PostgreSQL desechable si se define `KUBEPORTAL_TEST_DATABASE_URL` (borran y recrean el esquema); sin ella se omiten.

## Antes de terminar una tarea

```bash
make lint && make test && make security
```

El CI (`.github/workflows/ci.yml`) ejecuta lo mismo, más el escaneo de la imagen, el SBOM y un job `kind-e2e` que corre `make dev-up`, `make dev-deploy` y `make dev-verify` desde cero.

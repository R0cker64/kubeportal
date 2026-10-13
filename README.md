# KubePortal (nombre provisional)

Portal web de autoservicio para Kubernetes: cada equipo ve los logs y la salud de sus servicios, sin acceso a Grafana ni al cluster.

> Estado: fase 1 (esqueleto). Ver el [roadmap](docs/roadmap.md).

## Características de la v1

- Login con Keycloak (OIDC, patrón BFF)
- Aislamiento por equipo mediante el label `team`
- Logs desde Loki, con búsqueda y modo en vivo
- Salud de servicios: estado de pods y CPU/RAM desde Prometheus
- Vista de nodos y gestión de equipos para administradores

## Documentación

- [Desarrollo local](docs/development.md)
- [Arquitectura](docs/architecture.md)
- [Modelo de seguridad](docs/security-model.md)
- [Roadmap](docs/roadmap.md)
- [Decisiones de arquitectura (ADR)](docs/adr/README.md)

## Requisitos de desarrollo

Go (última estable), Node.js LTS, Docker, kind, kubectl, Helm, openssl, golangci-lint, govulncheck, gitleaks y trivy. Ver [docs/development.md](docs/development.md).

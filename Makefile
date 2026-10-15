SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
BACKEND_DIR := backend
FRONTEND_DIR := frontend
IMAGE ?= kubeportal:dev

.PHONY: help
help: ## List targets
	@grep -E '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- Local environment (kind only; never touches the current kubectl context) ---

.PHONY: dev-up
dev-up: ## Create the kind cluster with Loki, Prometheus, Keycloak, PostgreSQL and demo apps
	deploy/kind/up.sh

.PHONY: dev-down
dev-down: ## Delete the kind cluster
	deploy/kind/down.sh

.PHONY: dev-verify
dev-verify: ## Check that Loki and kube-state-metrics expose the team label
	deploy/kind/verify.sh

.PHONY: dev-deploy
dev-deploy: ## Build the image and install the chart into kind
	deploy/kind/deploy-app.sh

.PHONY: dev-e2e
dev-e2e: ## Log in as each demo user and check team isolation through the portal
	deploy/kind/e2e.sh

.PHONY: dev-forward
dev-forward: ## Port-forward the portal (:8080) and Keycloak (:8180) together
	deploy/kind/forward-all.sh

# --- Install rehearsal (second kind cluster installed like production) ---

.PHONY: rehearsal-up
rehearsal-up: ## Install rehearsal: kind cluster installed with Flux from GitHub and the GHCR chart (REHEARSAL_VERSION)
	deploy/rehearsal/up.sh

.PHONY: rehearsal-e2e
rehearsal-e2e: ## Run the isolation e2e against the install rehearsal
	KIND_CLUSTER_NAME=kubeportal-staging KEYCLOAK_SERVICE=keycloak-service deploy/kind/e2e.sh

.PHONY: rehearsal-forward
rehearsal-forward: ## Port-forward the rehearsal portal (:8080) and Keycloak (:8180)
	KIND_CLUSTER_NAME=kubeportal-staging KEYCLOAK_SERVICE=keycloak-service deploy/kind/forward-all.sh

.PHONY: rehearsal-down
rehearsal-down: ## Delete the install rehearsal cluster and its fake Cloud SQL
	deploy/rehearsal/down.sh

.PHONY: dev-keycloak
dev-keycloak: ## Port-forward Keycloak to http://localhost:8180
	deploy/kind/port-forward.sh keycloak keycloak 8180:8080

.PHONY: dev-portal
dev-portal: ## Port-forward the in-cluster portal to http://localhost:8080
	deploy/kind/port-forward.sh kubeportal kubeportal 8080:8080

# --- Run ---

.PHONY: backend-run
backend-run: ## Run the backend locally (reads .env when present)
	set -a; [[ -f .env ]] && source ./.env; set +a; \
	export KUBEPORTAL_PUBLIC_URL="$${KUBEPORTAL_PUBLIC_URL:-http://localhost:8080}"; \
	export KUBEPORTAL_ENVIRONMENTS="$${KUBEPORTAL_ENVIRONMENTS:-dev,staging,prod}"; \
	cd $(BACKEND_DIR) && $(GO) run ./cmd/server

.PHONY: frontend-run
frontend-run: ## ng serve with proxy to the backend on :8080
	cd $(FRONTEND_DIR) && $(NPM) start

# --- Quality gates (all must pass before finishing a task) ---

.PHONY: test
test: test-backend test-frontend ## Backend and frontend tests

.PHONY: test-backend
test-backend:
	cd $(BACKEND_DIR) && $(GO) test -race -count=1 ./...

.PHONY: test-frontend
test-frontend:
	cd $(FRONTEND_DIR) && $(NPM) test

.PHONY: lint
lint: lint-backend lint-frontend lint-helm ## golangci-lint, eslint and helm lint

.PHONY: lint-backend
lint-backend:
	cd $(BACKEND_DIR) && golangci-lint run ./...

.PHONY: lint-frontend
lint-frontend:
	cd $(FRONTEND_DIR) && $(NPM) run lint

.PHONY: lint-helm
lint-helm:
	helm lint --strict deploy/helm/kubeportal

.PHONY: security
security: ## govulncheck, npm audit, gitleaks and trivy
	cd $(BACKEND_DIR) && govulncheck ./...
	cd $(FRONTEND_DIR) && $(NPM) audit --audit-level=high
	gitleaks dir . --config .gitleaks.toml --redact --no-banner
	gitleaks git . --config .gitleaks.toml --redact --no-banner
	trivy fs --quiet --scanners vuln --severity HIGH,CRITICAL --exit-code 1 --skip-dirs '**/node_modules' .
	trivy config --quiet --ignorefile .trivyignore.yaml --severity HIGH,CRITICAL --exit-code 1 --skip-dirs '**/node_modules' .

.PHONY: image
image: ## Build the container image
	docker build -t $(IMAGE) .

.PHONY: image-scan
image-scan: image ## Scan the built image with trivy
	trivy image --quiet --severity HIGH,CRITICAL --exit-code 1 --ignore-unfixed $(IMAGE)

.PHONY: fmt
fmt: ## Format Go code
	cd $(BACKEND_DIR) && gofmt -w . && golangci-lint fmt ./...

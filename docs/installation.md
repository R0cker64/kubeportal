# Instalación en un cluster

Guía para instalar KubePortal en un cluster real (pensada para GKE, válida para otros). El portal se instala con un solo `helm install` o con Flux; Keycloak y la base de datos son prerrequisitos.

```
             kubectl port-forward (8080 y 8180)
Navegador ───────────────────────────────────────┐
                                                 ▼
┌──────────── namespace kubeportal ─────────┐  ┌──── namespace keycloak ─────┐
│ KubePortal (chart, imagen firmada)        │─►│ Keycloak (operador oficial) │
└─────┬───────────────┬─────────────────────┘  └──────────────┬──────────────┘
      │ LogQL/PromQL  │ SQL (TLS, verify-ca)                   │ SQL (TLS, verify-ca)
      ▼               ▼                                        ▼
 Loki, Prometheus   Cloud SQL (IP privada): bases kubeportal y keycloak
```

## 1. Comprobar los prerrequisitos

Los mismos de [arquitectura](architecture.md#prerrequisitos-en-el-cluster): label `team` y `app.kubernetes.io/name` en los workloads, el colector de logs copiándolos a Loki, kube-state-metrics con el allowlist de labels y cAdvisor en Prometheus.

El script de verificación es de solo lectura (`kubectl get` y port-forwards temporales) y exige el contexto de forma explícita:

```bash
deploy/preflight/check-cluster.sh \
  --context <contexto-del-cluster> \
  --environments dev,staging,prod \
  --loki monitoring/loki-gateway:80 \
  --prometheus monitoring/prometheus-server:80
```

Indica qué workloads no tienen label, si Loki y kube-state-metrics los propagan y qué valor usar en `config.cadvisorNodeLabel`. La retención de Loki no la comprueba: confírmala (`limits_config.retention_period`, `compactor.retention_enabled`) y no pongas `logMaxRange` por encima.

La NetworkPolicy del portal necesita que el cluster las aplique (en GKE, Dataplane V2 o la aplicación de NetworkPolicy activada).

## 2. Cloud SQL

En la misma instancia, una base de datos y un usuario para cada componente:

```sql
CREATE USER kubeportal WITH PASSWORD '<contraseña>';
CREATE DATABASE kubeportal OWNER kubeportal;
CREATE USER keycloak WITH PASSWORD '<contraseña>';
CREATE DATABASE keycloak OWNER keycloak;
```

Necesitas la IP privada de la instancia y el CA del servidor:

```bash
gcloud sql instances describe <instancia> --format='value(ipAddresses[0].ipAddress)'
gcloud sql instances describe <instancia> --format='value(serverCaCert.cert)' > server-ca.pem
```

Las conexiones usan `sslmode=verify-ca`: el certificado de Cloud SQL no incluye la IP privada, así que no se puede verificar el nombre, pero sí que lo firma tu CA. Activa "Allow only SSL connections" en la instancia. Si rotas el CA, actualiza los Secrets `cloudsql-ca` y `kubeportal-db-ca`.

## 3. Secrets

Ningún secreto va en el repositorio. Créalos a mano como se muestra, o con tu herramienta habitual (SOPS con Flux, External Secrets) usando los mismos nombres y claves.

```bash
CTX=<contexto-del-cluster>
CLIENT_SECRET="$(openssl rand -hex 32)"

kubectl --context "$CTX" create namespace keycloak
kubectl --context "$CTX" create namespace kubeportal

# keycloak
kubectl --context "$CTX" -n keycloak create secret generic keycloak-db \
  --from-literal=username=keycloak --from-literal=password='<contraseña>'
kubectl --context "$CTX" -n keycloak create secret generic cloudsql-ca --from-file=ca.crt=server-ca.pem
kubectl --context "$CTX" -n keycloak create secret generic keycloak-bootstrap-admin \
  --from-literal=username=bootstrap-admin --from-literal=password="$(openssl rand -hex 16)"
kubectl --context "$CTX" -n keycloak create secret generic kubeportal-oidc --from-literal=client-secret="$CLIENT_SECRET"

# kubeportal
kubectl --context "$CTX" -n kubeportal create secret generic kubeportal-oidc --from-literal=client-secret="$CLIENT_SECRET"
kubectl --context "$CTX" -n kubeportal create secret generic kubeportal-session --from-literal=key="$(openssl rand -hex 32)"
kubectl --context "$CTX" -n kubeportal create secret generic kubeportal-db-ca --from-file=ca.crt=server-ca.pem
kubectl --context "$CTX" -n kubeportal create secret generic kubeportal-db --from-literal=url=\
'postgres://kubeportal:<contraseña>@<ip-privada>:5432/kubeportal?sslmode=verify-ca&sslrootcert=/etc/kubeportal/db-ca/ca.crt'
```

Si la contraseña tiene caracteres especiales, codifícala para URL. Con Flux, las namespaces las crea `stack.yaml`; en ese caso no ejecutes los `create namespace`.

## 4. Keycloak

Se instala con el operador oficial, el CR `Keycloak` y la importación del realm `kubeportal` (client confidencial con PKCE, claim `groups`, grupo `kubeportal-admin`). Los manifiestos están en [`deploy/flux/`](../deploy/flux/) y se aplican con Flux (sección 5). El realm se importa solo la primera vez; los cambios posteriores se hacen en la consola.

Después del primer arranque:

1. Abre la consola: `kubectl --context "$CTX" -n keycloak port-forward svc/keycloak-service 8180:8080` y entra en http://localhost:8180/admin con el usuario de `keycloak-bootstrap-admin`.
2. En el realm `master`, crea un administrador permanente y borra el usuario `bootstrap-admin`.
3. En el realm `kubeportal`, crea los usuarios (o conecta tu directorio) y los grupos de cada equipo. Añade a los administradores del portal al grupo `kubeportal-admin`.

## 5. Opción A: Flux (recomendada)

1. Copia `deploy/flux/` a tu repositorio GitOps, p. ej. a `clusters/<cluster>/kubeportal/`.
2. Sustituye cada `REPLACE_ME`: las rutas `path` de `stack.yaml`, la IP de Cloud SQL en `keycloak/keycloak.yaml` y los valores de `kubeportal/helmrelease.yaml` (ambientes, Loki, Prometheus, endpoint del API server, namespaces de monitorización).
3. Incluye `stack.yaml` en la Kustomization de Flux que ya reconcilia ese directorio y haz commit.

Flux instala el operador de Keycloak, luego Keycloak y el realm, y por último el portal. Solo acepta el chart si la firma de cosign la hizo el workflow de release de este repositorio en un tag de versión. El chart ya fija la imagen por digest.

```bash
flux --context "$CTX" get kustomizations keycloak-operator keycloak kubeportal
flux --context "$CTX" -n kubeportal get helmreleases
```

## 6. Opción B: un solo `helm install`

Con los Secrets creados y Keycloak en marcha, el portal se instala con un solo comando. Primero verifica la firma del chart:

```bash
cosign verify ghcr.io/r0cker64/charts/kubeportal:0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/R0cker64/kubeportal/\.github/workflows/release\.yml@refs/tags/v'

helm install kubeportal oci://ghcr.io/r0cker64/charts/kubeportal --version 0.1.0 \
  --kube-context "$CTX" -n kubeportal -f values-gke.yaml
```

`values-gke.yaml` de ejemplo (los valores que no pongas usan los del chart):

```yaml
config:
  environments: dev,staging,prod
  oidc:
    issuerURL: http://localhost:8180/realms/kubeportal
    discoveryURL: http://keycloak-service.keycloak.svc:8080/realms/kubeportal
  lokiURL: http://loki-gateway.monitoring.svc
  prometheusURL: http://prometheus-server.monitoring.svc
  cadvisorNodeLabel: instance   # lo que indique check-cluster.sh
database:
  caSecret:
    name: kubeportal-db-ca
networkPolicy:
  apiServer:
    cidrs: ["<endpoint-del-control-plane>/32"]
    ports: [443]
  egress:
    loki:       { namespace: monitoring, podLabels: { app.kubernetes.io/name: loki }, ports: [80, 3100, 8080] }
    prometheus: { namespace: monitoring, podLabels: { app.kubernetes.io/name: prometheus }, ports: [80, 9090] }
    keycloak:   { namespace: keycloak, podLabels: { app.kubernetes.io/instance: keycloak, app.kubernetes.io/component: server }, ports: [8080] }
    postgres:   { cidrs: ["<ip-privada-cloud-sql>/32"], ports: [5432] }
```

El chart valida los valores con `values.schema.json`: una URL, una duración o un nombre mal escritos hacen fallar la instalación antes de crear nada. Para comprobar la instalación: `helm test kubeportal -n kubeportal`.

## 7. Acceso

En la v1 se accede con port-forward ([riesgos aceptados](security-model.md#riesgos-aceptados-en-la-v1)). Cada usuario necesita dos port-forwards abiertos, porque el login redirige a Keycloak en `localhost:8180`:

```bash
kubectl -n kubeportal port-forward svc/kubeportal 8080:8080
kubectl -n keycloak port-forward svc/keycloak-service 8180:8080
```

Y luego abrir http://localhost:8080. Si el pod se reinicia (actualización), el port-forward se corta y hay que volver a abrirlo.

Los usuarios solo necesitan permiso de port-forward a esos dos Services, no acceso al resto del cluster:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kubeportal-access
  namespace: kubeportal   # y otro igual en keycloak
rules:
  - apiGroups: [""]
    resources: [services, pods]
    verbs: [get, list]
  - apiGroups: [""]
    resources: [pods/portforward]
    verbs: [create]
```

## 8. Primeros pasos en el portal

1. Entra con un usuario del grupo `kubeportal-admin`.
2. En **Equipos**, crea cada equipo con el valor de su label `team` y los grupos de Keycloak de sus miembros.
3. En **Sin equipo**, revisa los workloads sin label o con un equipo que no existe.

Los miembros ven sus servicios en cuanto el equipo existe; no hace falta tocar el cluster.

## 9. Actualizar y desinstalar

- **Flux:** cambia `ref.tag` en `kubeportal/ocirepository.yaml`. Si una actualización falla, el HelmRelease vuelve a la versión anterior.
- **Helm:** `helm upgrade kubeportal oci://ghcr.io/r0cker64/charts/kubeportal --version <nueva> -n kubeportal -f values-gke.yaml`.
- **Desinstalar:** `helm uninstall kubeportal -n kubeportal` (o quitar la Kustomization `kubeportal`). Las bases de datos de Cloud SQL y los Secrets no se borran.

## Problemas comunes

| Síntoma | Causa |
|---|---|
| Al pulsar "Entrar" el navegador no puede conectar | Falta el port-forward de Keycloak en `localhost:8180` |
| Login falla con una referencia | Revisa el log del portal con esa referencia; suele ser el secret del client distinto entre namespaces o la URL de discovery |
| El pod no pasa a Ready | `/readyz` comprueba PostgreSQL y los informers: revisa la URL de la base de datos, el CA y el egress `postgres` de la NetworkPolicy |
| Un servicio no aparece | Falta el label `team` (solo lo ve el admin en **Sin equipo**) o el equipo no tiene el grupo del usuario |
| Sin logs o sin métricas en un servicio | El template de sus pods no tiene `app.kubernetes.io/name`, o Loki o kube-state-metrics no lo propagan (ejecuta `check-cluster.sh`) |
| Nodos sin uso real | `config.cadvisorNodeLabel` no coincide con tu Prometheus |

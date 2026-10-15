# Manifiestos de Flux (ejemplo)

Plantillas para instalar KubePortal con Flux desde **tu** repositorio GitOps. Cópialas allí, sustituye cada `REPLACE_ME` y sigue la [guía de instalación](../../docs/installation.md).

| Archivo | Contenido |
|---|---|
| `stack.yaml` | Namespaces `keycloak` y `kubeportal` (Pod Security `restricted`), el operador oficial de Keycloak (tag fijado, imagen por digest, endurecido) y las Kustomizations de Flux en orden: operador → Keycloak → portal |
| `keycloak/keycloak.yaml` | Servidor Keycloak: base de datos en Cloud SQL por IP privada con TLS verificado, issuer fijo `http://localhost:8180` |
| `keycloak/realm-import.yaml` | Realm `kubeportal`: client confidencial con PKCE, claim `groups`, grupo `kubeportal-admin`, sin usuarios |
| `kubeportal/ocirepository.yaml` | Chart desde `oci://ghcr.io/r0cker64/charts`, solo si la firma de cosign es del workflow de release |
| `kubeportal/helmrelease.yaml` | Valores del portal para el cluster |

Probado en kind: el operador (vía Flux), Keycloak, la importación del realm y el Pod Security `restricted`, con un PostgreSQL sin TLS. La conexión con TLS a Cloud SQL (`verify-ca`) no se puede probar fuera de GCP: compruébala en el primer despliegue.

Los Secrets que necesita cada namespace están listados en la cabecera de cada manifiesto y en la guía. No los añadas al repositorio sin cifrar.

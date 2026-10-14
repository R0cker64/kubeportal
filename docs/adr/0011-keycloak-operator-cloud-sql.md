# 0011. Keycloak con el operador oficial y Cloud SQL por IP privada

**Estado:** Aceptada

**Contexto:** El cluster real no tiene Keycloak (ADR 0004) y la base de datos del portal está en Cloud SQL, accesible desde GKE por IP privada. Hay que decidir cómo desplegar Keycloak, dónde guarda sus datos y cómo se conectan ambos a la base de datos.

**Decisión:**

- Keycloak se instala con su operador oficial (`keycloak-k8s-resources`, tag fijado e imagen por digest) mediante Flux, con el CR `Keycloak` y la importación del realm `kubeportal` como `KeycloakRealmImport`. El secreto del client entra por un placeholder desde un Secret.
- Keycloak usa la misma instancia de Cloud SQL que el portal, con base de datos y usuario propios.
- Portal y Keycloak se conectan por IP privada con TLS y `sslmode=verify-ca` contra el CA del servidor de Cloud SQL, montado desde un Secret. No se verifica el nombre porque el certificado de Cloud SQL no incluye la IP privada.
- El namespace `keycloak` aplica Pod Security `restricted`: el Deployment del operador se endurece con un patch de Flux y los pods de Keycloak y del job de importación con `unsupported.podTemplate`.
- En la v1, Keycloak se expone igual que el portal, por port-forward, con issuer fijo `http://localhost:8180`.

**Consecuencias:** Un solo PostgreSQL que mantener, con los backups de GCP. El operador gestiona las actualizaciones de Keycloak. `unsupported.podTemplate` puede cambiar entre versiones del operador: revisarlo al actualizar. La conexión TLS a Cloud SQL solo se puede probar en GCP; en kind se probaron el operador, el realm y el Pod Security con un PostgreSQL sin TLS.

# 0010. Publicación en GHCR firmada con cosign keyless

**Estado:** Aceptada

**Contexto:** La fase 4 publica el portal para instalarlo con un solo `helm install` o con Flux. La imagen y el chart son la cadena de suministro del producto: si alguien los sustituye, controla un pod con lectura de todo el cluster (T7, T9). El repositorio es público y GitHub Actions ya construye y escanea la imagen.

**Decisión:**

- Un workflow (`.github/workflows/release.yml`) publica al crear un tag `vX.Y.Z`: la imagen en `ghcr.io/r0cker64/kubeportal` y el chart como OCI en `oci://ghcr.io/r0cker64/charts/kubeportal`. La versión del chart y la `appVersion` salen del tag.
- La imagen se escanea con trivy antes de publicarse (HIGH/CRITICAL bloquean) y se publica con SBOM y provenance de BuildKit.
- Imagen y chart se firman con cosign keyless (identidad OIDC del workflow, sin claves que guardar).
- El chart publicado fija la imagen por digest (`image.digest`), así que una versión del chart siempre despliega la misma imagen.
- Flux verifica la firma del chart (`OCIRepository.spec.verify`) y solo acepta las del workflow de release en un tag de versión del repositorio.

**Consecuencias:** Cualquiera puede verificar el origen de la imagen y del chart con `cosign verify`. Publicar requiere permisos `packages: write` e `id-token: write` solo en ese workflow. Los paquetes de GHCR nacen privados: tras la primera publicación hay que hacerlos públicos en GitHub. Las versiones de Keycloak y del operador en `deploy/flux/` no las actualiza Dependabot: se suben a mano.

# 0001. App web con backend central

**Estado:** Aceptada

**Contexto:** Herramientas como Lens son de escritorio y usan el kubeconfig de cada persona. Aquí se necesitan usuarios, equipos y permisos compartidos, y que los usuarios no tengan acceso directo al cluster.

**Decisión:** App web con un backend central que es el único con credenciales hacia el cluster y sus fuentes de datos.

**Consecuencias:** Los usuarios no necesitan kubeconfig. El backend concentra el riesgo: su seguridad es crítica (ver `docs/security-model.md`).

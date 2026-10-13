# 0007. Desarrollo local con kind

**Estado:** Aceptada

**Contexto:** Desarrollar contra el cluster de la empresa mezclaría datos reales con un proyecto que será público.

**Decisión:** Todo el desarrollo y la demo usan un cluster kind con Loki, Prometheus, Keycloak y apps de ejemplo etiquetadas con `team`. El cluster real solo se usa para validar al final.

**Consecuencias:** Requiere Docker y kind en la máquina de desarrollo. La demo del portafolio es reproducible y no contiene datos de la empresa.

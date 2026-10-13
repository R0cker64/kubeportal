# 0005. Usuarios de solo lectura en la v1

**Estado:** Aceptada

**Contexto:** Acciones como reiniciar o escalar aumentan el riesgo y chocan con Flux, que revierte los cambios manuales.

**Decisión:** En la v1 los usuarios solo leen. El ServiceAccount del backend tiene solo `get`, `list` y `watch`, sin Secrets ni ConfigMaps.

**Consecuencias:** Superficie de ataque mínima. Las acciones se evaluarán después de la v1 con un ADR propio.

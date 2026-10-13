# 0006. Proxy a Loki y Prometheus en vez de embeber Grafana

**Estado:** Aceptada

**Contexto:** No se quiere dar acceso a Grafana a los usuarios. Embeber paneles de Grafana requeriría exponerla y replicar sus permisos.

**Decisión:** El backend consulta Loki y Prometheus por sus API HTTP con consultas construidas en el servidor. Loki y Prometheus no se exponen fuera del cluster.

**Consecuencias:** Control total del filtro por equipo. Las gráficas se construyen en Angular. Los dashboards personalizables quedan fuera de la v1.

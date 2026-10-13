# 0003. Autorización por label `team` aplicada en el backend

**Estado:** Aceptada

**Contexto:** Los namespaces son por ambiente (`dev`, `staging`, `prod`) y contienen apps de varios equipos. El RBAC de Kubernetes da permisos por namespace o por nombre de recurso, no por label, así que no puede aislar equipos. Hoy los workloads solo tienen label de app.

**Decisión:** Cada workload lleva un label `team`. El backend usa un ServiceAccount de solo lectura y filtra en `internal/authz` por los equipos del usuario. Se descarta la impersonation de Kubernetes.

**Consecuencias:** El aislamiento depende del código del backend: `authz` centralizado y tests de aislamiento obligatorios. Hay que agregar el label a las apps y propagarlo a Loki y kube-state-metrics (fase 0). Un workload sin label solo lo ve el administrador.

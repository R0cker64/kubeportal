# 0002. Go en el backend, Angular en el frontend

**Estado:** Aceptada

**Contexto:** El autor domina Angular y no conoce Go. El proyecto busca peso en perfiles de plataforma/DevOps.

**Decisión:** Backend en Go (lenguaje nativo de Kubernetes: `client-go`, informers). Frontend en Angular.

**Consecuencias:** Curva de aprendizaje en Go, compensada porque el backend es mayormente integración. Binario pequeño e imagen distroless mínima. Se descartó .NET + Angular por menor encaje con el ecosistema de Kubernetes.

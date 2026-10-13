# 0004. Keycloak con patrón BFF (tokens solo en el servidor)

**Estado:** Aceptada (precisada por el ADR 0009: los tokens se validan y se descartan)

**Contexto:** Se necesita un proveedor de identidad portable a otros clusters. En una SPA, guardar tokens en el navegador los expone a XSS. La guía actual de OAuth para apps de navegador recomienda un Backend-for-Frontend.

**Decisión:** Keycloak (se instalará; hoy no existe) como proveedor OIDC. El backend Go es un client confidencial que hace Authorization Code + PKCE, guarda los tokens del lado del servidor y entrega al navegador solo una cookie de sesión `__Host-`, `HttpOnly`, `Secure`, `SameSite=Strict`.

**Consecuencias:** El frontend no maneja tokens ni librerías OIDC. Se requiere almacenamiento de sesiones (PostgreSQL) y protección CSRF. Keycloak necesita un hostname fijo para que el `iss` sea estable con port-forward y con dominio.

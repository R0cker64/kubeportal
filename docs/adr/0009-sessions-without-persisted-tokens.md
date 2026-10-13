# 0009. Sesiones del BFF sin tokens persistidos

**Estado:** Aceptada (precisa el ADR 0004)

**Contexto:** El ADR 0004 establece que el backend guarda los tokens del lado del servidor. Al implementar la fase 2 se vio que el portal solo necesita del ID token la identidad (`sub`, `preferred_username`) y el claim `groups`, y que no llama a ninguna API en nombre del usuario. Guardar access y refresh tokens en PostgreSQL añadiría un activo sensible (con un refresh token se obtienen tokens nuevos) sin ningún uso.

**Decisión:**

- El backend valida el ID token en el callback (firma con JWKS, `iss`, `aud`, `azp` si hay varias audiencias, `exp`, `nbf`, `nonce`, algoritmo RS256/ES256) y descarta todos los tokens.
- La sesión en PostgreSQL guarda solo `sub`, `username`, `groups` y sus tiempos. La clave es el SHA-256 de un id aleatorio de 256 bits; el id solo viaja en la cookie `__Host-kp_session` (`HttpOnly`, `Secure`, `SameSite=Strict`, sin `Max-Age`).
- Vida de la sesión: 8 h absolutas y 1 h de inactividad (configurables). Una tarea periódica borra las expiradas.
- Los equipos se resuelven en cada petición a partir de los grupos de la sesión, así que un cambio en el mapeo grupo → equipo se aplica sin volver a iniciar sesión. Un cambio de grupos en Keycloak se aplica en el siguiente login.
- El estado del login (`state`, `nonce`, verificador PKCE) viaja en una cookie `__Host-kp_login` cifrada con AES-256-GCM (clave `KUBEPORTAL_SESSION_KEY`), de un solo uso y válida 10 minutos. Es `SameSite=Lax` porque el callback es una navegación que llega desde Keycloak; no autoriza nada por sí sola.
- El logout borra la sesión y redirige al `end_session_endpoint` de Keycloak con `client_id` y `post_logout_redirect_uri` (sin `id_token_hint`, porque no se guarda).

**Consecuencias:** Un volcado de la base de datos no permite suplantar sesiones ni obtener tokens. Al no haber `id_token_hint`, Keycloak puede pedir confirmación al cerrar sesión. Los grupos quedan fijos durante la sesión (máximo 8 h); quitar a alguien de un grupo surte efecto al expirar su sesión o al borrar sus sesiones en la base de datos.

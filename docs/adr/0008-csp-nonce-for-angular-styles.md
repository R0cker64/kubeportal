# 0008. Nonce de CSP para los estilos de componentes de Angular

**Estado:** Aceptada

**Contexto:** La regla 16 exige una CSP estricta sin `unsafe-inline`. Angular inserta los estilos de cada componente en tiempo de ejecución como elementos `<style>`, que una política `style-src 'self'` bloquea. Las alternativas son prescindir de los estilos por componente (todo en hojas globales) o autorizar esos `<style>` con un nonce.

**Decisión:** El backend genera un nonce aleatorio de 128 bits por respuesta, lo incluye en `style-src 'self' 'nonce-…'` y lo sustituye en `index.html` (`<app-root ngCspNonce="__CSP_NONCE__">`), que se sirve con `Cache-Control: no-store`. `script-src` sigue siendo solo `'self'`. El build de producción desactiva la incrustación de CSS crítico (`inlineCritical: false`) y usa Subresource Integrity.

**Consecuencias:** Se mantienen los estilos por componente sin `unsafe-inline`. Un atacante con inyección de HTML no puede reutilizar el nonce porque cambia en cada petición, y los atributos `style="…"` siguen bloqueados (eslint prohíbe estilos inline en plantillas). `index.html` deja de ser un archivo estático cacheable.

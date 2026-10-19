# ADR 0002. Topología de despliegue

**Estado:** Aceptado (2026-10-18).

**Contexto.** Debe instalarse pegando un YAML en cualquier sistema con Docker Compose, correr sin
intervención y separar responsabilidades sin complejidad innecesaria. Diego pidió explícitamente
no diseñar para un NAS en particular.

**Decisión.** Un proyecto Compose con `postgres` (imagen oficial), `sqsolve` (panel, API, tareas)
y `sqsolve-init` (una sola ejecución para secretos, configuración y permisos). En 1.1 se agrega
`sqsolve-hooks` opcional por perfil. sqsolve habla con PostgreSQL por un socket Unix en un
volumen compartido.

**Alternativas.**
- Un solo contenedor con PostgreSQL y sqsolve bajo un supervisor (como Studio Server 1): más fácil
  de copiar en plataformas sin Compose, pero mezcla ciclos de vida, complica actualizaciones de
  PostgreSQL y obliga a un supervisor.
- Kubernetes o Helm: desproporcionado para el público.
- Paquetes nativos (deb, rpm): fuera de alcance de 1.x.

**Consecuencias.** Tres servicios en el YAML. Plataformas que no aceptan Compose quedan fuera
(se reevalúa un modo "todo en uno" si hay demanda real). sqsolve no puede reiniciar PostgreSQL
(no tiene socket de Docker): los cambios que requieren reinicio se señalan en la interfaz.

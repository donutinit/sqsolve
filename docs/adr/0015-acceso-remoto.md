# ADR 0015. Acceso remoto

**Estado:** Aceptado (2026-10-18).

**Contexto.** PostgreSQL expuesto a Internet es un blanco constante y Resolve no verifica
certificados (por confirmar en PoC-06).

**Decisión.** El único acceso remoto documentado es por VPN (WireGuard o Headscale). sqsolve
avisa si la dirección anunciada es una IP pública. No se incluye ni se depende de ningún servicio
de VPN.

**Alternativas.** Publicar 5432 con TLS y reglas de red (frágil); túneles de proveedores
comerciales (dependencia propietaria).

**Consecuencias.** La colaboración por VPN depende de que las estaciones se vean entre sí; PoC-08
documenta qué funciona.

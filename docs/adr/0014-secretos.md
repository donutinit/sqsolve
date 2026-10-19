# ADR 0014. Secretos

**Estado:** Aceptado (2026-10-18).

**Contexto.** sqsolve necesita mostrar de nuevo las contraseñas de bibliotecas (para conectar
estaciones) y guardar contraseñas de servicios de aviso. Variables de entorno filtran secretos a
`docker inspect` y a procesos hijos.

**Decisión.** Secretos de arranque en archivos 0400 en el volumen `secrets`, generados por
`sqsolve-init`. Secretos recuperables cifrados en SQLite con XChaCha20-Poly1305 y una clave
maestra de 32 bytes en `secrets/master.key`. Kit de recuperación obligatorio con la clave.

**Alternativas.** Mostrar contraseñas una sola vez y no guardarlas (obliga a rotar para cada
estación nueva); gestores externos de secretos (dependencia innecesaria).

**Consecuencias.** Quien lee el volumen `secrets` y el `state` tiene todas las contraseñas; se
documenta que esos volúmenes no deben compartirse ni mezclarse con los respaldos copiados fuera.

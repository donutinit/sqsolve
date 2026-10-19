# ADR 0008. Modelo de roles de PostgreSQL

**Estado:** Aceptado, pendiente de PoC-01.

**Contexto.** La convención de Blackmagic usa el superusuario `postgres` con contraseña
`DaVinci` para todo [01 §5]. Un superusuario expuesto en la red da control del servidor y, por
`COPY ... PROGRAM`, del contenedor.

**Decisión.**
- `postgres` y `sqsolve_admin` solo por socket local; rechazados por red en `pg_hba.conf`.
- `sqsolve_admin` sin superusuario, con `CREATEDB`, `CREATEROLE`, `pg_monitor`,
  `pg_signal_backend` y `pg_read_all_data`.
- Un rol propietario por biblioteca, con el mismo nombre que la base, que solo puede conectarse a
  ella. `CREATEDB` solo durante la ventana de inicialización.
- En 1.1, roles por persona miembros del propietario con `SET role` por defecto.

**Alternativas.** Superusuario compartido (inseguro); un rol por estación sin propietario común
(los objetos quedarían repartidos entre dueños).

**Consecuencias.** Requiere PostgreSQL 16+ (semántica de `CREATEROLE`). Si PoC-01 demuestra que
Resolve necesita ser dueño de la base, el dueño será el rol de la biblioteca y los renombres del
intercambio usarán el superusuario por socket, auditados.

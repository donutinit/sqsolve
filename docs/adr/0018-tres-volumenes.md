# ADR 0018. Tres volúmenes visibles

**Estado:** Aceptado (2026-10-18). Reemplaza la disposición de volúmenes de `12-despliegue.md`
(seis volúmenes) y la ubicación de secretos de ADR 0014.

**Contexto.** Diego pidió que la instalación no dependa de ningún NAS y preguntó si convenía
separar base de datos, aplicación y respaldos. Seis volúmenes (pgdata, pgconf, pgsocket, state,
secrets, backups) eran demasiados para mapear a mano en un "Install via YAML".

**Decisión.** El administrador mapea tres rutas, cada una con un perfil de almacenamiento
distinto:

| Volumen | Ruta en contenedores | Contiene | Almacenamiento |
|---|---|---|---|
| db | `/var/lib/postgresql` | Clúster (`18/docker`) y configuración que sqsolve genera (`sqsolve/`: `postgresql.conf`, `pg_hba.conf`, certificado TLS, contraseña del superusuario) | SSD |
| app | `/var/lib/sqsolve` | SQLite del plano de control, secretos de sqsolve (`secrets/`), certificado del panel, copias previas a migraciones | SSD |
| respaldos | `/backups` | Respaldos, manifiestos, copias del plano de control (`_system`), importaciones | Otro disco o pool |

El socket de PostgreSQL usa un volumen con nombre interno que no guarda datos.

**Por qué esta separación.** Base de datos y respaldos deben vivir en discos distintos: perder un
disco no puede llevarse las dos cosas. La aplicación va aparte porque su contenido (secretos y
catálogo) no debe copiarse junto con los respaldos que salen del edificio. La contraseña del
superusuario vive con el clúster que protege: quien tiene ese volumen ya tiene los datos.

**Alternativas.** Un solo volumen (simple, pero mezcla datos con respaldos); los seis originales
(precisos, pero confusos de mapear).

**Consecuencias.** El directorio `sqsolve/` del volumen db es setgid y del grupo de PostgreSQL,
para que los archivos que sqsolve reescribe sigan siendo legibles por el servidor. El kit de
recuperación sigue siendo necesario para recuperar contraseñas en otro servidor.

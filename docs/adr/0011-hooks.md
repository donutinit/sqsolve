# ADR 0011. Hooks y automatización externa

**Estado:** Aceptado (2026-10-18).

**Contexto.** Se piden acciones antes y después de operaciones. Studio Server ejecuta scripts
BASH dentro del mismo contenedor que la base de datos, con acceso a todo.

**Decisión.** 1.0: webhooks firmados y notificaciones; ningún código del usuario corre dentro de
sqsolve. 1.1: hooks ejecutables en el contenedor opcional `sqsolve-hooks`, que no monta secretos
ni el socket de PostgreSQL, ve los respaldos en solo lectura, recibe el evento por la entrada
estándar y tiene tiempo máximo. Los hooks solo se agregan desde el sistema de archivos del host,
nunca desde la interfaz.

**Alternativas.** Hooks en el contenedor de sqsolve con usuario aparte (requiere capacidades para
cambiar de usuario); scripts subidos por la interfaz (convierte una sesión robada en ejecución de
código).

**Consecuencias.** Subir respaldos a la nube con un script propio llega en 1.1; en 1.0 se cubre
con restic (también 1.1) o con la replicación del almacenamiento del host.

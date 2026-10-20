# Runbook: recuperar una instalación en otro servidor

Cuándo: murió el servidor o el disco de `db` y `app`, y tienes la copia del volumen de respaldos
y el kit de recuperación.

Necesitas: la carpeta `respaldos` completa (incluida `_system/`) y el archivo
`sqsolve-recuperacion-<instalación>.txt` con la clave maestra.

1. En el servidor nuevo, crea `db` y `app` vacíos y monta la copia de los respaldos como volumen
   `respaldos`. Usa el mismo `deploy/compose.yaml` y la misma versión de imagen, o una más nueva
   de la misma serie.
2. `docker compose up -d`. Esperado en `docker compose logs sqsolve-init`: reconoce el marcador
   del volumen y adopta la identidad de la instalación anterior.
3. Abre `https://<ip-nueva>:9432`. Esperado: el asistente ofrece "Recuperar una instalación
   existente".
4. Escribe el código de instalación del registro y pega la clave maestra del kit. Esperado:
   sqsolve restaura su catálogo desde `_system/` y se reinicia.
5. Entra con tu usuario de antes. Cada biblioteca aparece como "La base no existe en el
   servidor": en cada una, "Recrear desde el último respaldo". Esperado: tarea de restauración
   por biblioteca y estado Activa al terminar.
6. Las estaciones conservan su contraseña (la clave maestra es la misma). Solo cambia la
   dirección del servidor si cambió: Ajustes, "Dirección que usan las estaciones", y en Resolve
   vuelve a agregar la biblioteca con la IP nueva.

Sin kit: instala desde cero, `docker compose exec sqsolve sqsolve catalog rebuild --wait` para
ver los respaldos, y restáuralos como bibliotecas nuevas; las estaciones necesitan contraseñas
nuevas.

Cómo saber que terminó bien: `docker compose exec sqsolve sqsolve doctor` sin avisos; la página
Salud en verde; un proyecto abre en Resolve desde cada biblioteca.

# Runbook: actualizar sqsolve

Cuándo: sale una versión nueva. Con la etiqueta `0.1` del compose llegan los arreglos de la
serie; para cambiar de serie (`0.2`) se edita la línea `x-sqsolve`.

1. Confirma en el panel que cada biblioteca tiene un respaldo verificado reciente (línea de
   protección en verde) o lanza "Respaldar ahora". Esperado: ningún ámbar.
2. Lee la sección de la versión en `CHANGELOG.md`: dice si hay migración del catálogo y cómo
   volver atrás.
3. Docker Compose:

   ```
   docker compose pull
   docker compose up -d
   docker compose logs -f sqsolve
   ```

   TrueNAS: la app muestra "Update available" (hasta 24 horas después de publicada la imagen,
   porque TrueNAS revisa una vez al día) y el botón Update hace lo mismo. Desde la consola del
   NAS: `sudo midclt call -j app.pull_images sqsolve '{"redeploy": true}'`.

4. Esperado en el registro: `sqsolve-init` termina, `sqsolve listo` con la versión nueva y, si
   hubo migración, una copia previa del catálogo en `app/state-backups/`.
5. Entra al panel: pie de página con la versión nueva; Salud en verde; `sqsolve doctor` sin
   avisos.

Volver atrás: pon la versión anterior exacta en `x-sqsolve` (por ejemplo `0.1.4`) y `docker
compose up -d`. Si la versión nueva migró el catálogo y la anterior no arranca, restaura la copia
de `app/state-backups/` con el servicio detenido.

# Runbook: restaurar un respaldo y revertir

Cuándo: se borró o se rompió un proyecto, o toda la biblioteca, y hay un respaldo verificado.

## A. Recuperar un proyecto sin tocar la biblioteca

1. Respaldos, abre el respaldo de la fecha que quieres. Verificación debe decir "Suma e índice
   correctos" o "Restauración de prueba correcta".
2. "Exportar .drp y .drx", "Preparar la exportación". Espera a que la tarea termine (la página
   de la tarea lo muestra; un respaldo de varios GB tarda minutos).
3. Vuelve a la página de exportación: descarga el `.drp` del proyecto o los `.drx` de los
   grados. Impórtalos en Resolve. Esperado: Resolve abre el proyecto o aplica el grado.
4. "Cerrar y borrar la base temporal".

Si prefieres tener la biblioteca entera de ese día al lado de la actual: "Restaurar", "Como
biblioteca nueva", con otro identificador. Esperado: aparece como Activa con su propia
contraseña; conéctala desde Resolve con Connect.

## B. Reemplazar la biblioteca por un respaldo

1. Pide que cierren Resolve en todas las estaciones de esa biblioteca, o marca "cerrar sesiones"
   en el paso 3.
2. Respaldos, abre el respaldo, "Restaurar", "Reemplazar la biblioteca actual".
3. Escribe el identificador para confirmar. Esperado: una tarea con pasos respaldo previo,
   restaurar en base aparte, comparar con el manifiesto, intercambiar. Termina en "Correcta".
4. En Resolve, vuelve a abrir la biblioteca (Connect con los mismos datos). Esperado: los
   proyectos del respaldo.

Si la tarea falla en "intercambiar": la biblioteca original sigue en su lugar; revisa el registro
de la tarea y `sqsolve doctor`.

## C. Revertir un reemplazo

Hasta 7 días después (ajuste "Estado previo tras restaurar"):

1. Biblioteca, pestaña Zona de peligro, "Revertir". Escribe el identificador.
2. Esperado: tarea que intercambia de vuelta; la biblioteca queda como estaba antes del reemplazo.
3. Reconecta Resolve.

## Desde la consola

```
docker compose exec sqsolve sqsolve backup list <identificador>
docker compose exec sqsolve sqsolve restore <id-respaldo> --new <identificador-nuevo> --wait
docker compose exec sqsolve sqsolve restore <id-respaldo> --replace --confirm <identificador> --wait
```

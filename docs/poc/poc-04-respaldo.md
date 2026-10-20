# PoC-04. Formato de respaldo y restauración cruzada

Fecha: 2026-10-19. Servidor: mininas, sqsolve 0.1.6, PostgreSQL 18.6. Estación: Linux, Resolve
Studio 21.1.1.10.

## Resultados literales

**Paso 1, respaldo hecho por Resolve.** No hecho: el Project Manager no se automatizó y no hay
archivos `.resolve.backup` en la estación ni en el servidor de Diego para inspeccionar.

**Paso 2, Resolve restaura un respaldo de sqsolve.** No hecho (Project Manager).

**Paso 3, dos bibliotecas del mismo respaldo en una estación.** Hecho en parte. El respaldo
`01M5BDTRHX...` de `apolo` (`pg_dump` formato custom, verificado L1) se restauró con sqsolve como
biblioteca nueva `apolo_poc` (`pg_restore --no-owner --no-privileges --role=apolo_poc`), se
agregó a Resolve por `.dblist` y, tras reiniciar Resolve:

```
postgres project library apolo_poc at 192.168.50.172 version 18.6
Current project library changed to (apolo_poc : Network)
Current project pointer changed to (solo_en_copia) from project library (apolo_poc : Network)
```

Resolve abrió la copia, listó sus proyectos y creó `solo_en_copia` en ella mientras `apolo`
seguía en su lista. La comprobación de que `apolo` no recibió ese proyecto no llegó a
ejecutarse por el bloqueo de la API que describe `poc-01`.

Además, la verificación L2 de sqsolve (restauración de prueba de cada respaldo en una base
temporal y comparación con el manifiesto) ha pasado en todos los respaldos de `apolo` del día, y
la exportación desde respaldo (base temporal `sqsolve_export_*`) leyó esos mismos archivos.

**Paso 4, compresión.** No hecho (requiere restaurar desde Resolve).

## Decisiones que habilita

- Un respaldo de sqsolve restaurado por sqsolve es una biblioteca que Resolve 21.1.1 abre y
  usa: la cadena respaldo, restauración como nueva y apertura en Resolve queda demostrada.
- Que Resolve acepte directamente el archivo de sqsolve en su propio "Restore", y qué formato
  produce su "Backup", sigue REPORTADO: se prueba cuando Diego tenga una hora con el Project
  Manager. Hasta entonces, el README no afirma compatibilidad con los respaldos propios de
  Resolve.

## Estado

VERIFICADO: la restauración de sqsolve produce bibliotecas que Resolve abre. REPORTADO: el
intercambio de archivos con el "Backup" y "Restore" de Resolve.

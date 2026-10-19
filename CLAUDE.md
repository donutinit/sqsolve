# Instrucciones para agentes en sqsolve

Lee esto completo antes de tocar el repo. Las reglas globales de `~/.claude/CLAUDE.md` y
`~/dev/README.md` siguen aplicando.

## Estado del proyecto

Versiones 0.1 a 0.6 implementadas (2026-10-18) por encargo de Diego, antes de correr las
pruebas de concepto con Resolve. La arquitectura está en `docs/` y el avance en
`docs/21-tareas.md` §12. Lo que depende de una PoC (columna "Bloquea" de
`docs/02-pruebas-de-concepto.md`) está escrito según la hipótesis del plan y **no está validado
con Resolve real**: no lo extiendas ni lo des por bueno hasta que la PoC lo confirme.

## Cómo elegir y cerrar una tarea

1. Las tareas viven en `docs/21-tareas.md` con un ID `SQS-NNN`. Toma una cuyas dependencias
   estén cerradas.
2. Lee los documentos que la tarea cita en "Referencias" y los ADR relacionados.
3. Implementa solo lo que pide la tarea. Si descubres que una decisión no se sostiene, no la
   cambies en silencio: escribe un ADR nuevo con estado "Propuesto" y avísale a Diego.
4. La tarea se cierra cuando cumple todos sus criterios de aceptación y la "definición de
   terminado" global de abajo.

## Definición de terminado (todas las tareas)

- `make check` pasa: formato, `go vet`, `staticcheck`, pruebas unitarias y `govulncheck`.
- Las pruebas de integración de los paquetes tocados pasan (`make test-integration`).
- Cada comportamiento nuevo tiene prueba. Cada error esperado tiene prueba de su mensaje y
  código.
- Ninguna operación destructiva sin respaldo previo, confirmación y entrada en la auditoría.
- La documentación afectada está actualizada en el mismo cambio.
- Sin dependencias nuevas fuera de las aprobadas en `docs/06-stack.md`. Agregar una requiere
  ADR y permiso de Diego.

## Reglas técnicas que no se negocian

- Las bases de datos de Resolve son una caja negra (ADR 0007): sqsolve no lee ni escribe
  tablas de Resolve salvo las consultas de catálogo y conteos descritos en
  `docs/07-bibliotecas-y-postgresql.md`.
- SQL siempre con parámetros. Los identificadores (nombres de base y de rol) pasan por la
  validación por expresión regular y por `pgx.Identifier.Sanitize`. Nunca concatenar texto
  del usuario en SQL.
- Procesos externos solo con `exec.CommandContext` y argumentos en arreglo, desde
  `internal/execx`. Nada de `sh -c`. Contraseñas por `PGPASSFILE` temporal, nunca en argumentos.
- Cancelar una tarea solo manda señales a procesos hijos que lanzó esa misma tarea. Nunca
  `pkill`, nunca señales a PIDs ajenos (en pruebas, simula `os.Process`).
- Secretos: nunca en logs, nunca en variables de entorno de larga vida, nunca en respuestas de
  la API sin reautenticación. El manejador de `slog` redacta campos marcados como secretos.
- Escrituras de archivos importantes: archivo `.partial`, `fsync`, `rename`, `fsync` del
  directorio (`internal/fsx`).

## Código

- Go estable vigente (ver `go.mod`), `CGO_ENABLED=0`. Identificadores y comentarios de código en
  inglés; documentación, interfaz y mensajes de commit en español.
- Paquetes bajo `internal/`. Sin frameworks web: `net/http`, `html/template`, `log/slog`.
- Interfaz: plantillas en el servidor, formularios estándar y `app.js` propio (ADR 0004). Sin
  htmx, sin JavaScript de terceros, sin Node ni npm en la compilación.
- Antes de la primera tarea que cambie la interfaz, corre la fase de patrones de
  `ui-consistency` (finding-patterns) y deja su lista de verificación en
  `docs/ui/checklist.md`.

## Texto en español

Sin rayas largas ni cortas, comillas rectas, nada de datos inventados: si falta un dato,
"Por confirmar".

## Commits

Mensaje de una línea, corto, en español. Sin trailers de ningún tipo. No hagas commit ni push
sin que Diego lo pida.

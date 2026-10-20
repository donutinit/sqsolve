# Contribuir

## Antes de empezar

Lee [`AGENTS.md`](AGENTS.md): ahí están las reglas que no se negocian (SQL con parámetros,
procesos externos solo desde `internal/execx`, nada destructivo sin respaldo, secretos fuera de
los registros) y la definición de terminado. Aplican igual a personas y a agentes.

## Entorno

- Go estable vigente (ver `go.mod`), `CGO_ENABLED=0`.
- Para las pruebas de integración, los binarios de PostgreSQL 18 en el `PATH` o en
  `SQSOLVE_TEST_PG_BIN`.
- Sin Node, npm ni dependencias fuera de las aprobadas en `docs/06-stack.md`.

## Ciclo

```
make check              # formato, vet, staticcheck, pruebas con -race, govulncheck
make test-integration   # pruebas contra PostgreSQL real
make errores            # regenera docs/manual/errores.md si agregaste o cambiaste un código SQS-
```

Primero la prueba que fija el comportamiento, luego el código. Cada error esperado tiene prueba
de su código y mensaje. La documentación afectada va en el mismo cambio.

## Decisiones

Si una decisión registrada en `docs/adr` no se sostiene, no la cambies en silencio: escribe un
ADR nuevo con estado "Propuesto" y proponlo.

## Estilo

Identificadores y comentarios de código en inglés; documentación, interfaz y mensajes de commit
en español, con comillas rectas y sin rayas. Nada de datos inventados: si falta un dato, "Por
confirmar". Commits de una línea, sin trailers.

## Publicar una versión

1. En `CHANGELOG.md`, mueve lo de "Sin publicar" a una sección `## [X.Y.Z] AAAA-MM-DD` con los
   pasos de actualización y de vuelta atrás. El release de GitHub toma sus notas de ahí; sin esa
   sección sale vacío.
2. Etiqueta `vX.Y.Z` y push: el CI publica la imagen firmada y el release con el compose.
3. Verifica la firma como dice `docs/17` §6 (cosign 3 o más nuevo) y actualiza la instalación de prueba.

## Cómo proponer un cambio

1. Una rama por tarea (`sqs-NNN-nombre-corto` si viene de `docs/21-tareas.md`).
2. `make check` y `make test-integration` en verde.
3. Pull request con qué cambia, por qué y cómo se probó.

Licencia: al contribuir aceptas que el código se publique bajo AGPL-3.0-only y la documentación
bajo CC BY-SA 4.0 (ADR 0001).

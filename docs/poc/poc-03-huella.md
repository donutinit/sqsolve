# PoC-03. Huella de esquema

Fecha: 2026-10-19. Servidor: mininas, sqsolve 0.1.5, PostgreSQL 18.6. Estación: Linux, Resolve
Studio 21.1.1.10, biblioteca `apolo` (136 tablas), operada con la API de scripting de Resolve.

## Procedimiento y resultados literales

1. Huella guardada antes: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
   136 tablas (API `GET /libraries/{id}`).
2. Uso normal desde Resolve: `CreateProject("poc03_huella")`, `CreateEmptyTimeline`,
   `SetSetting("timelineFrameRate", "25")`, `SaveProject`, `CloseProject`; después `LoadProject`,
   más guardados y `DeleteProject`.
3. "Respaldar ahora" (la huella se recalcula en cada respaldo): respaldo `01M5BDTRHX...` manual,
   `ok`, `l1_ok`.
4. Huella después: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
   136 tablas. Sin cambios.

La parte de "actualizar la biblioteca con una versión nueva de Resolve" no se pudo hacer: solo
hay 21.1.1 en la estación. Queda como REPORTADO que una actualización cambia el esquema.

## Decisiones que habilita

- La huella es estable con el uso normal (crear, editar, guardar, borrar proyectos y líneas de
  tiempo): un cambio de huella significa cambio de estructura, no de datos. SQS-240 queda
  validado en su mitad "no hay falsos positivos".
- Lo que falta (una actualización de versión que sí cambie el esquema) se registra cuando Diego
  actualice Resolve: la ventana de actualización de `docs/14` §5 sigue siendo obligatoria en la
  guía hasta entonces.

## Estado

VERIFICADO: estable con el uso. REPORTADO: cambia al actualizar Resolve.

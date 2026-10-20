# PoC-01. Ciclo de vida y privilegios mínimos

Fecha: 2026-10-19. Servidor: mininas, sqsolve 0.1.5 y 0.1.6, PostgreSQL 18.6. Estación: Linux,
Resolve Studio 21.1.1.10. Operación por la API de scripting de Resolve y, para agregar
bibliotecas sin pasar por el Project Manager, editando `~/.local/share/DaVinciResolve/configs/.dblist`
(formato `clave:ip:usuario:contraseña:base:QPSQL`, se lee al arrancar Resolve). Lo que exige el
botón "Create" o "Connect" del Project Manager no se automatizó.

## Resultados literales

**Paso 1, qué crea Resolve con "Create" (E1).** `apolo` la creó Diego desde el Project Manager
dentro de la ventana de inicialización de sqsolve. Estado final en el catálogo:

| Dato | Valor |
|---|---|
| Dueño de la base | `apolo` (el rol de la biblioteca) |
| Codificación, proveedor, locale | `UTF8`, builtin, `C.UTF-8` (hereda la plantilla del clúster) |
| Extensiones | solo `plpgsql` |
| Esquema `public` | 136 tablas, 2 secuencias, 444 índices, 11 funciones `plpgsql`, un solo dueño (`apolo`) |
| Funciones | `check_if_upgrade_allowed`, `del_orphan_still_on_ref_die`, `delete_stills_if_unreferenced`, `dropemptytable`, `find_if_still_is_referenced`, `increment_coversiontable_timestamp_on_update`, `insert_update_render_blob`, `print_notice`, `query_emptytables`, `query_vertabledata`, `unlock_vertable` |

sqsolve detectó la base, cerró la ventana, quitó `CREATEDB` al rol (`rolcreatedb = f`) y la activó.

**Paso 2, "Connect" a una base vacía (E0).** Biblioteca `labvacia` creada por sqsolve con la
estrategia "Base vacía" y agregada a Resolve con "Connect" (entrada en `.dblist`). Registro de
Resolve al arrancar:

```
Project library labvacia192.168.50.172 is incompatible with the running application (expecting db version 18.1.0.004)
```

Resolve no inicializa una base vacía al conectar: la marca incompatible y no la usa. E0 no
funciona con 21.1.1; para una base precreada haría falta que sqsolve cargara la estructura de
Resolve (E2, plantillas, 1.1). La opción "Base vacía" del panel queda marcada como no
compatible.

**Paso 3, base cuyo dueño es `sqsolve_admin`.** No hecho: requiere "Create" en el Project
Manager.

**Paso 4, uso diario sin `CREATEDB`.** Verificado durante todo el día con `apolo`: crear, abrir,
guardar, cerrar y borrar proyectos y líneas de tiempo con el rol sin `CREATEDB`.

**Paso 5, rol miembro (`apolo_poc__ana`).** La credencial se creó desde el panel y la entrada en
`.dblist` quedó lista, pero la batería se detuvo antes de esa prueba (la API de scripting dejó
de responder tras crear un proyecto en otra biblioteca). No hecho.

**Paso 6, conexiones por estación.** `pg_stat_activity` muestreado cada 2 s durante 40 s con un
proyecto abierto: 1 conexión. Sin proyecto abierto: 1. Al cambiar de biblioteca o cargar un
proyecto: ráfaga de 6 conexiones (`application_name` `Resolve_0007433F5FC0`) que se cierran
después. Durante un render: no medido.

**Paso 7, orden con acentos.** No concluyente: la API devuelve la lista con caché y no refleja
los proyectos recién creados.

**Paso 8, Resolve 20.3.2.** No disponible en la estación.

## Decisiones que habilita

- Estrategia de creación: **E1 (Resolve crea la base) es la única soportada en 1.0.** El dueño de
  la base es el rol de la biblioteca; sqsolve administra con `sqsolve_admin` por membresía, como
  está implementado.
- Privilegios mínimos en uso diario: `LOGIN` sin `CREATEDB`, confirmado.
- `max_connections`: una estación en reposo usa 1 conexión y hasta 6 en ráfagas; el valor por
  defecto de sqsolve sobra para un estudio pequeño.
- Credenciales por persona (SQS-700): pendiente de prueba con Resolve.

## Estado

VERIFICADO: pasos 1 (resultado final), 2 y 4, y el reposo del paso 6. NO HECHO: pasos 3, 5, 7, 8
y el render del 6; necesitan el Project Manager o una segunda versión de Resolve.

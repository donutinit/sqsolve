# 16. Plan de pruebas

## 1. Niveles

| Nivel | Qué cubre | Herramienta | Dónde corre |
|---|---|---|---|
| Unitarias | Funciones puras y servicios con dobles | `go test` | Cada cambio, CI |
| Propiedades y fuzzing | Validadores de identificadores, analizador de cron, retención, cifrado | `testing.F`, generadores propios | CI (fuzz corto), nocturno (fuzz largo) |
| Integración | Servicios contra PostgreSQL 18 real, SQLite real, `pg_dump`/`pg_restore` reales | `testcontainers-go` o el Compose de pruebas | CI |
| Contrato | Respuestas de la API contra `api/openapi.yaml` | `kin-openapi` | CI |
| Extremo a extremo | Flujos de la interfaz en navegador | `chromedp` contra el Compose completo | CI |
| Fallos y recuperación | Cortes, disco lleno, volúmenes ausentes, PostgreSQL caído | Scripts Go que controlan Docker en la máquina de CI | CI (nocturno) |
| Seguridad | Análisis estático, vulnerabilidades, escaneo web, autorización | `gosec`, `govulncheck`, OWASP ZAP, pruebas de matriz | CI |
| Compatibilidad con Resolve | Comportamiento real de Resolve, incluido el kit de exportación (`22` §6) | Manual guiado (`14-compatibilidad.md` §4) | Estaciones de Diego |
| Aceptación | Criterios AC-01 a AC-11 | Guion §8, parte automática y parte manual | Antes de cada versión |
| Rendimiento | Tiempos de página y respaldo | `go test -bench`, cargas sintéticas | Antes de cada versión |

Resolve no puede correr en CI (requiere GPU y licencia). Por eso el diseño evita depender de su
comportamiento en las rutas automatizables y concentra lo que sí depende en PoC y guiones
manuales.

## 2. Pruebas unitarias obligatorias

- `internal/pg/ident`: aceptar y rechazar slugs y nombres de rol; propiedad: todo nombre aceptado
  cabe en 63 bytes y no contiene `__` salvo los auxiliares generados.
- `internal/backup/retention`: casos de tabla (sin respaldos, solo fijados, cambios de horario,
  años bisiestos, `min_verified` mayor que los disponibles) y propiedades: nunca se borra un fijado,
  nunca queda una biblioteca activa sin verificado si existía uno, el resultado no depende del
  orden de entrada.
- `internal/scheduler`: próxima ejecución con zonas horarias y horario de verano; ejecuciones
  perdidas.
- `internal/auth/policy`: matriz completa rol por acción por recurso.
- `internal/secrets`: cifrar y descifrar, rechazo con datos adicionales cambiados, rotación de clave.
- `internal/auth/totp`: vectores del RFC 6238.
- `internal/audit`: cadena válida, detección de alteración en cualquier posición.
- `internal/pgconf`: `pg_hba.conf` generado para combinaciones de bibliotecas, credenciales y CIDR;
  que las líneas de rechazo de roles administrativos vayan siempre antes.
- `internal/fsx`: escritura atómica (simulando fallos entre pasos).
- `internal/errs`: cada código tiene mensaje en ambos idiomas y página de documentación.

## 3. Pruebas de integración

Contra PostgreSQL 18 en contenedor, con la configuración generada por sqsolve:

- Arranque desde cero: secretos, `sqsolve_admin`, asistente.
- Bibliotecas: crear con E1 simulando a Resolve (un cliente que, con el rol de la biblioteca y
  `CREATEDB`, crea la base y unas tablas de ejemplo propias del proyecto de pruebas); la ventana
  detecta, quita `CREATEDB` y activa. E2 desde plantilla. Mantenimiento. Eliminar y recuperar.
- Seguridad de red: un cliente TCP como `sqsolve_admin` y `postgres` es rechazado; un rol de
  biblioteca no entra a otra biblioteca.
- Respaldos: crear, verificar L1 y L2, conteos coinciden; alterar un byte del archivo y comprobar
  que L1 falla.
- Restauración como nueva y por intercambio con sesiones abiertas (clientes de prueba conectados);
  reversión.
- Retención real sobre archivos.
- Importar un archivo de `pg_dump` hecho con otro dueño y sin manifiesto.
- Catálogo: reconstrucción desde manifiestos tras borrar SQLite.
- Migraciones de SQLite desde cada versión publicada anterior (fixtures guardados por versión).

Los datos de prueba nunca incluyen esquemas de Resolve: se usa un esquema sintético propio que
imita el patrón (muchas tablas, algunos `bytea` grandes, índices). El esquema sintético de la
exportación experimental puede repetir los nombres de tablas y columnas del formato `.drp` (`22`
§3), nunca el esquema completo de Resolve ni datos reales.

## 4. Pruebas de fallos y recuperación

Cada escenario termina comprobando los invariantes: la biblioteca activa existe y abre; ningún
respaldo `ok` se perdió; no quedan `.partial`, `__restore_*` ni `sqsolve_verify_*` huérfanos tras el
siguiente arranque; el catálogo coincide con los archivos.

| Escenario | Cómo se provoca |
|---|---|
| Corte durante `pg_dump` | `docker kill -s KILL` de sqsolve y de PostgreSQL en un punto aleatorio del paso `dump` |
| Corte entre los dos renombres | Punto de inyección de fallos en el paso `swap_2` (compilación con etiqueta `faultinject`) |
| Corte durante verificación L2 | `docker kill` durante `pg_restore` |
| Disco lleno | Volumen `backups` en un tmpfs pequeño |
| Volumen ausente | Arrancar sin montar `backups` |
| PostgreSQL caído durante respaldo programado | `docker stop postgres` |
| PostgreSQL que no arranca | Configuración corrupta a propósito |
| Reloj que salta | Cambiar la hora del contenedor de pruebas (librería de reloj inyectable) |
| SQLite corrupto | Truncar el archivo; sqsolve debe negarse a arrancar y ofrecer restaurar la copia de `_system` |

La función que termina procesos en pruebas usa un `os.Process` simulado o solo hijos propios.
Ninguna prueba manda señales a PIDs que no lanzó.

## 5. Pruebas de seguridad

- `gosec` y `govulncheck` sin hallazgos altos para fusionar.
- OWASP ZAP en modo "baseline" contra el Compose de pruebas autenticado; sin alertas altas.
- Matriz de autorización probada contra la web, la API y la CLI (misma tabla, tres adaptadores).
- CSRF: cada `POST` sin token falla.
- Encabezados: CSP, `X-Content-Type-Options`, `Referrer-Policy`, HSTS cuando hay certificado
  propio.
- Inyección: fuzzing de entradas de slugs, nombres visibles, CIDR, expresiones cron, rutas de
  importación (incluidos `..`, enlaces simbólicos y caracteres nulos).
- Secretos en salida: búsqueda de secretos marcados en registros, respuestas, archivos de
  diagnóstico y eventos.
- Revisión manual de seguridad antes de 1.0 y en cada versión mayor, siguiendo `08-seguridad.md`.

## 6. Compatibilidad

- Automatizable: matriz de PostgreSQL 16, 17 y 18 para el modo externo (2.0); `pg_restore` de
  respaldos generados por versiones anteriores de sqsolve.
- Manual: guion de `14-compatibilidad.md` §4 con Resolve en Windows o macOS y Linux. Resultados en
  `docs/compat/`.

## 7. Rendimiento

- Página de bibliotecas y catálogo con 50 bibliotecas y 5000 respaldos: p95 menor a 300 ms
  (RNF-USA-05).
- Arranque con catálogo grande: menor a 5 s.
- Respaldo y restauración: se registran tiempos por tamaño en cada versión para detectar
  regresiones; los valores de referencia salen de PoC-10.

## 8. Guion de aceptación de 1.0

Entorno: un servidor Linux y un NAS con Docker; dos estaciones con Resolve 21.1.x (al menos una
Studio); red local; luego sin salida a Internet.

| Paso | Criterio | Verificación |
|---|---|---|
| 1 | AC-01 | Instalar con el YAML en ambos entornos; cronometrar; anotar cada edición del YAML |
| 2 | AC-02 | Asistente completo; reiniciar; el asistente no reaparece |
| 3 | AC-03 | Crear `aceptacion_a`; inicializar desde la estación 1; crear proyecto |
| 4 | AC-04 | Una persona sin experiencia previa conecta la estación 2 siguiendo solo el panel |
| 5 | AC-05 | Ambas ven el proyecto; el panel muestra dos estaciones |
| 6 | AC-06 | Activar colaboración; bloqueo de bins y chat entre estaciones |
| 7 | AC-07 | Programar cada hora con retención corta durante 72 h; revisar catálogo y retención |
| 8 | AC-08 | Modificar el proyecto; restaurar como copia y abrirla; reemplazar y abrir; revertir |
| 9 | AC-09 | Reiniciar el host; actualizar a una versión candidata; cortar la energía durante un respaldo |
| 10 | AC-10 | Repetir 3, 7 y 8 sin salida a Internet |
| 11 | AC-11 | Borrar todo salvo `backups` y el kit; reinstalar en otro equipo y recuperar |

El resultado se registra en `docs/aceptacion/1.0.md` con versiones, fechas y evidencias
(capturas y salidas de `sqsolve doctor`).

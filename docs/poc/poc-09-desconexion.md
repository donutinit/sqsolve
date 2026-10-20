# PoC-09. Estados restringidos y desconexión forzada

Fecha: 2026-10-19. Servidor: mininas, sqsolve 0.1.5, PostgreSQL 18.6. Estación: Linux, Resolve
Studio 21.1.1.10 con el proyecto `poc03_huella` abierto en la biblioteca `apolo`, operado con la
API de scripting; lo que Resolve mostró en pantalla no se observó.

## Procedimiento y resultados literales

| Paso | Acción en sqsolve | Resolve (API) |
|---|---|---|
| 1 | Mantenimiento ON sin cerrar sesiones (`ALLOW_CONNECTIONS false`) | `SaveProject` = `True`: la sesión abierta sigue trabajando |
| 2 | Mantenimiento OFF | `SaveProject` = `True` |
| 3 | Reemplazar `apolo` con su respaldo de minutos antes, marcando "cerrar sesiones" | Tarea `restore.replace` terminó `succeeded` (respaldo previo, restaurar aparte, comparar, intercambiar) |
| 4 | Inmediatamente después del intercambio | `GetCurrentProject` sigue devolviendo `poc03_huella`; `SaveProject` = `True`; `GetProjectListInCurrentFolder` lista los proyectos |
| 5 | `SetCurrentDatabase(apolo)` y `LoadProject` otra vez | `True`, `True`, `SaveProject` = `True` |

Lectura: al cerrar sus sesiones, Resolve 21.1.1 **vuelve a conectar por sí solo** a la base del
mismo nombre, que ya es la restaurada, y sigue guardando sin avisar por la API. No se vio ningún
diálogo porque la estación no estaba a la vista.

## Decisiones que habilita

- El modo mantenimiento sin cerrar sesiones no interrumpe a quien ya está dentro; para sacar a
  alguien hay que marcar "cerrar sesiones". Como estaba previsto.
- El reemplazo por intercambio con cierre de sesiones funciona con Resolve abierto. **Riesgo
  nuevo:** como Resolve reconecta solo y conserva el proyecto en memoria, un editor que tenía el
  proyecto abierto puede guardar encima de lo recién restaurado sin enterarse. La guía debe
  pedir que cierren el proyecto antes de reemplazar y lo reabran después; sqsolve ya lo pide en
  el formulario y ahora también en el README y `docs/11` §7.2. Una protección técnica (por
  ejemplo, una contraseña nueva al reemplazar) queda para 1.1.

## Estado

VERIFICADO por la API de scripting. Pendiente de observar en pantalla en la aceptación (AC-08).

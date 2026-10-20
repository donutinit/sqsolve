# 14. Compatibilidad

## 1. Política de soporte

| Nivel | Significado |
|---|---|
| **Soportado** | Probado con el guion de compatibilidad (§4) en cada versión de sqsolve; los fallos son errores a corregir |
| **Verificado** | Probado al menos una vez con resultado correcto; puede romperse sin aviso |
| **Esperado** | Debería funcionar por la documentación, sin prueba propia |
| **No soportado** | No funciona o no se prueba; sqsolve avisa |

Regla: se soporta la familia mayor actual de Resolve y la última versión de la anterior que
Blackmagic declara compatible a nivel de biblioteca.

## 2. Matriz al 2026-10-18

### 2.1 DaVinci Resolve

| Versión | Nivel | Base de la decisión |
|---|---|---|
| 21.1.x | Soportado (pendiente de PoC-01, 02, 04) | Blackmagic declara bibliotecas en PostgreSQL 13+ y trae 18 [01 §4]. Exportación experimental verificada con 21.1.1 el 2026-10-19 (`22` §6). TLS: 21.1.1 en Linux negocia TLS 1.3 con `sslmode=prefer` y acepta el certificado autofirmado (2026-10-19, `02` PoC-06); `hostssl` obligatorio sin confirmar |
| 21.0.x | Esperado | Misma familia; se recomienda actualizar a 21.1.x |
| 20.3.2 | Soportado si PoC-02 pasa con PostgreSQL 18 | Bibliotecas compatibles con 21 según Blackmagic [01 §6] |
| 19.x, 18.x | Esperado como máximo, sin pruebas | Usaban PostgreSQL 13; sin garantía con 18 |
| 17 y anteriores | No soportado | Requieren PostgreSQL 11 o anterior, sin soporte de seguridad |

### 2.2 PostgreSQL

| Versión | Uso en sqsolve |
|---|---|
| 18.x | Única versión empaquetada en 1.x |
| 17.x | Perfil alternativo solo si PoC-02 muestra que alguna versión soportada de Resolve falla con 18 |
| 16.x o superior | Mínimo para el modo servidor externo (2.0), por la semántica de `CREATEROLE` [01 §12] |
| 15 o anterior | No soportado como servidor; sí como origen de migración (CU-30): `pg_dump` 18 lee servidores antiguos |

### 2.3 Entorno

| Elemento | Requisito |
|---|---|
| Docker Engine | 24 o superior con Compose v2 |
| Arquitecturas | `linux/amd64`, `linux/arm64` |
| Navegadores | Dos últimas versiones mayores de Firefox, Chromium (Chrome, Edge) y Safari |
| Estaciones | Las que soporte la versión de Resolve en uso (Resolve 21.1: macOS 15+ en Apple Silicon, Windows 10 Creators Update o Windows 11, Rocky Linux 8.6 según Blackmagic [01 ref. 1]) |

## 3. Cómo detecta sqsolve la versión de una biblioteca

1. **Versión declarada** por el administrador en el detalle de la biblioteca (texto libre
   validado, por ejemplo `21.1.1`).
2. **Huella de esquema** (`07` §9) comparada con el registro de huellas conocidas incluido en cada
   versión de sqsolve (`internal/compat/fingerprints.json`), que indica además qué exportaciones
   se verificaron con esa huella (`verified`).
3. Eventos `library.schema_changed` con huella anterior y nueva.

No se lee ninguna tabla de versión interna de Resolve: no está documentada.

## 4. Guion de compatibilidad (por versión nueva de Resolve)

Responsable: quien mantiene la matriz. Plazo objetivo: dentro de los 14 días posteriores a una
versión de Resolve; las beta se prueban solo si hay tiempo y se marcan como tales.

1. Leer las notas de versión buscando "project library", "PostgreSQL", "Project Server",
   "collaboration" y avisos de compatibilidad. Registrar citas en `docs/compat/<versión>.md`.
2. Biblioteca nueva con la estrategia vigente; crear proyecto, importar medios, editar,
   cerrar y reabrir.
3. Abrir una biblioteca de la versión anterior; registrar si Resolve pide actualizarla y la huella
   antes y después.
4. Respaldo de sqsolve, restauración como copia y por intercambio; abrir en Resolve.
5. Restaurar en Resolve un respaldo de sqsolve y en sqsolve un respaldo de Resolve (PoC-04).
6. Colaboración entre dos estaciones.
7. Si el módulo de llaves está activo: importar una llave generada.
8. Si la exportación experimental está activa: correr el kit de `22` §6 (exportar con sqsolve y
   con Resolve, comparar, importar) y registrar la huella con lo verificado.
9. Actualizar la matriz, el registro de huellas y las notas de versión de sqsolve.

## 5. Ventana de actualización de Resolve

Porque abrir proyectos en una versión nueva puede dejarlos inaccesibles para la anterior
[01 §6], sqsolve guía el proceso (CU-21, `09-interfaz-web.md` §4.5):

1. Elegir versión de destino y bibliotecas afectadas (todas por defecto).
2. Respaldos `pre_upgrade` con verificación L2 de cada biblioteca. La ventana no avanza si alguno
   falla.
3. Checklist para el estudio: todas las estaciones cierran Resolve; se actualiza una estación;
   abre cada biblioteca y acepta la actualización si se ofrece; se comprueba un proyecto; se
   actualizan las demás.
4. Cierre: comparación de huellas; la versión declarada se actualiza; los respaldos
   `pre_upgrade` quedan protegidos 30 días.

Volver atrás: restaurar los `pre_upgrade` (por intercambio) y reinstalar la versión anterior en
las estaciones.

## 6. Mezcla de versiones

- La colaboración exige la misma versión de Resolve en todas las estaciones (lo exige Blackmagic;
  la guía lo repite).
- sqsolve muestra, por biblioteca, los `application_name` de las sesiones si Resolve los informa
  (PoC-01 paso 6); si incluyen versión, la interfaz avisa cuando hay versiones distintas
  conectadas a la misma biblioteca.

## 7. Compatibilidad de sqsolve consigo mismo

- Respaldos: cualquier versión de sqsolve lee manifiestos y archivos de versiones anteriores.
- SQLite: migraciones hacia adelante; volver a una versión anterior requiere la copia previa.
- API: `v1` estable durante toda la serie 1.x.

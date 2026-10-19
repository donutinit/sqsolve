# 06. Stack tecnológico

Criterios, en este orden: riesgo de pérdida de datos, simplicidad operativa, superficie de
cadena de suministro, mantenibilidad a 5 años, rendimiento suficiente, comunidad.

## 1. Lenguaje del backend: **Go**

| Opción | A favor | En contra |
|---|---|---|
| **Go** | Binario estático único; arranque y memoria mínimos; excelente manejo de procesos, contextos y cancelación; compilación cruzada trivial a arm64; biblioteca estándar suficiente para HTTP, plantillas, TLS, criptografía y logs; ecosistema PostgreSQL maduro (pgx) | Menos expresivo para modelos de dominio ricos; manejo de errores verboso |
| Python (FastAPI) | Rápido de escribir; muchos colaboradores potenciales | Intérprete y dependencias en la imagen; empaquetado y reproducibilidad más frágiles; más memoria |
| TypeScript (Node) | Mismo lenguaje en interfaz y servidor | Cadena de suministro de npm amplia y riesgosa; el proyecto quiere evitar Node en la compilación |
| Rust | Seguridad de memoria, binario único | Desarrollo más lento; menos gente que pueda mantenerlo; el problema no exige ese rendimiento |

Versión: `go.mod` declara `go 1.26` como mínimo (la del sistema en lizeth). CI compila con la
última versión de parche de la rama estable vigente (1.27 al 2026-10-18).

## 2. Interfaz: **plantillas en el servidor** (sin htmx, ver ADR 0004)

| Opción | A favor | En contra |
|---|---|---|
| **`html/template` + htmx vendorizado + CSS propio** | Sin toolchain de Node; un solo binario con los recursos embebidos; escape automático contra XSS; progreso en vivo con SSE; poco JavaScript que auditar | Interacciones muy ricas cuestan más; plantillas sin tipado |
| SPA (Svelte, React, Vue) | Interacción rica; separación limpia de API | Toolchain de npm, cientos de paquetes, dos aplicaciones que mantener |
| `templ` (plantillas tipadas para Go) | Tipado | Herramienta de generación extra; no hace falta para el tamaño de esta interfaz |

La interfaz no consume la API REST: ambas llaman a los mismos servicios. Así la API no se
deforma para la interfaz y la interfaz no paga latencia extra.

JavaScript permitido: un archivo propio pequeño (`app.js`, menos de 300 líneas) para copiar al
portapapeles, confirmaciones tecleadas y progreso en vivo. Sin `eval`, sin scripts en línea. Al
implementar se descartó htmx: formularios con POST, redirección y GET bastan. El tema sigue al
sistema.

## 3. Estado del plano de control: **SQLite**

| Opción | A favor | En contra |
|---|---|---|
| **SQLite (modernc.org/sqlite, puro Go)** | Independiente de PostgreSQL; un archivo; transacciones; copias en caliente con `VACUUM INTO`; sin servidor | Una sola escritura a la vez (suficiente aquí) |
| Base `sqsolve` dentro del mismo PostgreSQL | Un solo motor | Si PostgreSQL falla o se restaura, el panel pierde su estado justo cuando hace falta; mezcla plano de control y de datos |
| Archivos JSON | Simple | Sin transacciones ni consultas; corrupción ante cortes |

`modernc.org/sqlite` evita CGO y mantiene la compilación estática y cruzada.

## 4. Base de datos de bibliotecas: **PostgreSQL 18, imagen oficial**

| Opción | A favor | En contra |
|---|---|---|
| **`postgres:18.x-trixie` de Docker Official Images, fijada por digest** | Mantenida por la comunidad de Docker y PostgreSQL; actualizaciones de seguridad rápidas; es la que usan los catálogos de NAS | Hay que pasar la configuración por archivos y `-c` |
| PostgreSQL 13 como Blackmagic recomendaba para 18 a 20 | Coincide con instalaciones viejas | Sin soporte desde 2025-11-13 [01 §12] |
| Imágenes de terceros empaquetadas | Configuración cómoda | Añaden un proveedor intermedio con su propia política de distribución |
| PostgreSQL compilado por nosotros | Control total | Mantenimiento de seguridad propio sin necesidad |

Detalle y alternativas por versión en ADR 0006.

## 5. Respaldos

| Necesidad | Elegido | Alternativas evaluadas |
|---|---|---|
| Respaldo por biblioteca, restaurable en Resolve | `pg_dump` formato custom orquestado por sqsolve | `prodrigestivill/postgres-backup-local` (sin verificación ni catálogo ni restauración segura); scripts cron (sin estado ni interfaz) |
| Copia externa cifrada (1.1) | **restic** (BSD-2, binario único, cifrado y deduplicación por defecto, `restic check`) | rclone (sin cifrado por defecto ni verificación de repositorio); Borg (requiere servidor Borg por SSH); Kopia (válido, menos difundido en NAS) |
| Recuperación a un punto en el tiempo (2.0) | **pgBackRest** (MIT) | WAL-G (válido; pgBackRest tiene mejor verificación y documentación); Barman (Python, pensado para servidores dedicados) |
| Cifrado de archivos sueltos (1.1) | **age** (`filippo.io/age`, BSD-3) | GPG (complejo de operar) |

## 6. Programación de tareas

| Opción | A favor | En contra |
|---|---|---|
| **Programador dentro del proceso + cola en SQLite** | Estado visible en la interfaz; ejecuciones perdidas controladas; sin otro servicio | Hay que escribirlo bien (alcance acotado y probado) |
| cron dentro del contenedor (como Studio Server con `/etc/cron.d`) | Conocido | Otro proceso supervisado; estado invisible; difícil de probar |
| Programador del host (cron del NAS, timers de systemd) | Ya existe | Configuración fuera de la app; no portable |

Expresiones cron: `github.com/adhocore/gronx` (MIT) para validar y calcular la siguiente
ejecución con zona horaria. Alternativa si se abandona: `github.com/robfig/cron/v3` (MIT), solo su
analizador.

## 7. Notificaciones

Implementación propia de tres canales (webhook con HMAC, SMTP con `net/smtp` y STARTTLS o TLS
implícito, ntfy por HTTP). Se descartó Apprise porque metería Python en la imagen para tres
protocolos sencillos.

## 8. Autenticación

- Contraseñas: Argon2id de `golang.org/x/crypto/argon2`.
- TOTP: implementación propia de RFC 6238 (unas 60 líneas, probada con los vectores del RFC).
- OIDC (2.0): `github.com/coreos/go-oidc/v3` (Apache-2.0) con `golang.org/x/oauth2`.
- WebAuthn (2.0): `github.com/go-webauthn/webauthn` (BSD-3), a evaluar en su ADR.

## 9. Lista cerrada de dependencias de Go para 1.0

| Módulo | Licencia | Uso |
|---|---|---|
| `github.com/jackc/pgx/v5` | MIT | Cliente PostgreSQL, `Identifier.Sanitize` |
| `modernc.org/sqlite` | BSD-3 | SQLite sin CGO |
| `golang.org/x/crypto` | BSD-3 | Argon2id, XChaCha20-Poly1305 |
| `github.com/adhocore/gronx` | MIT | Expresiones cron |

Solo en pruebas: `github.com/testcontainers/testcontainers-go` (MIT), `github.com/chromedp/chromedp`
(MIT), `github.com/getkin/kin-openapi` (MIT).

Sin recursos de terceros vendorizados: estilos, scripts y la marca SVG son propios
(`internal/web/static`).

Cualquier módulo nuevo requiere ADR, versión publicada hace al menos 7 días, revisión de
licencia y del árbol de dependencias transitivas (`go mod graph`).

## 10. Imagen y empaquetado

- Imagen de sqsolve `FROM postgres:18.x-trixie@sha256:...` (ADR 0016) para garantizar que
  `pg_dump` y `pg_restore` son de la misma versión mayor que el servidor y compartir capas con
  el contenedor de PostgreSQL.
- Compilación del binario en una etapa `golang:<versión>-trixie` fijada por digest, con
  `CGO_ENABLED=0 -trimpath -buildvcs=true`.
- Despliegue: Docker Compose v2. Sin Helm, sin Kubernetes, sin catálogos de NAS específicos.

## 11. Herramientas de desarrollo y CI

| Herramienta | Uso |
|---|---|
| `make` | Punto de entrada único: `make check`, `make test-integration`, `make image` |
| `gofmt`, `go vet`, `staticcheck` | Calidad |
| `govulncheck` | Vulnerabilidades conocidas en dependencias y biblioteca estándar |
| `gosec` | Análisis estático de seguridad |
| Docker Buildx | Imágenes multiarquitectura |
| `syft` | SBOM en SPDX y CycloneDX |
| `cosign` | Firma de imágenes y atestaciones |
| OWASP ZAP (imagen Docker) | Escaneo de la interfaz en CI |

CI escrito para GitHub Actions con sintaxis compatible con Forgejo Actions, para poder mudar o
duplicar la integración a una forja libre sin reescribirla.

# 17. Control de versiones, CI/CD y publicación

## 1. Repositorio

- Un solo repositorio: código, documentación, despliegue, OpenAPI, pruebas.
- Forja principal: Por confirmar (GitHub bajo la cuenta de Diego es lo previsto). Réplica en una
  forja libre (Codeberg) para no depender de una sola plataforma.
- Estructura:

```
cmd/sqsolve/            punto de entrada
internal/               paquetes (ver 21-tareas.md §1)
api/                    openapi.yaml y esquemas JSON
deploy/compose.yaml     Compose de referencia
deploy/lab/             Compose de laboratorio para PoC
docs/                   esta documentación, manuales, compatibilidad, ADR
test/                   integración, extremo a extremo, fallos, fixtures
Dockerfile
Makefile
```

## 2. Flujo de trabajo

- Rama principal `main` siempre publicable. Cambios en ramas cortas con revisión.
- Mensajes de commit de una línea en español, sin trailers.
- `main` protegida: CI verde obligatorio.
- Etiquetas `vX.Y.Z` firmadas para publicar.

## 3. Versionado

- SemVer. `1.0.0` es el MVP. Las versiones `0.x` son previas y pueden romper compatibilidad.
- Cambios que obligan a acción del administrador (nuevo volumen, variable obligatoria) solo en
  versiones mayores o, en 1.x, con valor por defecto que mantiene el comportamiento anterior.
- `CHANGELOG.md` en español, formato "Keep a Changelog", con secciones Agregado, Cambiado,
  Corregido, Seguridad, Compatibilidad.

## 4. Integración continua

| Trabajo | Disparo | Contenido |
|---|---|---|
| `check` | Cada push y PR | `gofmt -l`, `go vet`, `staticcheck`, `go test ./...` con `-race`, cobertura, `govulncheck`, `gosec` |
| `integration` | Cada push y PR | Pruebas de integración con PostgreSQL 18 |
| `contract` | Cada push y PR | Validación de la API contra OpenAPI |
| `e2e` | PR a `main` | `chromedp` contra el Compose completo |
| `image` | Cada push | Construir imagen amd64 y arm64 (sin publicar en PR) |
| `reproducible` | Nocturno y en etiquetas | Compilar dos veces en entornos limpios y comparar SHA-256 del binario |
| `faults` | Nocturno | Pruebas de fallos y recuperación |
| `fuzz` | Nocturno | Fuzzing largo |
| `zap` | Nocturno | OWASP ZAP baseline |
| `release` | Etiqueta `v*` | Todo lo anterior, publicar imágenes, SBOM, firmas, notas |

Implementado hoy (2026-10-18) en `.github/workflows`:

- `ci.yml`: trabajo `check` con el último parche de Go 1.26 (mínimo de `go.mod`) y de Go 1.27
  (el de la imagen): formato, `go vet`, staticcheck, pruebas con `-race` incluidas las de
  integración contra PostgreSQL 18 del repositorio PGDG, govulncheck y trabajo `smoke` (construye
  la imagen, levanta `deploy/compose.yaml` tal cual con rutas temporales, espera `/readyz`, corre
  `sqsolve doctor` y comprueba que los datos sobreviven a un reinicio).
- `image.yml`: en cada push a `main` y en etiquetas `v*`, imagen amd64 y arm64 en
  `ghcr.io/donutinit/sqsolve` con procedencia y SBOM de buildx. Etiquetas `edge` (main),
  `X.Y.Z` y `X.Y` (versiones) y `sha-<commit>`.

staticcheck corre por ahora solo con Go 1.26: la v0.8.1 no lee el formato de Go 1.27.2 y la
v0.8.2, que sí, se publicó el 2026-10-12 (se puede subir desde el 2026-10-19 por la regla de
7 días).

Pendientes de la tabla: contrato OpenAPI, `e2e` con navegador, `reproducible`, `faults`, `fuzz`,
`zap`, firmas con cosign y `release`.

Acciones usadas, todas fijadas por SHA de commit: `actions/checkout`, `actions/setup-go` y las
de Docker (`setup-buildx`, `login`, `metadata`, `build-push`). Todo lo que hace CI también se
puede hacer en local con `make`.

## 5. Compilación

- Binario: `CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags "-s -w -X
  main.version=$(VERSION)"`, con `SOURCE_DATE_EPOCH` igual a la fecha del commit.
- Imagen: Dockerfile multietapa; etapa de compilación `golang:<versión>-trixie@<digest>`; etapa
  final `postgres:18.x-trixie@<digest>` más el binario, usuario sin privilegios por defecto,
  `ENTRYPOINT ["sqsolve"]`. Etiquetas OCI (`org.opencontainers.image.source`, `.revision`,
  `.version`, `.licenses=AGPL-3.0-only`).
- `go mod vendor` permite compilar sin red; `make vendor-check` comprueba que `vendor/` coincide.

## 6. Publicación

- Registros: GHCR (Por confirmar) y una réplica (Quay o la forja libre). Etiquetas `X.Y.Z`,
  `X.Y`, `X`; `latest` solo apunta a la última estable, y la documentación nunca recomienda usarla.
- Firmas con `cosign` (clave del proyecto, sin depender del servicio público de transparencia
  para verificar) y atestación de procedencia.
- SBOM en SPDX y CycloneDX generados con `syft`, adjuntos a la versión y a la imagen.
- Archivos de la versión: binarios `sqsolve` para linux/amd64 y linux/arm64 (para la CLI y para
  instalaciones sin contenedor en el futuro), sumas SHA-256 firmadas, `compose.yaml` de esa
  versión, SBOM, notas.

## 7. Dependencias

- Go: solo las de `06-stack.md`. Actualizaciones agrupadas una vez al mes, con al menos 7 días
  desde su publicación (salvo arreglos de seguridad), revisando el diff de `go.sum`.
- Imágenes base: digest fijado; un trabajo semanal avisa de digest nuevos de
  `postgres:18.x-trixie`; la actualización pasa por la batería completa.
- Interfaz sin recursos de terceros (ADR 0004).

## 8. Lista de comprobación de una versión

1. CI completo en verde, incluidos nocturnos de la última semana.
2. Matriz de compatibilidad actualizada (`14-compatibilidad.md`).
3. Guion de aceptación (en versiones mayores y menores) o humo (en parches).
4. Notas de versión con pasos de actualización y vuelta atrás.
5. Etiqueta firmada; publicación automática; verificación manual de firmas y SBOM.
6. Actualizar `deploy/compose.yaml` en `main` con los nuevos digest.

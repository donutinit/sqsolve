# sqsolve

Servidor libre y autohospedado de bibliotecas de proyectos para DaVinci Resolve, administrable
desde el navegador. El nombre junta **SQ**L (PostgreSQL) y Re**solve**.

> Estado: **versión preliminar (0.x)**. Bibliotecas, accesos, respaldos, restauración, panel y
> API están implementados y probados contra PostgreSQL 18, pero la interacción con DaVinci
> Resolve todavía no se valida con estaciones reales (pruebas de concepto en
> [`docs/02-pruebas-de-concepto.md`](docs/02-pruebas-de-concepto.md)). No lo uses con proyectos
> de clientes hasta que esas pruebas pasen.

## Qué resuelve

DaVinci Resolve guarda sus proyectos en "bibliotecas de proyectos". Para que varias estaciones
compartan proyectos, la biblioteca vive en un servidor PostgreSQL. Blackmagic Design ofrece una
aplicación de escritorio (Project Server) para Windows y macOS; sqsolve es una alternativa
libre que corre en contenedores y se administra desde la web:

- crea y administra bibliotecas con un rol de PostgreSQL propio para cada una;
- entrega los datos de conexión para cada estación de trabajo;
- hace respaldos manuales y programados, con retención, verificación y restauración segura;
- registra eventos y auditoría, avisa cuando algo falla y funciona sin Internet.

sqsolve no sustituye el almacenamiento de medios ni es un editor: administra la base de datos de
proyectos y sus respaldos.

## Instalación

Una imagen de sqsolve (`ghcr.io/donutinit/sqsolve`) más la imagen oficial de PostgreSQL,
descritas en un único archivo: [`deploy/compose.yaml`](deploy/compose.yaml). Sirve tal cual en
cualquier Linux con Docker Compose y en NAS que aceptan Compose (TrueNAS con "Install via YAML",
Portainer, Unraid).

1. Copia `deploy/compose.yaml` y cambia las tres rutas de volúmenes y `TZ`.
2. `docker compose up -d`.
3. Busca el código de instalación en el registro: `docker compose logs sqsolve`.
4. Abre `https://<ip-del-servidor>:9432` y sigue el asistente. Guarda el kit de recuperación.

Tres volúmenes: base de datos (SSD), aplicación (SSD) y respaldos (de preferencia en otro disco).
Detalles en [`docs/12-despliegue.md`](docs/12-despliegue.md) y en el ADR 0018.

## Desarrollo

Go 1.26 o más nuevo. `make check` corre formato, `go vet`, staticcheck, pruebas y govulncheck.
Las pruebas de integración arrancan un PostgreSQL 18 temporal si encuentran sus binarios
(`SQSOLVE_TEST_PG_BIN`); `make test-integration` exige que estén.

## Documentación

Empieza por [`docs/README.md`](docs/README.md). Los agentes de programación leen además
[`AGENTS.md`](AGENTS.md).

## Licencia

Código: AGPL-3.0-only. Documentación: CC BY-SA 4.0. Ver
[`docs/adr/0001-licencia.md`](docs/adr/0001-licencia.md).

DaVinci Resolve y Blackmagic Design son marcas de Blackmagic Design Pty. Ltd. sqsolve es un
proyecto independiente, sin relación con Blackmagic Design ni con WireBear.

# sqsolve

Servidor libre y autohospedado de bibliotecas de proyectos para DaVinci Resolve, administrable
desde el navegador. El nombre junta **SQ**L (PostgreSQL) y Re**solve**.

> Estado: **planificación**. Este repositorio contiene por ahora solo la investigación, la
> arquitectura y el plan de implementación. No hay código todavía.

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

## Despliegue previsto

Una imagen Docker de sqsolve más la imagen oficial de PostgreSQL, descritas en un único archivo
de Docker Compose. Ese archivo sirve tal cual en cualquier Linux con Docker y en NAS que aceptan
Compose (por ejemplo, TrueNAS con "Install via YAML", Portainer o Unraid).

## Documentación

Empieza por [`docs/README.md`](docs/README.md). Los agentes de programación leen además
[`AGENTS.md`](AGENTS.md).

## Licencia

Código: AGPL-3.0-or-later. Documentación: CC BY-SA 4.0. Ver
[`docs/adr/0001-licencia.md`](docs/adr/0001-licencia.md).

DaVinci Resolve y Blackmagic Design son marcas de Blackmagic Design Pty. Ltd. sqsolve es un
proyecto independiente, sin relación con Blackmagic Design ni con WireBear.

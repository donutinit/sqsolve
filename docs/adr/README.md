# Registro de decisiones de arquitectura

Formato: contexto, decisión, alternativas, consecuencias. Estados: **Propuesto**, **Aceptado**,
**Aceptado, pendiente de PoC** (la decisión se mantiene salvo que la PoC indicada la refute),
**Reemplazado por NNNN**.

| ADR | Decisión | Estado |
|---|---|---|
| [0001](0001-licencia.md) | AGPL-3.0-only para el código, CC BY-SA 4.0 para la documentación | Aceptado |
| [0002](0002-topologia.md) | Dos contenedores (PostgreSQL oficial y sqsolve) más uno de inicialización, con Docker Compose | Aceptado |
| [0003](0003-go.md) | Backend en Go, binario único | Aceptado |
| [0004](0004-interfaz-servidor.md) | Interfaz renderizada en el servidor con formularios estándar, sin Node ni JavaScript de terceros | Aceptado (actualizado) |
| [0005](0005-sqlite.md) | Estado del plano de control en SQLite | Aceptado |
| [0006](0006-postgresql-18.md) | PostgreSQL 18 oficial con colación `builtin` | Aceptado, pendiente de PoC-01 y PoC-02 |
| [0007](0007-caja-negra.md) | Las bibliotecas de Resolve son una caja negra | Aceptado; acotado por 0019 |
| [0008](0008-roles.md) | Rol de control sin superusuario y un rol propietario por biblioteca | Aceptado, pendiente de PoC-01 |
| [0009](0009-respaldos-logicos.md) | Respaldos lógicos por biblioteca con `pg_dump` custom y manifiesto | Aceptado, pendiente de PoC-04 |
| [0010](0010-restauracion-intercambio.md) | Restaurar siempre en base nueva e intercambiar por renombre | Aceptado, pendiente de PoC-09 |
| [0011](0011-hooks.md) | Sin hooks ejecutables en 1.0; webhooks; hooks en contenedor aislado en 1.1 | Aceptado |
| [0012](0012-puerto-50059.md) | El servidor no publica el puerto 50059 | Aceptado, pendiente de PoC-07 |
| [0013](0013-resolvedbkey.md) | `.resolvedbkey` como módulo experimental | Aceptado, pendiente de PoC-05 |
| [0014](0014-secretos.md) | Secretos en archivos y cifrado de credenciales recuperables | Aceptado (ubicación según 0018) |
| [0015](0015-acceso-remoto.md) | Acceso remoto solo por VPN; 5432 nunca a Internet | Aceptado |
| [0016](0016-imagen-base.md) | Imagen de sqsolve basada en la imagen oficial de PostgreSQL | Aceptado |
| [0017](0017-version-mayor-postgresql.md) | Versiones mayores de PostgreSQL por migración lógica a un clúster nuevo | Aceptado |
| [0018](0018-tres-volumenes.md) | Tres volúmenes visibles: base de datos, aplicación y respaldos | Aceptado |
| [0019](0019-exportacion-experimental.md) | Exportación experimental de proyectos (`.drp`) y grados (`.drx`) leyendo un conjunto fijo de tablas | Propuesto |
| [0020](0020-identidad-visual.md) | Identidad visual "Nodo": símbolo, logotipo en trazos, paleta de marca solo en los puertos, íconos | Aceptado |
| [0021](0021-seguridad-proporcional.md) | Seguridad proporcional a LAN y VPN: sin reautenticación, contraseñas de 8, sesiones largas, HTTPS obligatorio, bloqueo de cuenta con desbloqueo por CLI | Aceptado |
| [0022](0022-respaldo-segun-actividad.md) | Respaldo según actividad: revisión cada N minutos y respaldo solo si la biblioteca cambió; un respaldo vigente no es atraso | Aceptado |

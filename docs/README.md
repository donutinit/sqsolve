# Plan maestro de sqsolve

Fecha de corte de la investigación: **2026-10-18**. Todo lo que dependa de versiones de DaVinci
Resolve, PostgreSQL o Docker se revisa en cada versión mayor de Resolve
(ver `14-compatibilidad.md`).

## Cómo leer estos documentos

| # | Documento | Para qué sirve |
|---|---|---|
| 01 | [Investigación](01-investigacion.md) | Cómo funciona Resolve con PostgreSQL, qué hace Studio Server, qué está verificado y qué no |
| 02 | [Pruebas de concepto](02-pruebas-de-concepto.md) | Experimentos que deben pasar antes de comprometer partes de la arquitectura |
| 03 | [Producto](03-producto.md) | Visión, alcance, personas, casos de uso y criterios de aceptación |
| 04 | [Requisitos](04-requisitos.md) | Requisitos funcionales y no funcionales con ID |
| 05 | [Arquitectura](05-arquitectura.md) | Vista general, componentes, flujos y principios |
| 06 | [Stack](06-stack.md) | Evaluación de alternativas y tecnologías elegidas |
| 07 | [Bibliotecas y PostgreSQL](07-bibliotecas-y-postgresql.md) | Roles, ciclo de vida, configuración del servidor y modelo de datos |
| 08 | [Seguridad](08-seguridad.md) | Modelo de amenazas, autenticación, autorización y secretos |
| 09 | [Interfaz web](09-interfaz-web.md) | Pantallas, flujos y patrones de interacción |
| 10 | [API y contratos](10-api.md) | API REST, eventos, webhooks, CLI y formatos internos |
| 11 | [Respaldos y automatización](11-respaldos-y-automatizacion.md) | Respaldo, verificación, retención, restauración, tareas y notificaciones |
| 12 | [Despliegue](12-despliegue.md) | Docker Compose, volúmenes, configuración, actualizaciones |
| 13 | [Observabilidad](13-observabilidad.md) | Registros, métricas, salud y diagnóstico |
| 14 | [Compatibilidad](14-compatibilidad.md) | Matriz de versiones y procedimiento ante versiones nuevas de Resolve |
| 15 | [Almacenamiento de medios](15-almacenamiento-de-medios.md) | Relación con el almacenamiento compartido, rutas y proxies |
| 16 | [Pruebas](16-pruebas.md) | Estrategia de pruebas por nivel |
| 17 | [CI/CD y publicación](17-ci-cd-y-publicacion.md) | Control de versiones, compilación, firma y publicación |
| 18 | [Documentación y licencia](18-documentacion-y-licencia.md) | Plan de manuales, licencia y marcas |
| 19 | [Riesgos](19-riesgos.md) | Registro de riesgos con mitigación |
| 20 | [Roadmap](20-roadmap.md) | MVP, versiones, fases, hitos y dependencias |
| 21 | [Tareas](21-tareas.md) | Desglose ejecutable por agentes con criterios de terminado |
| 22 | [Exportación de proyectos y grados](22-exportacion-de-proyectos-y-grados.md) | Formato `.drp` y `.drx`, reglas, kit de verificación (experimental, ADR 0019) |
| ADR | [Decisiones](adr/README.md) | Registro de decisiones de arquitectura |

## Convención de estados de evidencia

Toda afirmación que condiciona la arquitectura lleva uno de estos estados:

| Estado | Significado |
|---|---|
| **VERIFICADO** | Documentado por Blackmagic Design, PostgreSQL u otra fuente primaria, o comprobado por nosotros |
| **REPORTADO** | Lo afirma soporte de Blackmagic en foros o una fuente de la comunidad confiable; falta comprobarlo |
| **VIABLE-PENDIENTE** | Técnicamente viable según la documentación de PostgreSQL o Docker, pero no probado con Resolve |
| **INVESTIGAR** | No hay información suficiente; requiere una prueba de concepto antes de diseñar sobre ello |
| **DESCARTADO** | Imposible, no recomendable o fuera de los principios del proyecto |

Cuando una PoC confirma o refuta algo, se actualiza el estado aquí, en `01-investigacion.md` y
en `14-compatibilidad.md`, con la fecha y la versión probada.

## Glosario

| Término | Definición |
|---|---|
| Biblioteca de proyectos | Contenedor de proyectos de Resolve. En red es una base de datos de PostgreSQL. Antes de Resolve 18 se llamaba "base de datos" |
| Estación de trabajo | Computadora con DaVinci Resolve que se conecta a una biblioteca |
| Rol propietario | Rol de PostgreSQL con el que Resolve se conecta a una biblioteca y que es dueño de sus tablas |
| Rol de control | Rol de PostgreSQL con el que sqsolve administra el servidor (`sqsolve_admin`) |
| Plano de control | sqsolve y su base SQLite: usuarios web, catálogo, tareas, auditoría |
| Plano de datos | El servidor PostgreSQL con las bibliotecas |
| Huella de esquema | Hash de la lista de tablas, columnas y tipos de una biblioteca; detecta cambios de versión sin leer datos |
| Respaldo lógico | Archivo de `pg_dump` en formato custom de una biblioteca, con manifiesto |
| Intercambio | Restauración que reemplaza una biblioteca renombrando bases, sin escribir sobre la activa |
| Llave de acceso | Archivo `.resolvedbkey` que Resolve importa para agregar una biblioteca en red |

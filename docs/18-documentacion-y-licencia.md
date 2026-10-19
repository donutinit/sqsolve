# 18. Documentación, licencia y marcas

## 1. Plan de documentación

| Documento | Público | Contenido | Versión |
|---|---|---|---|
| `README.md` | Todos | Qué es, instalación rápida, enlaces | 0.1 |
| `docs/manual/instalacion.md` | Administrador | Requisitos, YAML, volúmenes, primer arranque, por plataforma | 0.9 |
| `docs/manual/administracion.md` | Administrador | Cada pantalla y tarea del panel | 0.9 |
| `docs/manual/editores.md` | Editores | Conectar Resolve paso a paso por sistema, colaborar, puertos y firewall de la estación, qué hacer si algo falla | 0.9 |
| `docs/manual/respaldos.md` | Administrador | Estrategia 3-2-1, verificación, restauración, simulacros | 0.9 |
| `docs/manual/acceso-remoto.md` | Administrador | WireGuard y Headscale, qué funciona por VPN (según PoC-08) | 1.0 |
| `docs/manual/medios.md` | Administrador | Contenido de `15-almacenamiento-de-medios.md` en forma de guía | 1.0 |
| `docs/manual/errores.md` | Todos | Cada código `SQS-` con causa y solución (generado) | 0.9 |
| `docs/runbooks/*.md` | Administrador | Restaurar, revertir, recuperar instalación, actualizar Resolve, actualizar sqsolve, rotar credenciales, credencial filtrada, pérdida de clave maestra, disco lleno, PostgreSQL no arranca | 1.0 |
| `docs/` técnico (este plan) | Desarrolladores, agentes | Arquitectura, ADR, contratos | 0.1 y vivo |
| `api/openapi.yaml` | Integradores | Referencia de la API | 0.5 |
| `CONTRIBUTING.md` | Contribuidores | Entorno, estilo, pruebas, cómo proponer cambios | 0.1 |
| `SECURITY.md` | Investigadores | Cómo reportar vulnerabilidades | 0.1 |
| `CHANGELOG.md` | Todos | Cambios por versión | 0.1 |

Reglas:

- Español primero; inglés para `README`, manual de instalación y de editores desde 1.0.
- La documentación de usuario no menciona funciones que no existen todavía.
- Cada pantalla del panel enlaza a su sección del manual.
- Los runbooks son pasos numerados, con la salida esperada de cada comando y cómo saber que
  terminó bien.
- Se publican como Markdown en el repositorio; un sitio estático generado es opcional y posterior.
- Capturas propias de sqsolve. De Resolve, solo las imprescindibles, hechas por el proyecto y
  usadas para ilustrar la interoperabilidad.

## 2. Licencia del código: AGPL-3.0-only

| Opción | A favor | En contra |
|---|---|---|
| **AGPL-3.0-only** | Garantiza que versiones modificadas ofrecidas por red (un fabricante de NAS, un servicio hospedado) publiquen su código; coherente con "100 % libre" | Algunas empresas evitan la AGPL |
| GPL-3.0-or-later | Copyleft conocido | No cubre el uso por red, que es justamente el caso de un servidor con panel web |
| Apache-2.0 o MIT | Adopción máxima | Permite versiones cerradas derivadas, que es lo que el proyecto quiere evitar |
| MPL-2.0 | Copyleft por archivo | Débil para una aplicación completa |

Decisión en ADR 0001. Consecuencias:

- Cada archivo fuente lleva `SPDX-License-Identifier: AGPL-3.0-only`.
- El panel muestra en "Acerca de" un enlace al código fuente de la versión exacta en ejecución
  (requisito de la sección 13 de la AGPL).
- Dependencias permitidas: licencias compatibles con AGPLv3 (MIT, BSD, ISC, Apache-2.0, MPL-2.0,
  LGPL, GPLv3). Se prueba en CI con un listado de licencias del SBOM.
- Contribuciones: sin CLA. Las contribuciones entran bajo la misma licencia del proyecto
  ("inbound = outbound"), explicado en `CONTRIBUTING.md`.

## 3. Licencia de la documentación

CC BY-SA 4.0 para `docs/` (salvo fragmentos de configuración y ejemplos de código, que siguen la
licencia del código para poder copiarse en instalaciones).

## 4. Marcas y relación con terceros

- "DaVinci Resolve", "Blackmagic Design", "Blackmagic Cloud" y "Fusion" son marcas de Blackmagic
  Design Pty. Ltd. Se usan solo para describir la interoperabilidad.
- El nombre "sqsolve", el logotipo y la interfaz no imitan a Blackmagic ni a WireBear.
- Aviso en el README y en "Acerca de": proyecto independiente, sin relación ni aval de Blackmagic
  Design ni de WireBear.
- No se distribuyen binarios, esquemas, plantillas, manuales ni capturas de Blackmagic.
- Interoperabilidad: el proyecto solo observa el comportamiento externo de software que el
  usuario tiene legalmente (conexiones a su propio servidor, archivos que genera su propio
  software). Antes de publicar el módulo de llaves `.resolvedbkey`, revisar con asesoría legal la
  descripción del formato publicada en la documentación (riesgo J-02 en `19-riesgos.md`).

## 5. Gobierno del proyecto

- Mantenedor principal: Diego (`donutinit`).
- Decisiones técnicas: ADR en `docs/adr/`, con estado Propuesto, Aceptado, Reemplazado.
- Hoja de ruta pública en `docs/20-roadmap.md`.
- Política de soporte: la última versión menor de la última mayor recibe arreglos; las de
  seguridad se aplican además a la mayor anterior durante 6 meses.

# ADR 0001. Licencia

**Estado:** Aceptado (2026-10-18).

**Contexto.** El proyecto debe ser 100 % libre, autohospedable y evitar derivados cerrados. Es
un servidor con interfaz web, así que el caso típico de uso de una versión modificada es por red.

**Decisión.** Código bajo AGPL-3.0-or-later. Documentación bajo CC BY-SA 4.0. Sin CLA;
contribuciones bajo la misma licencia. El panel enlaza al código fuente de la versión en
ejecución.

**Alternativas.** GPL-3.0 (no cubre uso por red), Apache-2.0 o MIT (permiten derivados cerrados),
MPL-2.0 (copyleft débil). Comparación en `18-documentacion-y-licencia.md` §2.

**Consecuencias.** Dependencias limitadas a licencias compatibles con AGPLv3, comprobadas en CI.
Algunas empresas evitarán el proyecto; se acepta.

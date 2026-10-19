# ADR 0001. Licencia

**Estado:** Aceptado (2026-10-18).

**Contexto.** El proyecto debe ser 100 % libre, autohospedable y evitar derivados cerrados. Es
un servidor con interfaz web, así que el caso típico de uso de una versión modificada es por red.

**Decisión.** Código bajo AGPL-3.0-only (solo la versión 3, sin la cláusula "o cualquier versión
posterior"). Documentación bajo CC BY-SA 4.0. Sin CLA; contribuciones bajo la misma licencia. El
panel enlaza al código fuente de la versión en ejecución.

Solo la versión 3 por decisión de Diego (2026-10-18). Como no hay CLA, pasar después a otra
versión de la licencia requeriría el acuerdo de cada persona que haya contribuido.

**Alternativas.** AGPL-3.0-or-later (admite versiones futuras de la AGPL publicadas por la FSF),
GPL-3.0 (no cubre uso por red), Apache-2.0 o MIT (permiten derivados cerrados),
MPL-2.0 (copyleft débil). Comparación en `18-documentacion-y-licencia.md` §2.

**Consecuencias.** Dependencias limitadas a licencias compatibles con AGPLv3, comprobadas en CI.
Algunas empresas evitarán el proyecto; se acepta.

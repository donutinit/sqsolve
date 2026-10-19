# ADR 0013. Llaves `.resolvedbkey`

**Estado:** Aceptado, pendiente de PoC-05.

**Contexto.** Resolve importa llaves `.resolvedbkey` (XML según el manual de 18) exportadas por
Project Server [01 §8]. El formato no está documentado y no hay implementaciones libres.

**Decisión.** Módulo experimental, desactivado por defecto, solo si PoC-05 demuestra que Resolve
acepta llaves generadas en Windows, macOS y Linux, y tras la revisión legal J-02. Las
instrucciones de conexión manual son siempre el camino principal.

**Alternativas.** No ofrecerlo nunca (peor experiencia); ofrecerlo sin pruebas (riesgo de
llaves que no funcionan o que filtran la contraseña).

**Consecuencias.** La llave contiene la contraseña: descargarla exige reautenticación, queda en
la auditoría y la interfaz advierte que se trate como una contraseña.

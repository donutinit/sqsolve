# ADR 0019. Exportación experimental de proyectos y grados

**Estado:** Aceptado (2026-10-19, Diego: la exportación experimental va en la 1.0, apagada por defecto y marcada "Experimental"). Acota a ADR 0007 sin reemplazarlo.

**Contexto.** Diego quiere descargar desde el panel los grados (`.drx`, el árbol de nodos de un
still de la galería, incluidas las PowerGrades) y los proyectos (`.drp`) de una biblioteca sin
abrir Resolve. ADR 0007 prohíbe leer tablas de Resolve. Un estudio del 2026-10-19 contra dos
bibliotecas reales (`docs/22` §2) mostró que ambos archivos son la serialización directa de
filas de la biblioteca y que se pueden reproducir desde PostgreSQL: 34 de 34 proyectos de la
biblioteca local en disco y 5 de 5 de la biblioteca `apolo` salieron sin diferencias frente a lo
que exporta Resolve 21.1.1, y Resolve importó un `.drp` de 11 líneas de tiempo y varios `.drx`
generados así.

**Decisión.** sqsolve lee, nunca escribe, un conjunto fijo de tablas de la galería y del
proyecto (`internal/drp/model.json`), solo para producir `.drp` y `.drx`, solo si el
administrador activa la función en Ajustes (apagada por defecto) y siempre marcada
"Experimental, sin garantía". La lectura corre con `sqsolve_admin` dentro de una transacción
`READ ONLY` con `statement_timeout`. Todo lo que no se observó en una exportación real hace que
sqsolve se niegue (`SQS-E004`) en vez de adivinar; un esquema que no coincide con el modelo apaga
la función (`SQS-E001`) hasta que el kit de verificación de `docs/22` §6 la confirme.

Se autoriza la dependencia `github.com/klauspost/compress` v1.20.3 (BSD-3, publicada el
2026-10-12), solo su paquete `zstd`: hace falta para leer el orden de las carpetas del media
pool y para reescribir el árbol de nodos de los efectos OFX como lo hace Resolve. Go no trae zstd
y el proyecto no usa cgo.

**Alternativas.** Que Resolve exporte con su API de scripting (`ProjectManager.ExportProject`,
`GalleryStillAlbum.ExportStills`): fiel, pero exige una estación con GPU y licencia Studio
dedicada, y `SetCurrentDatabase` cierra el proyecto que esté abierto. Servir las imágenes de los
stills: descartado, los DPX viven en el disco de la estación que los capturó, no en la base.
Mantener ADR 0007 sin excepción: deja fuera una función que Diego pidió y que no requiere
escribir nada.

**Consecuencias.** Nueva familia de códigos `SQS-E`. El modelo embebido describe el formato
`.drp` (nombres de clases y campos que aparecen en cualquier archivo exportado por Resolve), no
la base; las pruebas usan un esquema sintético que solo repite esos nombres. Cada versión nueva
de Resolve pasa por el kit de verificación antes de marcarse verificada en
`internal/compat/fingerprints.json`. El paquete `internal/drp` no se extiende a otras tablas ni a
escrituras sin otro ADR.

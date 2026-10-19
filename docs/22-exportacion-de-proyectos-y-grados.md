# 22. Exportación de proyectos y grados

Función experimental (ADR 0019): sqsolve produce archivos `.drp` (proyectos) y `.drx` (grados)
leyendo la biblioteca, sin pasar por Resolve. Apagada por defecto y marcada "Experimental, sin
garantía" en todas partes. Este documento recoge lo observado de Resolve 21.1.1 el 2026-10-19,
las reglas que sqsolve aplica y cómo verificar una versión nueva.

## 1. Qué exporta y qué no

| Archivo | Contenido | Desde dónde |
|---|---|---|
| `.drx` | El árbol de nodos de un still de la galería y su miniatura | Álbumes PowerGrade y stills de cada proyecto |
| `.zip` de álbum | Un `.drx` por still del álbum | Un álbum |
| `.drp` | El proyecto completo: media pool, líneas de tiempo, grados y galería, sin stills | Un proyecto |

No se exporta:

- La imagen de un still. Resolve la guarda como DPX en `<GalleryPath>/<id del proyecto>/<id
  del still>.dpx` en el disco de la estación que lo capturó; en la base solo está la ruta.
- Un `.drp` con stills. Resolve agrega a ese archivo un JPEG de cada still a resolución
  completa, que sale del DPX de la estación.
- Las miniaturas de los clips de la línea de tiempo. Resolve las renderiza desde el material al
  exportar y las regenera al abrir el proyecto; sqsolve escribe el campo vacío.

## 2. Cómo guarda Resolve estos archivos

Un `.drx` es un documento XML con la fila de `Gallery::GyStill` del still y tres objetos
anidados: dos `ListMgt::LmVersion` (la versión del clip y la del track) cuyo campo `Body` es el
árbol de nodos, y un `BtThumnail` con la miniatura JPEG de 288x162.

Un `.drp` es un zip con:

| Entrada | Contenido |
|---|---|
| `project.xml` | La fila de `SM_Project` con su configuración, miniaturas, grupos y bloqueos |
| `MediaPool/<raíz>/NNN_<carpeta>/.../MpFolder.xml` | Una entrada por carpeta del media pool, en postorden (las hijas antes que la madre), con prefijo de tres dígitos por el orden entre hermanas |
| `SeqContainer/<id>.xml` | Las pistas e items de cada línea de tiempo, ordenadas por id |
| `Gallery.xml` | La galería del proyecto: álbumes y referencias a stills |

En todos los documentos el nombre del elemento es el `DbType` de la fila, el atributo `DbId` es
su id y cada campo es un elemento hijo, en un orden fijo por clase. Los objetos que una fila
posee van anidados; las referencias a otros objetos van como texto. Después de la declaración
XML Resolve escribe un comentario `<!--DbAppVer="21.1.1.0010" DbPrjVer="17"-->`. Resolve
importa el archivo aunque ese comentario falte (comprobado con `.drx` el 2026-10-19).

## 3. El modelo

`internal/drp/model.json` describe la serialización: 41 clases, 49 tablas y 14 asociaciones.
Para cada clase: su tabla, sus campos en orden y, para los que no son escalares, su tipo:

| Tipo | Qué es | De dónde sale |
|---|---|---|
| `obj` | Un objeto anidado | La columna del mismo nombre guarda su id |
| `list:text` | `<Element>` con texto | Una columna de arreglo o una tabla de asociación |
| `list:obj` | `<Element>` con un objeto anidado | Una tabla de asociación o una referencia inversa |

Las tablas de asociación (`<Dueña>_<Asociada>`) tienen `DbOwner`, `DbAssociate`,
`DbPropertyName` (el nombre del campo) y `DbIndex` (el orden). `LocableBlobSet` es la única
lista que se resuelve por referencia inversa: las filas de `BtLockableBlob` apuntan a su mapa con
`BtLockableBlobMap_id`.

El modelo se aprendió de 41 exportaciones hechas por Resolve 21.1.1 con su API de scripting: 34
de la biblioteca local en disco y 5 de la biblioteca `apolo` en PostgreSQL, más 2 con stills para
conocer el `.drx`. Contiene solo nombres de clases y campos que aparecen en cualquier archivo
exportado por Resolve, no el esquema de la base.

## 4. Reglas de formato y reescrituras

Valores:

- Un uuid nulo se omite, salvo `Gallery::GyStillRef.Ref0`, `Gallery::GyStillRef.Ref1`,
  `SM_Config.User`, `SM_Project.Folder` y `SM_Project.ProjRef`, que salen como elemento vacío.
- Booleanos `true` y `false`.
- Decimales en la representación más corta que reproduce el valor, sin exponente ni `.0`.
  Comprobado solo con valores entre 66 y 85784; fuera de ese rango, Por confirmar.
- Fechas con tres decimales (`2026-10-19T13:37:24.830`).
- `bytea` en hexadecimal, salvo `Sm2MpMedia.SlateTC`, que guarda texto.
- Los arreglos `{a,b}` salen como `<Element>` por valor.
- Escape de `&`, `<`, `>` y `"` como el escritor XML de Qt. Para `"`: Por confirmar, no
  apareció en ninguna exportación.

Reescrituras que hace el exportador de Resolve y sqsolve repite:

- `SM_Project.UpToDate` es `false` cuando el proyecto tiene medios o líneas de tiempo.
- `RenderTextEnabled`, `RenderTextGanged` y `RenderTextPrefixed` de `Sm2TiAudioClip` son
  `false`.
- `SM_Config.Name` es `<nombre del proyecto>.Cfg`.
- `SM_Project.StreamCursorVector` queda en ceros.
- Dentro de `ListMgt::LmVersion.Body`, el uuid de `OfxImageEffectContextFilter_<uuid>_` se
  reescribe con el id de la versión, solo en el media pool (4 casos observados).
- El orden de las subcarpetas del media pool está en el campo 2 del `FieldsBlob` versión 2 de la
  carpeta madre: zstd, protobuf y un mapa QDataStream de índice a id. Si falta, sqsolve ordena
  por nombre y lo anota en la descarga.

## 5. Lo que no se reproduce

Valores que ni Resolve mantiene iguales entre dos exportaciones seguidas, así que sqsolve
escribe lo que hay en la base: `LastChangedTime` dentro del `FieldsBlob` de
`ListMgt::LmVersionTable`, un subcampo de `Body`, `SetupBA` de `SM_Config`, `RenderCacheBA` y
`AuxRenderCacheBA`. El orden de `LocableBlobSet` sale por hash en Resolve; sqsolve lo ordena por
id. Las miniaturas de los clips de la línea de tiempo van vacías (§1).

Guardas: el exportador se niega con `SQS-E004` si el proyecto usa `FolderMap`, el `GroupList`
antiguo de `SM_Project`, `FaceTaggingClipVec`, `FaceTaggingPeopleVec`, `PowerNodes` (nodos
compartidos), `SmartFolderVec`, `MatteVec` (mattes externos), `GeometryTrackVec`,
`FusionCompHolderItems` o `LayersVec`. Ninguno apareció lleno en una exportación, así que no se
sabe cómo los escribe Resolve.

## 6. Validación y kit de verificación

Resultados del 2026-10-19 con Resolve 21.1.1:

- 34 de 34 proyectos de la biblioteca local y 5 de 5 de `apolo` sin diferencias canónicas (los
  blobs decodificados, descontando lo volátil de §5).
- 388 y 70 documentos XML idénticos byte a byte salvo lo volátil.
- Importados en Resolve: un `.drp` de 11 líneas de tiempo y archivos `.drx` con y sin el
  comentario de versión.

Kit para una versión nueva de Resolve:

1. Exportar con sqsolve los proyectos de prueba:

   ```
   SQSOLVE_DRP_DSN=postgres://usuario:contraseña@servidor/biblioteca \
   SQSOLVE_DRP_OUT=/ruta/salida \
   SQSOLVE_DRP_PROJECTS='carpeta/proyecto,otro' \
   go test ./internal/drp -run TestCorpus -v
   ```

2. Exportar los mismos proyectos desde Resolve con la API de scripting:
   `ProjectManager.ExportProject(nombre, ruta, False)`.
3. Comparar los documentos XML de ambos zips descontando lo volátil de §5; cualquier otra
   diferencia es un cambio de formato que hay que entender antes de seguir.
4. Importar en Resolve un `.drp` y un `.drx` generados por sqsolve y abrirlos.
5. Registrar la huella en `internal/compat/fingerprints.json`:

   ```json
   "sha256:...": {
     "resolve": "21.1.1",
     "app_version": "21.1.1.0010",
     "project_version": 17,
     "verified": ["drx", "drp"],
     "checked": "2026-10-19"
   }
   ```

   `app_version` y `project_version` salen del comentario que Resolve escribe (§2);
   `verified` lista lo que pasó el kit.

Huella verificada hoy: `sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee`,
Resolve 21.1.1 (`21.1.1.0010`), `DbPrjVer` 17.

## 7. Interfaz, API y permisos

Pestaña **Exportar** en el detalle de la biblioteca, con el aviso "Experimental: sin garantía" y
el estado de la huella: "verificada con Resolve 21.1.1" o "no verificada". Tabla de proyectos
con miniatura, carpeta, nombre, fecha de modificación y botón "Descargar .drp". Álbumes
PowerGrade y de cada proyecto con sus miniaturas, botón ".drx" por still y "Descargar álbum
(.zip)".

| Ruta web | Acción |
|---|---|
| `GET /bibliotecas/{id}/exportar` | Pestaña |
| `GET /bibliotecas/{id}/exportar/proyectos/{pid}` | Descarga del `.drp` |
| `GET /bibliotecas/{id}/exportar/stills/{sid}` | Descarga del `.drx` |
| `GET /bibliotecas/{id}/exportar/albumes/{vid}` | Descarga del `.zip` |

| Ruta de la API | Alcance mínimo |
|---|---|
| `GET /libraries/{id}/projects` | `read` |
| `GET /libraries/{id}/projects/{pid}/export` | `export` |
| `GET /libraries/{id}/grades` | `read` |
| `GET /libraries/{id}/grades/{sid}/export` | `export` |
| `GET /libraries/{id}/grades/albums/{vid}/export` | `export` |

Acción `library.export` para operador y administrador; alcance de token `export`. Cada descarga
queda en la auditoría con tipo, id, nombre, archivos, bytes, huella y las notas de la
exportación.

## 8. Códigos

| Código | Significado |
|---|---|
| `SQS-E001` | La biblioteca no tiene el esquema que el modelo conoce |
| `SQS-E002` | La función está desactivada en Ajustes |
| `SQS-E003` | El proyecto, still o álbum no existe |
| `SQS-E004` | El proyecto usa una función que la exportación no reproduce |
| `SQS-E005` | La biblioteca no está disponible o falló la lectura |
| `SQS-E006` | Un valor o formato no previsto |

## 9. Límites y riesgos

- La lectura corre con `sqsolve_admin` dentro de una transacción `READ ONLY` con
  `statement_timeout` de 60 s.
- No hay tarea en cola: la descarga es síncrona y un proyecto grande tarda segundos.
- Una versión nueva de Resolve que cambie el esquema apaga la función con `SQS-E001` hasta que
  el kit de §6 la verifique; una que cambie el formato sin cambiar el esquema solo se detecta
  con el kit (riesgo T-16 de `19-riesgos.md`).

# 03. Definición del producto

## 1. Visión

Que cualquier editor o estudio pequeño tenga un servidor de bibliotecas de DaVinci Resolve
confiable, libre y propio, que se instala pegando un archivo YAML, se administra desde el
navegador y protege los proyectos con respaldos verificados, sin depender de nadie más.

## 2. Principios de producto

1. **No perder proyectos.** Ninguna acción de sqsolve destruye datos sin una copia verificable
   previa. Ante la duda, se conserva.
2. **Honesto con Resolve.** Solo se ofrece lo que Resolve respeta de verdad. Si una función
   depende de algo no documentado, la interfaz lo dice.
3. **Funciona sin Internet.** Ninguna función esencial requiere conexión externa.
4. **Simple de operar.** Dos contenedores, un archivo YAML, volúmenes claros. Sin terminal en el
   uso diario.
5. **Libre de verdad.** AGPL, compilable desde la fuente, sin telemetría.

## 3. Personas

| Persona | Contexto | Lo que necesita |
|---|---|---|
| **Editor individual** | Una persona con un escritorio y una laptop, NAS en casa | Abrir el mismo proyecto en ambas máquinas; respaldos automáticos sin pensar en ellos |
| **Estudio pequeño** | 2 a 10 estaciones, editores, colorista, asistente | Bibliotecas por cliente o producción, colaboración de Resolve Studio, accesos por persona, restaurar rápido |
| **Administrador técnico** | Quien instala y mantiene el NAS (puede ser el mismo editor) | Instalación clara, alertas, actualizaciones sin sustos, recuperación documentada |
| **Colaborador externo** (1.1) | Freelance que trabaja por VPN unos días | Recibir su acceso a una biblioteca y que se le pueda quitar sin afectar a los demás |

## 4. Alcance

### Dentro

- Administración del servidor PostgreSQL de bibliotecas de Resolve.
- Ciclo de vida de bibliotecas, credenciales y accesos por red.
- Respaldos, verificación, retención, restauración y copias externas.
- Automatización de tareas, notificaciones y webhooks.
- Auditoría, registros, salud y diagnóstico.
- Guías para conectar estaciones, colaborar y preparar el almacenamiento de medios.

### Fuera

- Almacenar, servir o transcodificar medios; generar proxies.
- Leer o modificar el contenido de los proyectos (bins, timelines). Ver ADR 0007, salvo la
  lectura acotada para exportar `.drp` y `.drx` (ADR 0019, experimental).
- Reemplazar Blackmagic Cloud o hospedar bibliotecas para terceros como servicio.
- Proveer la VPN (se documenta cómo usar una existente).
- Clientes de escritorio obligatorios: todo se hace desde el navegador y desde Resolve.

### Módulos opcionales planeados

- Llaves `.resolvedbkey` (experimental, PoC-05).
- Copias externas cifradas con restic.
- Monitor de capacidad de volúmenes de medios (solo espacio libre, sin leer archivos).
- Herramienta de diagnóstico para estaciones (`sqsolve-check`).

## 5. Casos de uso

| ID | Caso | Actor | Versión |
|---|---|---|---|
| CU-01 | Instalar sqsolve con un archivo Compose y completar el asistente inicial | Administrador | 1.0 |
| CU-02 | Iniciar y cerrar sesión en el panel, con segundo factor opcional | Administrador, operador | 1.0 |
| CU-03 | Ver el estado general: servicios, espacio, últimos respaldos, alertas | Todos | 1.0 |
| CU-04 | Crear una biblioteca nueva | Administrador | 1.0 |
| CU-05 | Obtener los datos de conexión y conectar una estación | Administrador, editor | 1.0 |
| CU-06 | Conectar una segunda estación a la misma biblioteca | Editor | 1.0 |
| CU-07 | Ver quién está conectado a cada biblioteca | Administrador, operador | 1.0 |
| CU-08 | Rotar la contraseña de una biblioteca | Administrador | 1.0 |
| CU-09 | Poner una biblioteca en mantenimiento (bloquear conexiones) | Administrador | 1.0 |
| CU-10 | Eliminar una biblioteca (a la papelera con respaldo final) | Administrador | 1.0 |
| CU-11 | Respaldar ahora una biblioteca | Administrador, operador | 1.0 |
| CU-12 | Programar respaldos y retención por biblioteca | Administrador | 1.0 |
| CU-13 | Consultar el catálogo de respaldos y su verificación | Todos | 1.0 |
| CU-14 | Restaurar un respaldo como biblioteca nueva | Administrador, operador | 1.0 |
| CU-15 | Restaurar un respaldo reemplazando la biblioteca, con reversión | Administrador | 1.0 |
| CU-16 | Descargar un respaldo para restaurarlo en Resolve | Administrador, operador | 1.0 |
| CU-17 | Importar un respaldo externo (de Resolve o de otro servidor) | Administrador | 1.0 |
| CU-18 | Ver tareas pendientes, activas, completadas y fallidas, con su registro | Todos | 1.0 |
| CU-19 | Recibir avisos por correo, webhook o ntfy | Administrador | 1.0 (webhook), 1.1 (correo, ntfy) |
| CU-20 | Consultar la auditoría | Administrador | 1.0 |
| CU-21 | Preparar una actualización de Resolve (ventana de actualización) | Administrador | 1.0 |
| CU-22 | Actualizar sqsolve o PostgreSQL (versión menor) conservando datos | Administrador | 1.0 |
| CU-23 | Recuperar la instalación completa en otro servidor | Administrador | 1.0 |
| CU-24 | Generar un paquete de diagnóstico | Administrador | 1.0 |
| CU-25 | Dar credenciales por persona y revocarlas | Administrador | 1.1 |
| CU-26 | Restringir una biblioteca a ciertas redes | Administrador | 1.1 |
| CU-27 | Portal de conexión: un colaborador ve y descarga solo sus accesos | Colaborador | 1.1 |
| CU-28 | Copiar respaldos a un repositorio externo cifrado | Administrador | 1.1 |
| CU-29 | Ejecutar hooks antes y después de operaciones | Administrador | 1.1 |
| CU-30 | Migrar bibliotecas desde otro servidor PostgreSQL (Project Server oficial, Studio Server) | Administrador | 1.1 |
| CU-31 | Descargar una llave `.resolvedbkey` | Administrador, colaborador | 1.1 si PoC-05 pasa |
| CU-32 | Recuperación a un punto en el tiempo | Administrador | 2.0 |
| CU-33 | Migrar a una versión mayor nueva de PostgreSQL | Administrador | 2.0 |

## 6. Criterios de aceptación de la versión 1.0 (MVP)

Cada criterio se demuestra con el guion de aceptación de `16-pruebas.md` §8, con Resolve real en
al menos una estación Windows o macOS y otra de cualquier plataforma.

| ID | Criterio | Cómo se verifica |
|---|---|---|
| AC-01 | Instalar en Linux genérico y en un NAS con Docker Compose pegando el YAML de referencia, sin editar más que rutas y la zona horaria | Instalación limpia cronometrada en ambos entornos; ningún paso de terminal fuera del YAML |
| AC-02 | Acceder al panel por HTTPS, completar el asistente y crear el primer administrador | El asistente termina; el certificado se muestra con su huella; el segundo arranque no vuelve a mostrar el asistente |
| AC-03 | Crear una biblioteca compatible con Resolve 21.1 desde el panel | La biblioteca queda "activa" y Resolve crea y abre un proyecto en ella |
| AC-04 | Conectar una estación siguiendo solo las instrucciones del panel | Un tercero sin ayuda conecta la estación en menos de 5 minutos |
| AC-05 | Conectar una segunda estación a la misma biblioteca | Ambas ven los mismos proyectos; el panel muestra las dos conexiones |
| AC-06 | Usar la colaboración de Resolve Studio en esa biblioteca | Bloqueo de bins y chat funcionan entre las dos estaciones con los puertos documentados |
| AC-07 | Respaldos automáticos programados con retención | Durante 72 horas se generan los respaldos programados, la retención elimina los que corresponde y ninguno verificado se pierde por error |
| AC-08 | Restaurar una biblioteca y verificar su recuperación | Restauración como copia y por reemplazo; Resolve abre el proyecto restaurado con el contenido del momento del respaldo; la reversión devuelve el estado previo |
| AC-09 | Reiniciar y actualizar sin pérdida | Reinicio del host, `docker compose pull && up -d` a una versión nueva y corte de energía simulado durante un respaldo: bibliotecas, catálogo y configuración intactos |
| AC-10 | Operar sin servicios propietarios | Con la red sin salida a Internet, todos los casos de uso 1.0 funcionan |
| AC-11 | Recuperación ante desastre | Con solo el volumen de respaldos y el kit de recuperación, una instalación nueva recupera todas las bibliotecas |

## 7. Métricas de éxito del producto

- Cero incidentes de pérdida de bibliotecas atribuibles a sqsolve.
- Tiempo de instalación hasta la primera biblioteca conectada: menos de 15 minutos.
- 100 % de los respaldos programados con resultado registrado (correcto o fallido con aviso).
- Restauración de prueba automática semanal correcta en todas las bibliotecas activas.

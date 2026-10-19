# 09. Interfaz web

## 1. Dirección visual

Decisión: **herramienta de sala de edición**. Oscura por defecto (como Resolve, para no
deslumbrar en salas con luz baja), con tema claro completo para oficinas. Sobria, densa en
información, sin ilustraciones ni degradados.

- Neutros grises fríos con un solo color de acento (ámbar, `#E8A33D` en oscuro y `#B4740F` en
  claro) para acciones primarias y foco. El ámbar evoca las luces de grabación y de estado
  de los equipos sin confundirse con los colores de estado.
- Estados siempre con icono y texto, nunca solo color: correcto (verde), aviso (amarillo), error
  (rojo), en curso (azul), inactivo (gris).
- Tipografía del sistema (`system-ui`) para el texto y monoespaciada del sistema
  (`ui-monospace`) para valores técnicos: IP, nombres de base, usuarios, hashes. Sin fuentes
  externas: el panel funciona sin Internet.
- Escala de espaciado de 4 px; radios pequeños (4 px); bordes de 1 px en lugar de sombras.
- Tokens de diseño como propiedades CSS en `:root` y sobrescritas en `[data-theme="light"]` y
  `prefers-color-scheme`.

Justificación: quien usa el panel viene de Resolve y de un NAS; reconocer jerarquía y estado de
un vistazo importa más que la personalidad visual. La sobriedad también envejece mejor.

## 2. Navegación

```mermaid
flowchart LR
  P["Panel"] --- B["Bibliotecas"]
  P --- R["Respaldos"]
  P --- T["Tareas"]
  P --- E["Eventos y auditoría"]
  P --- S["Salud y almacenamiento"]
  P --- A["Ajustes"]
  B --> BD["Detalle de biblioteca<br/>Conexión, Respaldos, Programación,<br/>Accesos, Actividad, Ajustes, Zona de peligro"]
  A --> AU["Usuarios y tokens"]
  A --> AN["Notificaciones"]
  A --> AS["Sistema: red, TLS, idioma, zona horaria"]
  A --> AM["Mantenimiento y recuperación"]
  A --> AC["Acerca de: versiones, licencias, compatibilidad"]
```

Escritorio: barra lateral fija con las 7 secciones. Móvil (menos de 768 px): barra superior con
menú desplegable y acciones primarias al pie. Una barra de estado global muestra tareas en curso
y alertas abiertas en todas las páginas.

## 3. Pantallas

### 3.1 Asistente inicial

Cinco pasos, con barra de progreso y posibilidad de volver:

1. **Código de instalación** (impreso en el registro del contenedor) e idioma.
2. **Cuenta de administrador** (usuario, contraseña con medidor, TOTP opcional con QR generado en
   el servidor).
3. **Red**: dirección que verán las estaciones (sqsolve sugiere las IP del contenedor y del host
   que detecta, y permite un nombre DNS), zona horaria.
4. **Respaldos por defecto**: preset (diario 02:00, cada 4 h en horario laboral, personalizado) y
   retención (por defecto: últimos 10, 14 diarios, 8 semanales, 12 mensuales).
5. **Kit de recuperación**: descarga obligatoria y casilla "lo guardé fuera de este servidor".
   Muestra la huella SHA-256 del certificado del panel.

### 3.2 Panel

Cuatro bloques: **estado del servidor** (PostgreSQL, versión, conexiones totales, espacio de
datos y de respaldos con barras), **atención requerida** (respaldos fallidos, verificaciones
fallidas, bibliotecas sin respaldo verificado en más de 48 h, espacio bajo, certificados por
vencer), **bibliotecas** (las 5 con actividad más reciente), **tareas** (en curso y últimas 5).
Cada elemento lleva a su detalle.

### 3.3 Bibliotecas

Tabla (tarjetas en móvil): nombre visible y slug, estado, conexiones ahora, tamaño, último
respaldo con su verificación, versión detectada. Filtros por estado. Botón "Nueva biblioteca".

**Detalle**, pestañas:

- **Conexión**: tarjeta con los cuatro valores para Resolve (nombre sugerido, ubicación,
  usuario, contraseña oculta con botón "Mostrar" que pide reautenticación), cada uno con botón de
  copiar; instrucciones paso a paso por sistema (Windows, macOS, Linux) con capturas de
  referencia propias; puertos que deben abrir las estaciones para colaborar; descarga de
  `.resolvedbkey` si el módulo está activo, marcada "Experimental".
- **Respaldos**: lista de respaldos de esta biblioteca con acciones; botón "Respaldar ahora".
- **Programación**: preset o cron con vista previa de las próximas 5 ejecuciones; retención con
  vista previa de qué se conservaría hoy.
- **Accesos**: credenciales (1.0: la del propietario con rotación; 1.1: por persona), reglas de
  red, límite de conexiones.
- **Actividad**: sesiones activas con botón de cerrar; eventos de la biblioteca; historial de
  huellas.
- **Ajustes**: nombre visible, notas (texto libre para rutas de medios, cliente, etc.), versión
  declarada de Resolve, guardar como plantilla.
- **Zona de peligro**: mantenimiento, eliminar. Fondo diferenciado y explicación de cada acción.

### 3.4 Respaldos

Catálogo global con filtros (biblioteca, tipo, estado, verificación, fecha), totales de espacio
por biblioteca, acción "Importar respaldo" y aviso de respaldos huérfanos (archivo sin catálogo o
al revés).

### 3.5 Tareas

Tres vistas: en curso (con barra y paso actual en vivo), en cola (con hora prevista) e
historial (filtrable por estado y tipo). El detalle de una tarea muestra pasos con tiempos y el
registro completo, con botón "Cancelar" si aplica y "Reintentar" para fallidas.

### 3.6 Eventos y auditoría

Dos pestañas. Eventos: severidad, tipo, biblioteca, mensaje. Auditoría: actor, acción, objetivo,
resultado, IP; indicador de integridad de la cadena; exportar JSON Lines.

### 3.7 Salud y almacenamiento

Volúmenes (ruta, sistema de archivos, espacio, marcador válido), PostgreSQL (versión, tiempo
encendido, conexiones por base, tamaño por base, versión de colación, sumas de verificación
activas), certificados (vencimiento), sqsolve (versión, commit, memoria, tareas). En 1.2, espacio
de volúmenes de medios montados en solo lectura.

### 3.8 Ajustes

Usuarios y tokens; notificaciones (canales con botón "Probar" y reglas); sistema (dirección
anunciada, TLS del panel con carga de certificado propio, idioma por defecto, zona horaria,
proxies de confianza); mantenimiento y recuperación (reconstruir catálogo, importar, regenerar
`pg_hba.conf`, paquete de diagnóstico, kit de recuperación, ventana de actualización de
Resolve); acerca de (versiones, licencias de terceros, SBOM, matriz de compatibilidad).

## 4. Flujos clave

### 4.1 Nueva biblioteca

1. Formulario: nombre visible; slug sugerido a partir del nombre y editable, validado en vivo;
   plantilla (si hay) o "inicializar con Resolve"; política de respaldo (la por defecto o una
   propia).
2. Pantalla de progreso de la tarea `library.create`.
3. Con E1: tarjeta "Abre Resolve en una estación y elige Create" con los valores para copiar y
   un contador de la ventana; la página se actualiza sola al detectar la inicialización.
4. Final: estado activa, primer respaldo en curso, enlace a "Conectar más estaciones".

### 4.2 Conectar una estación

Pestaña Conexión. Un enlace "Compartir instrucciones" genera una página imprimible sin la
contraseña, para mandar a un editor, y en 1.1 un enlace al portal de conexión.

### 4.3 Restaurar

1. Desde un respaldo: botón "Restaurar". Elección explícita entre **Como biblioteca nueva**
   (opción por defecto y recomendada) y **Reemplazar la biblioteca actual**.
2. Como nueva: pedir slug y nombre; tarea; final con datos de conexión de la copia y sugerencia
   "exporta desde Resolve el proyecto que necesites (.drp) e impórtalo en la biblioteca
   original".
3. Reemplazar: pantalla de consecuencias con datos reales: conexiones activas ahora (quién y
   desde dónde), fecha del respaldo y antigüedad ("se perderán los cambios de las últimas 26
   horas"), versión de Resolve del respaldo frente a la actual si difiere, que se hará un respaldo
   previo y que el estado actual se conservará 7 días para revertir. Pide teclear el slug y
   reautenticación. Opción "cerrar sesiones activas" desmarcada por defecto; si hay sesiones y no
   se marca, la tarea espera hasta 10 min a que se vacíe y luego falla sin tocar nada.
4. Progreso con pasos visibles. Final con botón "Revertir" disponible durante el plazo.

### 4.4 Eliminar

Consecuencias (conexiones, tamaño, respaldos que quedan y su retención), confirmación tecleada,
reautenticación. La biblioteca va a la papelera con respaldo final verificado; se purga a los 30
días. Desde la papelera: recuperar o purgar ya (con segunda confirmación).

### 4.5 Ventana de actualización de Resolve

Asistente en Mantenimiento: elegir versión de destino; sqsolve respalda y verifica (nivel 2)
todas las bibliotecas activas con tipo `pre_upgrade`; lista de pasos para el estudio (que todos
cierren Resolve, actualizar una estación, abrir cada biblioteca y aceptar la actualización si se
ofrece, actualizar las demás estaciones); al cerrar la ventana, comparación de huellas antes y
después por biblioteca.

## 5. Patrones de interacción

- **Operaciones largas**: siempre una tarea; la página muestra progreso por SSE y se puede salir
  y volver. Nunca un botón que bloquea esperando.
- **Peligro**: color de error solo en el botón final, consecuencias con datos reales,
  confirmación tecleada para irreversibles, reautenticación si pasaron más de 5 min.
- **Formularios**: validación en el servidor siempre y en vivo por htmx; errores junto al campo;
  el envío no borra lo escrito.
- **Vacíos**: cada lista vacía explica qué es y ofrece la acción principal ("Aún no hay
  bibliotecas. Crea la primera").
- **Errores**: mensaje humano, código `SQS-`, enlace a la ayuda y, si aplica, botón de
  reintentar.
- **Copiar**: botón con confirmación visual "Copiado"; en navegadores sin API de portapapeles,
  selecciona el texto.
- **Tiempos**: relativos ("hace 3 h") con la fecha absoluta en la zona del servidor al pasar el
  cursor o tocar.

## 6. Accesibilidad y adaptabilidad

- WCAG 2.2 AA: contraste 4.5:1 en texto, foco visible con el acento, objetivos táctiles de 44 px,
  navegación completa por teclado, `aria-live="polite"` para progreso y avisos, etiquetas en
  todos los campos, tablas con encabezados reales.
- Puntos de quiebre: 360, 768, 1200 px. Las tablas pasan a tarjetas bajo 768 px.
- `prefers-reduced-motion` desactiva animaciones.

## 7. Idiomas

Español (por defecto) e inglés. Catálogos en `internal/web/i18n/{es,en}.json` con claves
estables; una prueba falla si una clave falta en algún idioma. Fechas y números con el formato
del idioma elegido. Los códigos `SQS-` no se traducen.

## 8. Implementación

- Plantillas en `internal/web/templates`, un `layout` base, parciales por componente
  (`card`, `status-badge`, `danger-zone`, `job-progress`, `copy-field`, `empty-state`).
- Antes de la primera tarea de interfaz (SQS-500), el agente corre `ui-consistency`
  (finding-patterns, luego planning) y documenta en `docs/ui/checklist.md` los componentes,
  tokens y patrones que todas las páginas deben reutilizar. Cada página nueva se verifica contra
  esa lista.

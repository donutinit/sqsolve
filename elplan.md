# El plan contra lo que existe

Fecha: 2026-10-19, con la 1.0.0 publicada. Compara el plan original (`docs/03` producto, `docs/04`
requisitos, `docs/20` roadmap, escritos antes de la primera línea de código) con lo que hay en el
repositorio, y termina con lo que, a juicio del agente, le hace falta al proyecto y en qué orden.

Leyenda: **Hecho**, **Parcial**, **No**, y "Validado" cuando además se probó con Resolve real
(`docs/poc`, `docs/aceptacion/1.0.md`).

## 1. Lo que el plan prometía para la 1.0

Un servidor de bibliotecas de Resolve que se instala con un YAML, se administra desde el
navegador, respalda y verifica solo, restaura sin perder nada y se recupera en otro servidor con
los respaldos y un kit. Eso existe y está desplegado. Lo que el plan dejaba para 1.1, 1.2 y 2.0
sigue casi todo sin hacer, salvo tres piezas que entraron antes de tiempo (credenciales por
persona, reglas de red y SMTP y ntfy) y una que no estaba en el plan y Diego pidió (exportar
`.drp` y `.drx`, también desde un respaldo).

## 2. Requisitos funcionales, uno por uno

### Instalación (RF-INS)

| Requisito | Estado | Nota |
|---|---|---|
| RF-INS-01 un solo Compose | Hecho, validado en TrueNAS | |
| RF-INS-02 secretos y certificados al primer arranque | Hecho | |
| RF-INS-03 asistente: idioma, zona, administrador, dirección, política, kit | Parcial | Sin idioma: el panel solo existe en español |
| RF-INS-04 código de instalación de un solo uso | Hecho, validado | |
| RF-INS-05 negarse a arrancar sin volúmenes o permisos | Hecho | `SQS-C053`, `SQS-C054` |

### Bibliotecas (RF-BIB)

| Requisito | Estado | Nota |
|---|---|---|
| RF-BIB-01 crear con nombre, identificador y notas | Hecho, validado (E1) | La estrategia E0 "base vacía" no funciona con Resolve 21.1 (PoC-01) |
| RF-BIB-02 crear desde plantilla local (E2) | No | Sería además la salida para E0 |
| RF-BIB-03 lista con estado, tamaño, conexiones, último respaldo, versión | Hecho | Versión de Resolve es la declarada a mano |
| RF-BIB-04 detalle con pestañas | Hecho | Más la pestaña Exportar, fuera del plan |
| RF-BIB-05 editar nombre, notas, límite, versión | Hecho | |
| RF-BIB-06 mantenimiento | Hecho, validado (PoC-09) | |
| RF-BIB-07 papelera con respaldo final y purga | Hecho | Desde 1.0.0 también para bibliotecas sin base |
| RF-BIB-08 sesiones y cerrar sesión | Hecho | Con columna de cifrado desde 1.0 |
| RF-BIB-09 huella de esquema | Hecho, validado en parte (PoC-03) | Falta ver que cambie con una actualización real |
| RF-BIB-10 adoptar una base existente | No | |
| RF-BIB-11 solo lectura (archivada) | No | Dependía de PoC-09; nadie lo volvió a pedir |
| RF-BIB-12 VACUUM semanal | Hecho | |

### Credenciales y acceso (RF-ACC)

| Requisito | Estado | Nota |
|---|---|---|
| RF-ACC-01 rol propietario con contraseña fuerte | Hecho, validado | |
| RF-ACC-02 mostrar contraseña con reautenticación | Cambiado | ADR 0021: sin reautenticación, con auditoría |
| RF-ACC-03 rotar contraseña | Hecho | |
| RF-ACC-04 credenciales por persona | Hecho en código, no validado con Resolve | Era de 1.1 |
| RF-ACC-05 reglas de red en `pg_hba.conf` | Hecho, validado en parte (PoC-08) | Por la app de Tailscale llega una sola IP |
| RF-ACC-06 instrucciones de conexión | Hecho | |
| RF-ACC-07 llave `.resolvedbkey` | No | Solo existe el ajuste apagado; depende de PoC-05 |
| RF-ACC-08 portal de colaborador | No | |

### Respaldos (RF-RES)

| Requisito | Estado | Nota |
|---|---|---|
| RF-RES-01 a 12: manual, programación, retención, catálogo, L1, L2, restaurar como nueva y por reemplazo, descargar, importar, fijar, previsualizar retención | Hecho, validado | Más el modo según actividad (ADR 0022), fuera del plan |
| RF-RES-13 copia externa con restic | No | |
| RF-RES-14 respaldo diario del plano de control | Hecho, validado (recuperación en mininas) | |
| RF-RES-15 reconstruir catálogo desde manifiestos | Hecho | |
| RF-RES-16 PITR con pgBackRest | No | 2.0 |

### Tareas, notificaciones, usuarios (RF-TAR, RF-NOT, RF-USR)

| Requisito | Estado | Nota |
|---|---|---|
| RF-TAR-01 a 07: tareas persistentes, vista, cancelar, interrumpidas, ejecuciones perdidas, mantenimiento, webhooks HMAC | Hecho | Progreso en vivo por SSE |
| RF-TAR-08 hooks en contenedor aislado | No | |
| RF-NOT-01 webhook | Hecho | |
| RF-NOT-02 SMTP y ntfy | Hecho en código | Era de 1.1; sin prueba contra un servidor real |
| RF-NOT-03 reglas por tipo y severidad, botón de prueba | Hecho | |
| RF-NOT-04 agrupación de avisos repetidos | No | |
| RF-USR-01 roles administrador, operador, lector | Hecho | |
| RF-USR-02 TOTP opcional, obligatorio para administradores | Hecho | |
| RF-USR-03 tokens de API con alcance | Hecho | |
| RF-USR-04 recuperación de acceso por CLI | Hecho | Más `admin unlock` (ADR 0021) |
| RF-USR-05 rol colaborador | No | |
| RF-USR-06 OIDC, RF-USR-07 WebAuthn | No | 2.0 |

### Observación y actualizaciones (RF-OBS, RF-ACT)

| Requisito | Estado | Nota |
|---|---|---|
| RF-OBS-01 a 05: salud, eventos, auditoría encadenada, exportación JSONL, paquete de diagnóstico | Hecho | |
| RF-OBS-06 métricas Prometheus | No | |
| RF-ACT-01 migraciones de SQLite con copia previa | Hecho | Cuatro migraciones hoy |
| RF-ACT-02 comprobar versión de PostgreSQL al arrancar | Hecho | |
| RF-ACT-03 ventana de actualización de Resolve | Hecho, no validado | Falta una actualización real |
| RF-ACT-04 matriz de compatibilidad en el panel | Parcial | "Acerca de" remite a `docs/14` en vez de mostrarla |
| RF-ACT-05 asistente de versión mayor de PostgreSQL | No | 2.0 |
| RF-ACT-06 migración desde otro servidor | No | |

## 3. Requisitos no funcionales que cambiaron o no se midieron

| Requisito | Estado |
|---|---|
| RNF-SEG-09 reautenticación en operaciones peligrosas | Cambiado por decisión (ADR 0021): confirmación tecleada y auditoría; sin reautenticación |
| RNF-USA-01 interfaz en español e inglés | No: solo español |
| RNF-USA-03 WCAG 2.2 AA | Por confirmar: se cuidó (formas además de color, foco, contraste medido), sin auditoría formal |
| RNF-USA-05 p95 menor a 300 ms con 50 bibliotecas y 5000 respaldos | Por confirmar: no se midió |
| RNF-MAN-01 cobertura de pruebas 80 % | Por confirmar: no se mide en CI |
| RNF-MAN-03 compilación reproducible | Por confirmar: `-trimpath` y digests fijados, sin comprobar dos compilaciones |
| RNF-MAN-04 compilable sin Internet (`go mod vendor`) | No |
| RNF-MAN-05 OpenAPI validado en pruebas | No (pasó a 1.1) |
| RNF-COM-01 Resolve 21.1.x | Validado en Linux; macOS y Windows sin probar |
| El resto (fiabilidad, seguridad, operación) | Hecho según el plan; la batería de fallos de CI cubre cortes a media tarea |

## 4. Casos de uso del plan

Hechos y validados: CU-01 a 05, 07 a 15, 17, 18, 20, 22, 23, 24. Hechos sin validar con Resolve:
CU-16 (restaurar en Resolve un respaldo descargado), CU-19 correo y ntfy, CU-21 ventana de
actualización, CU-25 y CU-26 (de 1.1, ya en código). Sin probar por falta de segunda estación:
CU-06. No hechos: CU-27 portal, CU-28 restic, CU-29 hooks, CU-30 migración desde otro servidor,
CU-31 `.resolvedbkey`, CU-32 PITR, CU-33 versión mayor de PostgreSQL.

## 5. Lo que quedó sin validar con Resolve

- Resolve en macOS y Windows, y la colaboración entre dos estaciones (PoC-07, CU-06).
- El Backup y Restore del propio Resolve contra los archivos de sqsolve (PoC-04 pasos 1, 2 y 4).
- Credenciales por persona desde Resolve y la base con dueño `sqsolve_admin` (PoC-01 pasos 3 y 5).
- Que la huella cambie con una actualización de versión (PoC-03) y la ventana de actualización.
- Resolve 20.3.2 y la matriz completa de versiones (PoC-02).

## 6. Lo que le hace falta, en orden

1. **Copia externa con restic (RF-RES-13, CU-28).** Es la única pieza que convierte "no perder
   proyectos" en una promesa contra la pérdida del servidor, no solo contra el error humano. Hoy
   respaldos y datos viven en el mismo NAS. Pequeña dependencia (el binario `restic`), ya
   prevista en `docs/06` y `docs/11` §9: `backup`, `forget --prune` y `check` programados,
   restauración desde el remoto. Es lo primero.
2. **Bloqueo tras un reemplazo.** PoC-09 mostró que Resolve reconecta solo después del intercambio
   y guarda encima de lo restaurado si alguien tenía el proyecto abierto. Una contraseña nueva al
   reemplazar, o la biblioteca en mantenimiento hasta que el administrador la libere, cierra ese
   hoyo. Es poco código y protege la operación más delicada.
3. **Migración desde otro servidor (RF-ACT-06, CU-30).** El caso del propio Diego: un PostgreSQL
   15 con varias bibliotecas. Listar, respaldar con `pg_dump` de la versión correcta, importar
   con verificación L2 y crear la biblioteca con su rol. Hoy es trabajo a mano.
4. **Validación en macOS y Windows y de la colaboración.** No es una función, pero es la mayor
   incógnita que le queda al producto, y la que separa "me sirve a mí" de "le sirve a un
   estudio". Con una segunda estación, una tarde.
5. **Plantillas locales (RF-BIB-02, E2).** Crear bibliotecas sin pasar por Resolve y, de paso,
   rescatar la estrategia de base precreada que hoy no funciona.
6. **Portal de colaborador (RF-ACC-08, RF-USR-05).** Útil para estudios con freelances; sin
   sentido hasta que las credenciales por persona se prueben con Resolve.
7. **Agrupación de avisos (RF-NOT-04)** y **matriz de compatibilidad en el panel (RF-ACT-04)**:
   pulido barato.
8. **Interfaz en inglés (RNF-USA-01).** Imprescindible si el proyecto quiere usuarios fuera de
   habla hispana; hoy no hay infraestructura de traducción, así que es una tarea mediana.
9. **Hooks en contenedor aparte, Prometheus, `sqsolve-check`, `.resolvedbkey`, PITR, OIDC y
   WebAuthn**: según el plan y en ese orden; ninguno bloquea a un estudio pequeño hoy.

Lo que no hace falta y conviene no hacer sin una razón nueva: el modo solo lectura (RF-BIB-11), el
modo servidor externo (2.0) y la réplica del registro de imágenes.

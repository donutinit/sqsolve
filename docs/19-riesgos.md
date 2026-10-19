# 19. Riesgos

Probabilidad (P) e impacto (I): A alto, M medio, B bajo. Se revisa al cerrar cada fase.

## 1. Técnicos y de compatibilidad

| ID | Riesgo | P | I | Mitigación | Señal temprana |
|---|---|---|---|---|---|
| T-01 | Resolve cambia su comportamiento con PostgreSQL en una versión nueva | M | A | Caja negra (ADR 0007); guion de compatibilidad en 14 días; ventana de actualización; respaldos `pre_upgrade` | Notas de versión que mencionan Project Server o bibliotecas |
| T-02 | Resolve exige superusuario o ser dueño de la base | M | M | Estrategia E1 y plan B de PoC-01 (dueño = rol de biblioteca, renombres con superusuario por socket) | PoC-01 |
| T-03 | Resolve 20.3.x falla con PostgreSQL 18 | M | M | Perfil alternativo con PostgreSQL 17 para quien lo necesite; documentar | PoC-02 |
| T-04 | El formato `.resolvedbkey` es cerrado o cambia | A | B | Módulo experimental, desactivado por defecto; las instrucciones manuales siempre existen | PoC-05 |
| T-05 | Resolve no se reconecta bien tras el intercambio de bases | B | A | PoC-09; aviso de cerrar Resolve antes de reemplazar; modo "como nueva" por defecto | PoC-09 |
| T-06 | Bibliotecas restauradas del mismo respaldo se confunden en Resolve (identificadores internos repetidos) | B | M | PoC-04 paso 3; si pasa, advertir y sugerir desconectar la original al abrir la copia; E2 se descarta | PoC-04 |
| T-07 | `pg_dump` bloquea a Resolve durante cambios de esquema | B | M | `--lock-wait-timeout`; programación fuera de horas; medición en PoC-10 | `log_lock_waits` |
| T-08 | Docker oculta la IP de origen y las reglas de red no sirven | M | M | PoC-08; documentar `network_mode: host` para quien necesite reglas por red | PoC-08 |
| T-09 | Error de lógica en retención borra respaldos necesarios | B | A | Función pura con pruebas de propiedades; `min_verified`; vista previa; borrado en dos fases; fijado | Pruebas |
| T-10 | Corte de energía a mitad del intercambio | B | A | Diario de pasos y tabla de recuperación (11 §7.3); pruebas de inyección de fallos | Pruebas de fallos |
| T-11 | Disco lleno en `pgdata` detiene PostgreSQL | M | A | Alertas a 80 % y 90 %; reserva del sistema de archivos; L2 se omite si no hay espacio | Indicador de espacio |
| T-12 | Corrupción de SQLite | B | M | WAL, copias diarias en `backups/_system`, reconstrucción de catálogo desde manifiestos | `PRAGMA integrity_check` diario |
| T-13 | Colación cambia con una imagen nueva | B | A | Proveedor `builtin` (PoC-01 paso 7) e indicador de versión de colación | Indicador de colación |
| T-14 | La colaboración falla por la red de las estaciones y se culpa al servidor | A | B | Guía para editores con puertos; PoC-07; herramienta `sqsolve-check` (1.2) | Reportes de usuarios |
| T-15 | Cambio en la imagen oficial de PostgreSQL (rutas, entrada) | B | M | Digest fijado; pruebas de integración contra cada digest nuevo | Trabajo semanal de digest |

## 2. Seguridad

| ID | Riesgo | P | I | Mitigación |
|---|---|---|---|---|
| S-01 | Alguien expone 5432 a Internet | M | A | Documentación, aviso en el panel si la dirección anunciada es pública, contraseñas fuertes y SCRAM, reglas de red |
| S-02 | Inyección SQL o de comandos en sqsolve | B | A | Controles T-06 y T-07 de `08-seguridad.md`; rol sin superusuario; fuzzing |
| S-03 | Pérdida de la clave maestra | M | M | Kit de recuperación obligatorio; las bibliotecas siguen a salvo; rotación de credenciales documentada |
| S-04 | Cadena de suministro de Go o de la imagen base | B | A | Lista cerrada de dependencias, espera de 7 días, `govulncheck`, digest fijado, SBOM, firmas |
| S-05 | Estación comprometida filtra la contraseña de una biblioteca | M | M | Credenciales por persona (1.1) y revocación; reglas de red; rotación |

## 3. Jurídicos

| ID | Riesgo | P | I | Mitigación |
|---|---|---|---|---|
| J-01 | Uso indebido de marcas de Blackmagic | B | M | Nombre propio; uso descriptivo; aviso de independencia (18 §4) |
| J-02 | Describir públicamente el formato `.resolvedbkey` | B | M | Solo observación de archivos generados por el usuario; consulta legal antes de publicar el módulo; no distribuir archivos de Blackmagic |
| J-03 | Distribuir sin querer el esquema de Resolve (fixtures, plantillas, registros de PoC) | B | M | Fixtures sintéticos; plantillas solo locales; registros de PoC fuera del repo; revisión en PR |
| J-04 | Confusión con Studio Server | B | B | No copiar textos ni diseño; documentación propia |
| J-05 | Incompatibilidad de licencias de dependencias | B | M | Revisión de licencias del SBOM en CI |

## 4. Operativos y de proyecto

| ID | Riesgo | P | I | Mitigación |
|---|---|---|---|---|
| O-01 | Un solo mantenedor | A | M | Documentación completa, stack simple, ADR, tareas ejecutables por agentes |
| O-02 | Acceso limitado a hardware y licencias para probar Resolve | M | M | PoC concentradas en sesiones; guion de compatibilidad corto; reportes de la comunidad con formato fijo |
| O-03 | Alcance que crece (módulos de medios, clientes de escritorio) | M | M | Alcance de `03-producto.md`; todo lo nuevo pasa por ADR y roadmap |
| O-04 | Usuarios que no prueban sus restauraciones | A | A | Verificación L2 automática semanal; recordatorio trimestral del simulacro L3 |
| O-05 | El administrador usa la etiqueta `latest` y recibe una mayor sin leer notas | M | M | La documentación usa etiquetas de versión; sqsolve detecta una mayor nueva y pide confirmar en el panel antes de migrar SQLite |

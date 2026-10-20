# Códigos de error

Generado con `make errores` a partir del código; no se edita a mano. Cada código
sale con el texto con que aparece en el panel, la API, la consola o el registro.

| Código | Mensaje |
|---|---|
| `SQS-A001` | usuario no encontrado |
| `SQS-A002` | usuario o contraseña incorrectos |
| `SQS-A003` | demasiados intentos; espera ... |
| `SQS-A004` | cuenta bloqueada por contraseñas incorrectas; en la consola del contenedor: sqsolve admin unlock <usuario> |
| `SQS-A005` | falta el segundo factor |
| `SQS-A006` | código incorrecto |
| `SQS-A010` | contraseña débil |
| `SQS-A011` | usuario inválido: de 2 a 32 caracteres, letras, números, punto, guion o guion bajo |
| `SQS-A012` | rol inválido |
| `SQS-A013` | ya existe un usuario con ese nombre |
| `SQS-A014` | debe quedar al menos un administrador activo |
| `SQS-A015` | las contraseñas no coinciden |
| `SQS-A020` | el token necesita un nombre de hasta 80 caracteres |
| `SQS-A021` | alcance desconocido: ... |
| `SQS-A022` | elige al menos un alcance |
| `SQS-A023` | token no encontrado |
| `SQS-A024` | token inválido, vencido o revocado |
| `SQS-A030` | el rol o los alcances del token no permiten esta acción |
| `SQS-A031` | El formulario venció o no viene de este panel. Recarga la página e inténtalo de nuevo. |
| `SQS-A040` | el segundo factor es obligatorio para administradores en esta instalación |
| `SQS-B001` | El archivo de este respaldo ya no existe. |
| `SQS-B002` | no existe la base ... |
| `SQS-B003` | no se respalda una biblioteca en estado ... |
| `SQS-B010` | pg_dump falló: ... |
| `SQS-B011` | conteo de filas: ... |
| `SQS-B012` |  |
| `SQS-B020` | verificación fallida: ... |
| `SQS-B021` | el respaldo no está disponible (...) |
| `SQS-B022` | no hay espacio en el volumen de la base para la restauración de prueba (... necesarios) |
| `SQS-B030` | los valores de retención van de 0 a 1000 |
| `SQS-B031` | la retención debe conservar al menos un respaldo |
| `SQS-B032` | conserva al menos un respaldo verificado (1 a 100) |
| `SQS-B033` | según actividad se revisa cada 5, 10, 15, 30 o 60 minutos |
| `SQS-B034` | es el respaldo verificado más reciente de la biblioteca; no se borra a mano |
| `SQS-B040` | manifiesto ilegible: ... |
| `SQS-B041` | el archivo no parece un respaldo de pg_dump en formato custom |
| `SQS-B050` | elige un archivo de la carpeta de importación o súbelo |
| `SQS-B051` | el archivo está fuera de la carpeta de importación |
| `SQS-B052` | la subida se interrumpió: ... |
| `SQS-B054` | ya hay un archivo llamado ... en la carpeta de importación; impórtalo desde la lista o cámbiale el nombre |
| `SQS-B060` | la ventana se detiene: ... no quedó respaldada y verificada: ... |
| `SQS-C001` | ...: ... |
| `SQS-C010` | no se pudo leer la clave maestra (¿corriste sqsolve init?): ... |
| `SQS-C011` | la clave maestra debe tener ... bytes, tiene ... |
| `SQS-C012` | no se pudo descifrar el secreto |
| `SQS-C013` | contraseña de sqsolve_admin: ... |
| `SQS-C014` | contraseña del superusuario: ... |
| `SQS-C020` | certificado del panel: ... |
| `SQS-C021` | el certificado y la llave no corresponden: ... |
| `SQS-C030` | dirección anunciada inválida: escribe solo la IP o el nombre, sin puerto |
| `SQS-C031` | la ventana de inicialización va de 5 a 1440 minutos |
| `SQS-C032` | los plazos deben ser de al menos un día |
| `SQS-C040` | escribe la versión de Resolve de destino |
| `SQS-C050` | no se pudo crear ...: ... |
| `SQS-C051` | volumen de respaldos: ... |
| `SQS-C052` | falta ...: corre sqsolve init (el servicio sqsolve-init del compose): ... |
| `SQS-C053` | el servicio corre como ...:... pero SQSOLVE_UID y SQSOLVE_GID dicen ...:.... |
| `SQS-C054` | PostgreSQL no podrá usar el volumen de la base de datos (...). |
| `SQS-C060` | El envío es demasiado grande |
| `SQS-C061` | cuerpo JSON inválido: ... |
| `SQS-C062` | ruta de la API desconocida |
| `SQS-C063` | mode debe ser new o replace |
| `SQS-C070` | la clave maestra debe ser el texto base64 del kit (44 caracteres) |
| `SQS-C071` | no hay copias del plano de control en el volumen de respaldos (_system) |
| `SQS-C072` | la clave no corresponde a ninguna copia del plano de control: ... |
| `SQS-E001` | la biblioteca no tiene las tablas y columnas que la exportación experimental conoce |
| `SQS-E002` | la exportación experimental está desactivada; un administrador puede activarla en Ajustes |
| `SQS-E003` | no existe en la biblioteca |
| `SQS-E004` | la exportación experimental no reproduce esta función |
| `SQS-E005` | el respaldo no está disponible (falló o se borró) |
| `SQS-E006` | árbol de nodos de ...: ... |
| `SQS-E007` | el respaldo no está preparado para exportar; prepáralo primero |
| `SQS-J001` | tipo de tarea desconocido |
| `SQS-J002` | tarea no encontrada |
| `SQS-J003` | esta tarea no se puede cancelar en este momento |
| `SQS-J004` | solo se reintentan tareas fallidas, canceladas o interrumpidas |
| `SQS-J010',` | error_detail = 'El proceso se detuvo mientras la tarea corría' WHERE id = ? |
| `SQS-J020` | expresión de programación inválida |
| `SQS-J021` | tipo de programación desconocido: ... |
| `SQS-L001` | identificador inválido |
| `SQS-L002` | biblioteca no encontrada |
| `SQS-L003` | el nombre visible debe tener de 1 a 80 caracteres |
| `SQS-L004` | ya existe una biblioteca con el identificador ... |
| `SQS-L005` | ya existe una base ... |
| `SQS-L006` | ya existe un rol ... en el servidor |
| `SQS-L007` | estrategia de creación desconocida |
| `SQS-L010` | la biblioteca no está en preparación (...) |
| `SQS-L011` | la biblioteca no está esperando inicialización |
| `SQS-L012` | límite de conexiones inválido (-1 sin límite, hasta 1000) |
| `SQS-L013` | versión de Resolve demasiado larga |
| `SQS-L014` | notas demasiado largas |
| `SQS-L015` | no se puede cambiar el mantenimiento en estado ... |
| `SQS-L016` | la sesión ya no existe o no pertenece a esta biblioteca |
| `SQS-L020` | red inválida: ... |
| `SQS-L030` | credencial no encontrada |
| `SQS-L031` | la credencial está revocada |
| `SQS-L032` | solo se agregan personas a bibliotecas activas |
| `SQS-L033` | ya existe el rol ... |
| `SQS-L034` | la credencial principal no se revoca; rota su contraseña |
| `SQS-L040` | escribe el identificador de la biblioteca para confirmar |
| `SQS-L041` | no se puede eliminar una biblioteca en estado ... |
| `SQS-L042` | la biblioteca no está en la papelera |
| `SQS-L043` | ya existe otra biblioteca con el identificador ... |
| `SQS-L044` | solo se purgan bibliotecas de la papelera |
| `SQS-L045` | no hay un respaldo final verificado; no se purga |
| `SQS-L046` | el rol ... todavía es dueño de: ...; revísalas antes de purgar |
| `SQS-N001` | tipo de canal desconocido |
| `SQS-N002` | el destino respondió ... |
| `SQS-N010` | remitente inválido: ... |
| `SQS-N011` | destinatario inválido: ... |
| `SQS-N012` | STARTTLS: ... |
| `SQS-N020` | canal no encontrado |
| `SQS-N021` | el canal necesita un nombre |
| `SQS-N022` | severidad inválida |
| `SQS-N023` | URL inválida (usa http:// o https://) |
| `SQS-N024` | el correo necesita servidor, remitente y destinatarios |
| `SQS-N025` | puerto inválido |
| `SQS-N026` | seguridad inválida |
| `SQS-N030` | la prueba falló: ... |
| `SQS-P001` |  |
| `SQS-P002` | preparando ...: ... |
| `SQS-P010` | literal con caracteres no permitidos |
| `SQS-P020` | crear rol ...: ... |
| `SQS-P021` | membresía de ...: ... |
| `SQS-P030` | pg_reload_conf devolvió false |
| `SQS-P040` | rol administrativo en una regla de biblioteca |
| `SQS-P041` | no se pudo escribir pg_hba.conf: ... |
| `SQS-P042` | pg_hba.conf generado no es válido: ... |
| `SQS-P043` | no se pudo recargar PostgreSQL: ... |
| `SQS-R001` | el respaldo no se puede restaurar (falló o se borró) |
| `SQS-R002` | escribe el identificador de la biblioteca para confirmar |
| `SQS-R003` | no se puede reemplazar una biblioteca en estado ... |
| `SQS-R010` | pg_restore falló: ... |
| `SQS-R011` | la estructura restaurada no coincide con el manifiesto (... tablas, esperadas ...) |
| `SQS-R012` | los conteos no coinciden: ... |
| `SQS-R020` | siguen ... sesiones abiertas en la biblioteca; pide que cierren Resolve o marca cerrar sesiones |
| `SQS-R030` | no hay un estado anterior que recuperar |
| `SQS-R031` | ya no existe la base del estado anterior (...) |
| `SQS-R040` | solo se recrean bibliotecas sin base en el servidor |
| `SQS-R041` | la biblioteca no tiene respaldos verificados |
| `SQS-S001` | el volumen no tiene marcador de sqsolve |
| `SQS-S002` | marcador ilegible en ...: ... |
| `SQS-S003` | el volumen de respaldos pertenece a otra instalación (...); esta es ... |
| `SQS-S004` | el volumen de respaldos tiene archivos pero no es de sqsolve. Monta una carpeta vacía o define SQSOLVE_BACKUPS_ADOPT=1 si sabes que es la correcta |
| `SQS-X001` | programa no permitido: ... |
| `SQS-X002` | no se encontró ... |
| `SQS-X003` | credencial con saltos de línea |
| `SQS-X010` | no se pudo abrir ...: ... |
| `SQS-X011` | la migración ... cambió después de aplicarse |
| `SQS-X012` | la base de datos tiene la migración ..., más nueva que esta versión de sqsolve; usa la versión con la que se creó o restaura la copia previa |
| `SQS-X013` | no se pudo copiar la base antes de migrar: ... |
| `SQS-X014` | integridad de SQLite: ... |
| `SQS-X900` |  |
| `SQS-X998)` |  |
| `SQS-X999` | error interno: ... |
| `SQS-X999)` |  |

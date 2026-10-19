# sqsolve

Servidor libre y autohospedado de bibliotecas de proyectos para DaVinci Resolve, administrable
desde el navegador. El nombre junta **SQ**L (PostgreSQL) y Re**solve**.

> Estado: **versión preliminar (0.x)**. Bibliotecas, accesos, respaldos, restauración, panel y
> API están implementados y probados contra PostgreSQL 18, pero la interacción con DaVinci
> Resolve todavía no se valida con estaciones reales (pruebas de concepto en
> [`docs/02-pruebas-de-concepto.md`](docs/02-pruebas-de-concepto.md)). No lo uses con proyectos
> de clientes hasta que esas pruebas pasen.

## Contenido

1. [Qué hace](#qué-hace)
2. [Cómo está armado](#cómo-está-armado)
3. [Requisitos](#requisitos)
4. [Instalación](#instalación)
5. [Primer arranque](#primer-arranque)
6. [Bibliotecas y estaciones de Resolve](#bibliotecas-y-estaciones-de-resolve)
7. [Respaldos](#respaldos)
8. [Restaurar](#restaurar)
9. [Accesos y seguridad de cada biblioteca](#accesos-y-seguridad-de-cada-biblioteca)
10. [Usuarios del panel, avisos y API](#usuarios-del-panel-avisos-y-api)
11. [Consola](#consola)
12. [Actualizar](#actualizar)
13. [Mover a otro servidor o recuperar un desastre](#mover-a-otro-servidor-o-recuperar-un-desastre)
14. [Solución de problemas](#solución-de-problemas)
15. [Desarrollo](#desarrollo)
16. [Documentación y licencia](#documentación-y-licencia)

## Qué hace

DaVinci Resolve guarda sus proyectos en "bibliotecas de proyectos". Para que varias estaciones
compartan proyectos, la biblioteca vive en un servidor PostgreSQL. sqsolve es ese servidor,
empaquetado en contenedores y administrado desde la web:

- crea bibliotecas, cada una con su propio usuario de PostgreSQL, para que un cliente no vea ni
  toque los proyectos de otro;
- te da los datos de conexión para cada estación y, si quieres, una contraseña por persona;
- respalda cada biblioteca a mano o con horario, verifica cada respaldo y hace restauraciones de
  prueba para saber que sí sirven;
- restaura como biblioteca nueva o en lugar de la actual, con un respaldo automático previo y
  la opción de revertir;
- muestra de un vistazo si cada biblioteca está protegida (la "línea de tiempo de protección"),
  avisa por webhook, ntfy o correo cuando algo falla y funciona sin Internet.

No es un editor ni un servidor de medios: administra la base de datos de proyectos y sus
respaldos. Tus clips siguen en tu almacenamiento de siempre.

## Cómo está armado

```
 Estaciones con Resolve ──TCP 5432──▶ postgres   (imagen oficial de PostgreSQL 18)
                                         ▲ socket interno
 Navegador ─────────────HTTPS 9432──▶ sqsolve    (panel, API, respaldos, tareas)
                                         │
              volúmenes: db (SSD) · app (SSD) · respaldos (otro disco)
```

- **postgres**: la imagen oficial `postgres:18.6-trixie`, fijada por digest. Las estaciones se
  conectan aquí.
- **sqsolve**: un solo binario de Go sobre esa misma imagen, para que `pg_dump` y `pg_restore`
  tengan siempre la versión del servidor. Corre sin privilegios, con el sistema de archivos de
  solo lectura y sin capacidades de Linux. Habla con PostgreSQL solo por el socket interno.
- **sqsolve-init**: corre una vez en cada arranque, sin red. Prepara carpetas, permisos, secretos
  y la configuración de PostgreSQL, y termina.

Tres carpetas tuyas (ADR 0018):

| Volumen | Dentro de los contenedores | Qué guarda | Dónde ponerlo |
|---|---|---|---|
| db | `/var/lib/postgresql` | Los datos de PostgreSQL y la configuración que genera sqsolve | SSD |
| app | `/var/lib/sqsolve` | Catálogo, usuarios del panel, secretos y certificado del panel | SSD |
| respaldos | `/backups` | Respaldos, manifiestos y copias del catálogo | **Otro disco**, para no perder datos y respaldos juntos |

## Requisitos

- Linux con Docker Engine y Docker Compose v2, en `amd64` o `arm64`. Sirve un NAS que acepte
  Compose (TrueNAS con "Install via YAML", Portainer, Unraid) o cualquier servidor Linux.
- Los puertos 5432 (estaciones) y 9432 (panel) libres en esa máquina.
- Las estaciones en la misma red local o conectadas por la VPN del estudio. **No publiques 5432
  ni 9432 a Internet.**
- Espacio: el de tus bibliotecas en `db`, más varias veces su tamaño comprimido en
  `respaldos`, según cuántos respaldos guardes.

Probado en CI con Docker Compose en Ubuntu, con el usuario 1000 y con el 568 (el de las apps de
TrueNAS). En TrueNAS mismo: Por confirmar.

## Instalación

### 1. Crea las carpetas

Tres carpetas vacías: dos en SSD y la de respaldos de preferencia en otro disco. Por ejemplo:

```
/mnt/ssd/sqsolve/db
/mnt/ssd/sqsolve/app
/mnt/discos/sqsolve-respaldos
```

En TrueNAS conviene un dataset por carpeta, todos con el preset **Generic**. Los otros presets
dejan ACL de tipo NFSv4 y con eso PostgreSQL no arranca (`sqsolve-init` lo detecta y se detiene
con el error `SQS-C054`).

### 2. Ajusta el compose

Copia [`deploy/compose.yaml`](deploy/compose.yaml) y cambia solo esto:

1. Las tres rutas marcadas con `Volúmenes:` (cada una aparece en varios servicios; cámbialas
   todas igual).
2. `TZ`, tu zona horaria (por ejemplo `America/Mexico_City`). Los horarios de respaldo usan esta
   zona.
3. La versión de la imagen en `x-sqsolve` (`ghcr.io/donutinit/sqsolve:<versión>`). Usa una
   versión fija, no `edge`.
4. Si el puerto 5432 ya está ocupado en el host (pasa en Synology), publícalo solo en la IP de
   la LAN: `"192.168.1.10:5432:5432"`.

Si tu NAS exige otro usuario para las apps (en TrueNAS es el 568), cambia **las tres** cosas
juntas: `SQSOLVE_UID`, `SQSOLVE_GID` y la línea `user:` del servicio `sqsolve`. Si no coinciden,
sqsolve se niega a arrancar y te dice cuál cambiar (error `SQS-C053`).

### 3. Levántalo

```
docker compose -f compose.yaml up -d
docker compose -f compose.yaml ps
```

En TrueNAS: Apps, "Install via YAML", pega el archivo ya editado.

### 4. Busca el código de instalación

```
docker compose -f compose.yaml logs sqsolve
```

Verás un recuadro con un código como `ABCDE-FGHJK`. Solo sirve hasta que crees el primer
administrador; si reinicias el contenedor antes, sale uno nuevo.

## Primer arranque

1. Abre `https://<ip-del-servidor>:9432`. El navegador avisará que el certificado no es de
   confianza: es normal, sqsolve se lo firmó a sí mismo. Acepta el aviso. Después puedes subir
   tu propio certificado en Ajustes.
2. El asistente pide:
   - el código de instalación;
   - tu usuario y una contraseña de al menos 12 caracteres;
   - **la dirección que usan las estaciones** para llegar al servidor (su IP en la LAN o un
     nombre). Es la que se muestra en las instrucciones de conexión;
   - el nombre de la instalación (aparece en el panel y en los avisos);
   - el horario de respaldo por defecto para las bibliotecas nuevas: diario a las 02:00, cada 4
     horas de 09:00 a 21:00 de lunes a sábado, o cada 4 horas todos los días. Cada biblioteca
     se puede cambiar después.
3. **Descarga el kit de recuperación** y guárdalo fuera del servidor (gestor de contraseñas, USB
   en un cajón). Trae la clave maestra que cifra las contraseñas de las bibliotecas. Sin él,
   en otro servidor recuperas los proyectos pero las estaciones tendrán que usar contraseñas
   nuevas. Lo puedes volver a descargar en Ajustes.

Recomendado: activa el segundo factor (TOTP) en "Cuenta".

## Bibliotecas y estaciones de Resolve

### Crear una biblioteca

1. Panel, "Nueva biblioteca". Escribe un nombre (por ejemplo "Cliente X, temporada 2").
   sqsolve propone un identificador (`cliente_x_temporada_2`): minúsculas, números y guion
   bajo, de 3 a 40 caracteres. El identificador es el nombre de la base y del usuario en
   PostgreSQL y no cambia después; el nombre visible sí.
2. Se abre una **ventana de inicialización** de 60 minutos. En **una sola estación**:
   - abre el Project Manager de Resolve y agrega una biblioteca de proyectos en red
     (PostgreSQL) con la opción **Create**;
   - como nombre de la biblioteca escribe **exactamente el identificador**;
   - en ubicación, la IP o el nombre del servidor (sin puerto);
   - el usuario y la contraseña que muestra el panel ("Mostrar contraseña" pide tu contraseña
     del panel si no la escribiste hace poco).
3. Resolve crea la base y sus tablas. sqsolve lo detecta solo en unos segundos, cierra la
   ventana, quita el permiso de crear bases y hace el primer respaldo. La biblioteca pasa a
   "Activa".

Si en Resolve se escribió otro nombre, el panel te dice qué base se creó y cuál debía ser. Si
la ventana vence, "Abrir otra ventana".

Cómo se llaman exactamente los botones y campos cambia entre versiones de Resolve. El flujo
"Create la primera vez, Connect las demás" está reportado por la comunidad y falta comprobarlo
con estaciones reales (PoC-01).

### Conectar las demás estaciones

En cada estación: agrega la biblioteca en red con **Connect** y los mismos datos. La página
"Instrucciones" de cada biblioteca tiene todo listo para imprimir o mandar. Resolve no pide el
puerto: usa 5432. Para colaborar en tiempo real, todas las estaciones necesitan Resolve Studio en
la misma versión y los puertos TCP 50059 a 50314 abiertos **entre estaciones** (ese tráfico no
pasa por sqsolve).

### Mantenimiento, papelera y purga

- **Mantenimiento**: la biblioteca deja de aceptar conexiones nuevas; puedes cerrar las abiertas.
- **Eliminar**: hace un respaldo final, la manda a la papelera 30 días y la saca de la red. Se
  puede sacar de la papelera.
- **Purgar**: borra la base y sus usuarios para siempre (los respaldos se quedan según su
  retención). Pide escribir el identificador.

## Respaldos

- **Automáticos** según el horario de cada biblioteca (pestaña Programación), con un pequeño
  desfase aleatorio para que no arranquen todos a la vez. También "Respaldar ahora".
- Cada respaldo es un `pg_dump` en formato custom tomado de una foto consistente de la base,
  con conteo exacto de filas en esa misma foto, suma SHA-256 y un manifiesto JSON al lado.
  Se escribe primero como `.partial` y solo se renombra cuando está completo.
- **Verificación**: cada respaldo se comprueba al terminar (suma e índice legible). Cada
  domingo a las 05:00 se hace una **restauración de prueba** del último respaldo de cada
  biblioteca en una base temporal y se comparan estructura y filas con el manifiesto. Puedes
  pedirla a mano en cualquier respaldo.
- **Retención** por biblioteca: por defecto los últimos 10, uno por día 14 días, uno por semana
  8 semanas y uno por mes 12 meses, y nunca se borra el último verificado. Los respaldos fijados
  y los previos a restaurar o eliminar se protegen. La pestaña Programación muestra cuántos se
  conservarían antes de guardar.
- **Línea de tiempo de protección**: cada biblioteca es una pista y cada respaldo un marcador
  (alto si pasó la restauración de prueba, medio si pasó suma e índice, punteado sin verificar,
  cruz roja si falló). La franja ámbar marca tiempo sin respaldo verificado (48 horas por
  defecto).
- **En disco**:
  `respaldos/libraries/<identificador>/<año>/<mes>/<identificador>_<fecha>_<id>.backup` más su
  `.manifest.json`. Son archivos estándar: se pueden restaurar con `pg_restore` aunque sqsolve no
  exista.
- **Descargar** un respaldo desde su página. **Importar** uno de fuera (de otro servidor, del
  Project Server oficial o de una descarga): súbelo desde "Importar", o cópialo a
  `respaldos/_import/` y elígelo de la lista (mejor para archivos de varios GB; si marcas borrar
  el original, se mueve en vez de copiarse).

Si el disco de respaldos se desmonta, sqsolve lo nota (cada volumen lleva un marcador) y no
escribe respaldos en una carpeta vacía equivocada.

## Restaurar

Desde la página de un respaldo, "Restaurar":

- **Como biblioteca nueva** (recomendado): crea otra biblioteca con su propio usuario y
  contraseña. La original no se toca. Sirve para recuperar un proyecto borrado sin afectar a
  nadie.
- **Reemplazar la biblioteca actual**: primero hace un respaldo de cómo está ahora, restaura en
  una base aparte, la compara con el manifiesto, espera a que cierren Resolve (o cierra las
  sesiones si lo marcas) e intercambia las bases. La versión anterior se guarda 7 días para
  **revertir** desde la pestaña Zona de peligro. Pide escribir el identificador y confirmar tu
  contraseña.

Si sqsolve o el servidor se caen a media restauración, al arrancar lee el diario de la tarea y
deja la biblioteca en un estado coherente.

## Accesos y seguridad de cada biblioteca

- **Una contraseña por persona** (pestaña Accesos): crea usuarios como
  `cliente_x_temporada_2__maria`. Lo que crea esa persona queda a nombre de la biblioteca, y
  puedes revocarla sin cambiar la contraseña de todos.
- **Cambiar contraseña**: las estaciones que la usan tendrán que escribir la nueva.
- **Reglas de red**: limita desde qué redes se entra a esa biblioteca (por ejemplo
  `192.168.10.0/24`). Sin reglas, se entra desde cualquier dirección que llegue al puerto.
- **Sesiones**: quién está conectado ahora y desde dónde; puedes cerrar una sesión.
- Los usuarios administrativos de PostgreSQL (`postgres` y `sqsolve_admin`) no aceptan
  conexiones por red, solo por el socket interno.
- Las contraseñas de las bibliotecas se guardan cifradas con la clave maestra y solo se muestran
  a quien confirmó su contraseña del panel hace poco; cada vez queda en la auditoría.

## Usuarios del panel, avisos y API

- **Roles**: administrador (todo), operador (respaldos, restaurar como nueva, mantenimiento) y
  lector (solo ver). Ajustes, Usuarios.
- **Segundo factor** TOTP con 10 códigos de recuperación, en Cuenta.
- **Avisos**: Ajustes, Notificaciones: webhook, ntfy o correo (SMTP). sqsolve no envía nada
  fuera del servidor hasta que agregues un canal.
- **Actividad**: eventos y una auditoría encadenada por hashes, exportable en JSONL.
- **Salud**: PostgreSQL, espacio de cada volumen, certificados y bases del servidor.
- **API REST** en `/api/v1`, con tokens que creas en Cuenta (alcances: lectura, respaldos,
  restaurar, administración). Errores en formato RFC 9457. Ejemplos:

```
TOKEN=sqs_...
curl -k -H "Authorization: Bearer $TOKEN" https://servidor:9432/api/v1/libraries
curl -k -X POST -H "Authorization: Bearer $TOKEN" https://servidor:9432/api/v1/libraries/<id>/backups
curl -k -H "Authorization: Bearer $TOKEN" https://servidor:9432/api/v1/jobs/<id>
```

`GET /api/v1/system/health` no pide token y responde 503 si PostgreSQL o el volumen de
respaldos no están bien: sirve para un monitor externo.

## Consola

Dentro del contenedor (las acciones quedan en la auditoría con actor "consola"):

```
docker compose exec sqsolve sqsolve doctor                     # revisa todo y dice qué hacer
docker compose exec sqsolve sqsolve library list
docker compose exec sqsolve sqsolve backup create <identificador> --wait
docker compose exec sqsolve sqsolve backup list <identificador>
docker compose exec sqsolve sqsolve restore <id-respaldo> --new <identificador-nuevo> --wait
docker compose exec sqsolve sqsolve admin list
docker compose exec sqsolve sqsolve admin reset-password <usuario>
docker compose exec sqsolve sqsolve admin disable-totp <usuario>
docker compose exec sqsolve sqsolve catalog rebuild --wait     # recrea el catálogo desde los manifiestos
docker compose exec sqsolve sqsolve audit verify
```

`sqsolve help` muestra todos los comandos.

## Actualizar

1. Antes, haz un respaldo de todo (o confirma en el panel que todo tiene un verificado
   reciente).
2. Cambia la versión de la imagen en el compose y corre:

```
docker compose pull
docker compose up -d
```

`sqsolve-init` vuelve a correr y regenera la configuración. Si la versión nueva cambia el
catálogo, sqsolve guarda antes una copia en `app/state-backups/`. Para volver atrás, regresa a
la versión anterior; si ya migró, restaura esa copia.

La imagen de PostgreSQL está fijada por digest en el compose; las actualizaciones menores llegan
con versiones nuevas de sqsolve. Cambiar de versión mayor de PostgreSQL todavía no está
automatizado (ADR 0017).

## Mover a otro servidor o recuperar un desastre

Necesitas **la copia del volumen de respaldos** y **el kit de recuperación**.

1. En el servidor nuevo, instala como en [Instalación](#instalación), pero monta la copia de los
   respaldos como volumen `respaldos` y deja `db` y `app` vacíos.
2. `sqsolve-init` reconoce el volumen y adopta la identidad de la instalación anterior.
3. En el asistente elige "Recuperar una instalación existente", escribe el código de instalación
   y pega la clave maestra del kit. sqsolve restaura su catálogo desde `_system` y se reinicia.
4. Cada biblioteca aparece como "La base no existe en el servidor": "Recrear desde el último
   respaldo". Con la misma clave maestra, las estaciones conservan su contraseña.

Sin el kit, instala desde cero, corre `sqsolve catalog rebuild` para ver los respaldos y
restáuralos como bibliotecas nuevas (con contraseñas nuevas).

## Solución de problemas

Primero: `docker compose exec sqsolve sqsolve doctor` y la página Salud. Los errores traen un
código `SQS-...` que también aparece en la API.

| Qué pasa | Por qué | Qué hacer |
|---|---|---|
| No aparece el código de instalación | Ya hay un administrador, o miraste el registro de otro contenedor | `docker compose logs sqsolve`. Si ya hay administrador y no puedes entrar: `sqsolve admin reset-password` |
| `SQS-C053` al arrancar | `user:` del servicio `sqsolve` no coincide con `SQSOLVE_UID`/`SQSOLVE_GID` | Pon los mismos números en los tres lugares y vuelve a levantar |
| `SQS-S004` en `sqsolve-init` | La carpeta de respaldos tiene archivos que no son de sqsolve | Monta una carpeta vacía. Si de verdad es la correcta, `SQSOLVE_BACKUPS_ADOPT=1` |
| `SQS-S003` | Ese volumen de respaldos es de otra instalación | Revisa que montaste la carpeta correcta |
| Errores de permisos tras cambiar de usuario o copiar carpetas | Los dueños no corresponden | Agrega `SQSOLVE_FIX_PERMS: "1"` al servicio `sqsolve-init`, levanta una vez y quítalo |
| `SQS-C054` en `sqsolve-init`, o PostgreSQL se reinicia con `mkdir: cannot create directory '/var/lib/postgresql/18': Permission denied` o `data directory ... has invalid permissions` | La carpeta `db` usa ACL de tipo NFSv4 y no respeta los permisos que PostgreSQL necesita | Crea la carpeta `db` en un dataset con permisos POSIX (en TrueNAS, preset Generic) |
| `port is already allocated` en 5432 | Otro PostgreSQL en el host (Synology) | Publica `"IP-de-la-LAN:5432:5432"` |
| Resolve no conecta | Red, firewall, dirección o regla de red | Desde la estación, que llegue a `IP:5432`; revisa la dirección anunciada en Ajustes y las reglas de la biblioteca; el registro de `postgres` dice por qué rechazó (`pg_hba.conf rejects connection for host ...`) |
| Rechazos con una IP `172.x` en el registro de `postgres` | Tu Docker reescribe la IP de origen | Las reglas de red no ven la IP real de la estación: quítalas o pon esa red |
| La biblioteca no se activa | El nombre en Resolve no era el identificador, o venció la ventana | El panel muestra la base que se creó; reabre la ventana y crea con el nombre exacto |
| `SQS-R020` al reemplazar | Resolve sigue abierto con esa biblioteca | Pide que lo cierren o marca "cerrar sesiones" |
| `SQS-B012` | No hay espacio en el disco de respaldos | Libera espacio o ajusta la retención |
| `SQS-L046` al purgar | El usuario de la biblioteca todavía es dueño de otra base (por ejemplo, una creada con el nombre equivocado) | Revisa esa base y bórrala a mano si no sirve |
| `SQS-A003` | Demasiados intentos de entrada | Espera el tiempo que indica |
| Perdiste el segundo factor | | `sqsolve admin disable-totp <usuario>` |
| El panel dice "PostgreSQL sin conexión" | El contenedor `postgres` está detenido o reiniciando | `docker compose ps` y `docker compose logs postgres`; sqsolve se reconecta solo |

Si necesitas ayuda, Ajustes, Mantenimiento tiene un paquete de diagnóstico sin secretos.

## Desarrollo

Go 1.26 o más nuevo y, para las pruebas de integración, los binarios de PostgreSQL 18.

| Comando | Qué hace |
|---|---|
| `make build` | Compila `./sqsolve` |
| `make check` | Formato, `go vet`, staticcheck, pruebas con `-race` y govulncheck |
| `make test-integration` | Pruebas que exigen PostgreSQL real (`SQSOLVE_TEST_PG_BIN` si no está en el PATH) |
| `make scenarios` | Fallas provocadas: estaciones simuladas escribiendo durante un respaldo, sqsolve muerto a media tarea, PostgreSQL caído, restauración con Resolve abierto, carpeta sin permisos |
| `make image` | Construye la imagen local |

CI (`.github/workflows`) corre todo lo anterior, levanta `deploy/compose.yaml` tal cual con la
imagen recién construida y hace un recorrido completo con los usuarios 1000 y 568. Cada push a
`main` publica `ghcr.io/donutinit/sqsolve:edge`; cada etiqueta `vX.Y.Z` publica esa versión para
amd64 y arm64.

Las reglas para quien programe (incluidos agentes) están en [`AGENTS.md`](AGENTS.md).

## Documentación y licencia

La arquitectura, las decisiones (ADR), la investigación y las tareas están en
[`docs/`](docs/README.md).

Código: AGPL-3.0-only. Documentación: CC BY-SA 4.0. Ver
[`docs/adr/0001-licencia.md`](docs/adr/0001-licencia.md).

DaVinci Resolve y Blackmagic Design son marcas de Blackmagic Design Pty. Ltd. sqsolve es un
proyecto independiente, sin relación con Blackmagic Design ni con WireBear.

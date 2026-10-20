# ADR 0021. Seguridad proporcional a la red local y la VPN

**Estado:** Aceptado (2026-10-19). Decisión de Diego.

**Contexto.** sqsolve está pensado para una LAN de estudio o una VPN (ADR 0015: el puerto 5432 y
el panel nunca salen a Internet). Con ese modelo, el panel pedía además confirmar la contraseña
cada 5 minutos para ver una contraseña de biblioteca, rotarla, dar de alta personas, cambiar
reglas de red, eliminar, purgar, revertir o reemplazar; exigía 12 caracteres de contraseña y
cerraba la sesión a las 8 horas sin uso. Esa fricción no compra seguridad que la red no dé ya:
quien llega al panel ya está dentro de la red, y el SSH del NAS es un objetivo más fácil que la
interfaz. Diego lo resumió así el 2026-10-19: usuario con contraseña normal y ya.

**Decisión.**

- **Sin reautenticación.** Desaparecen la pantalla "Confirmar identidad", la ruta `/confirmar`,
  `FreshAuth` y el error `SQS-A007`. Quien tiene el permiso ejecuta la acción; cada una sigue
  quedando en la auditoría con usuario e IP.
- **Las confirmaciones contra pérdida de datos se quedan:** escribir el identificador para
  eliminar, purgar, revertir o reemplazar, y `data-confirm` en el resto de acciones destructivas.
  No son seguridad, son freno contra el error.
- **Contraseña mínima de 8 caracteres**, con al menos 4 distintos, sin el nombre de usuario y
  fuera de la lista de contraseñas comunes. El medidor sigue siendo informativo.
- **Sesiones largas:** 30 días sin uso, 180 días en total. Se cierran desde Cuenta.
- **HTTPS obligatorio en el panel.** `SQSOLVE_INSECURE_HTTP` sigue existiendo solo para
  desarrollo y las pruebas de CI; no se documenta como opción de uso.
- **Bloqueo de cuenta por intentos.** Además del freno por IP y por cuenta, diez contraseñas
  incorrectas seguidas bloquean la cuenta (`SQS-A004`), cierran sus sesiones y emiten el evento
  `auth.locked`. El panel no la desbloquea: solo `sqsolve admin unlock <usuario>` desde la
  consola del contenedor, o `admin reset-password`, que también limpia el contador. Quien puede
  entrar al contenedor ya es dueño del servidor; el panel no tiene por qué ofrecer esa salida.
- Se conservan: código de instalación de un solo uso, límite de intentos de entrada, TOTP
  opcional, roles, tokens con alcances, CSRF, cabeceras, cifrado de credenciales con la clave
  maestra y la cadena de auditoría. Nada de esto estorba en el uso diario.

**Consecuencias.** `docs/08` (T-10, T-12, matriz de permisos), `docs/09`, `docs/10` y `docs/12`
cambian en el mismo commit. La regla de `AGENTS.md` sobre secretos pasa de "nunca sin
reautenticación" a "solo a quien tiene el permiso, con auditoría". Si algún día el panel se
expone fuera de la red del estudio, este ADR se revisa antes.

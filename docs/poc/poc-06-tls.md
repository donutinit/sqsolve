# PoC-06. TLS y SCRAM desde Resolve

Fecha: 2026-10-19. Servidor: mininas, sqsolve 0.1.5, PostgreSQL 18.6 con el certificado
autofirmado de `sqsolve-init`, `ssl = on`, `ssl_min_protocol_version = TLSv1.2`,
`password_encryption = scram-sha-256`. Estación: Linux, DaVinci Resolve Studio 21.1.1.10, la
biblioteca `apolo` agregada en el Project Manager con IP, usuario y contraseña, sin ninguna
opción de TLS (Resolve no la expone).

## Procedimiento y resultados literales

1. Con `pg_hba.conf` en líneas `host` (TLS oportunista), sesión de Resolve ya abierta:

   ```
   apolo|192.168.50.99|Resolve_0007433F5FC0|t|TLSv1.3   (cifrado TLS_AES_256_GCM_SHA384)
   ```

   Resolve negocia TLS por sí solo (`sslmode=prefer` de libpq) y acepta el certificado
   autofirmado sin verificarlo.

2. Modo "Solo TLS" activado en Ajustes (líneas `hostssl`):

   | Cliente | Resultado |
   |---|---|
   | `psql ... sslmode=disable` | `FATAL: pg_hba.conf rejects connection for host "192.168.50.99", user "apolo", database "apolo", no encryption` |
   | `psql` por defecto | conecta, `ssl = t`, TLSv1.3 |
   | Resolve, conexión nueva forzada con la API de scripting (`SetCurrentDatabase` a otra base y de vuelta a `apolo`) | `True`; lista los proyectos; en `pg_stat_ssl` sus seis conexiones salen con `ssl = t`, TLSv1.3 |

3. Modo "Solo TLS" desactivado: `psql ... sslmode=disable` vuelve a entrar en claro.

SCRAM: todas las conexiones anteriores autenticaron con `scram-sha-256`; Resolve no necesitó
nada especial.

## Decisiones que habilita

- **TLS puede ser obligatorio** con Resolve 21.1.1 en Linux: el modo "Solo TLS" es seguro de
  activar en esa combinación. Sigue apagado por defecto y con advertencia porque no se probó
  con Resolve en macOS ni en Windows ni con versiones anteriores; la columna "Cifrado" de las
  sesiones permite comprobarlo antes de activarlo en cada instalación.
- Resolve no verifica el certificado: el cifrado protege contra escucha pasiva, no contra
  suplantación del servidor. La recomendación de VPN fuera de la LAN (ADR 0015) se mantiene.
- `ssl_min_protocol_version = TLSv1.3` no se probó; con 21.1.1 negociando 1.3 es viable, pero
  queda para cuando haya evidencia de las otras plataformas.

## Estado

VERIFICADO para Resolve 21.1.1 en Linux. REPORTADO para macOS y Windows (misma libpq, sin
probar).

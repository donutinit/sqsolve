# PoC-08. Red: IP de origen en Docker, puerto y VPN

Fecha: 2026-10-19. Servidor: mininas, TrueNAS 25.10.7, sqsolve 0.1.5 como Custom App (Docker,
puertos publicados `5432:5432` y `9432:9432`), PostgreSQL 18.6. Cliente: la estación de Diego
(Linux, 192.168.50.99) con `psql` 18 y DaVinci Resolve Studio 21.1.1. VPN: la app de Tailscale
de TrueNAS en el mismo NAS (100.92.52.75).

## Procedimiento y resultados literales

Consulta en cada conexión, como el usuario de la biblioteca `apolo`:

```sql
SELECT inet_client_addr(), client_addr, ssl, version
FROM pg_stat_activity a LEFT JOIN pg_stat_ssl s USING (pid) WHERE a.pid = pg_backend_pid();
```

| Desde | `client_addr` que ve PostgreSQL | TLS |
|---|---|---|
| LAN, `psql -h 192.168.50.172` | `192.168.50.99` (la IP real de la estación) | `t`, TLSv1.3 |
| LAN, Resolve 21.1.1 (sesión `Resolve_0007433F5FC0`) | `192.168.50.99` | `t`, TLSv1.3 |
| Tailscale, `psql -h 100.92.52.75` | `172.16.10.1` | `t`, TLSv1.3 |
| LAN, `psql ... sslmode=disable` | `192.168.50.99` | `f` (aceptada: `host`, no `hostssl`) |

## Decisiones que habilita

- **La IP real de la estación se ve en la LAN** con Docker en TrueNAS: las reglas de red por
  biblioteca (`pg_hba.conf`) funcionan con las subredes de la LAN tal cual.
- **Por Tailscale, la app de TrueNAS traduce la dirección de origen**: toda conexión que entra
  por la VPN llega como `172.16.10.1`, la IP del contenedor de Tailscale. Una regla de red para
  "los colaboradores por VPN" tiene que permitir esa dirección (o su red), no las direcciones
  `100.x`; y no se puede distinguir a un colaborador de otro por IP a través de esa app. Lo
  documenta el README en "Reglas de red".
- El puerto 5432 funciona publicado sin más configuración; el 50059 de colaboración no pasa por
  sqsolve (ADR 0012) y queda para PoC-07.
- Con las líneas `host` de hoy, una conexión en claro entra. Pasar a `hostssl` es el modo "Solo
  TLS" (PoC-06).

## Estado

VERIFICADO para LAN y para la app de Tailscale de TrueNAS. Otras VPN (WireGuard en el host,
Headscale) sin probar: la IP vista depende de si hacen NAT.

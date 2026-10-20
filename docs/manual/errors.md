# Error codes

Generated with `make errors` from the source code; do not edit by hand. Each code
is listed with the text shown in the panel, the API, the console or the log.

| Code | Message |
|---|---|
| `SQS-A001` | user not found |
| `SQS-A002` | incorrect username or password |
| `SQS-A003` | too many attempts; wait ... |
| `SQS-A004` | account locked after incorrect passwords; in the container console: sqsolve admin unlock <username> |
| `SQS-A005` | second factor required |
| `SQS-A006` | incorrect code |
| `SQS-A010` | weak password |
| `SQS-A011` | invalid username: 2 to 32 characters, letters, digits, dot, hyphen or underscore |
| `SQS-A012` | invalid role |
| `SQS-A013` | a user with that name already exists |
| `SQS-A014` | at least one active administrator must remain |
| `SQS-A015` | the passwords do not match |
| `SQS-A020` | the token needs a name of up to 80 characters |
| `SQS-A021` | unknown scope: ... |
| `SQS-A022` | choose at least one scope |
| `SQS-A023` | token not found |
| `SQS-A024` | invalid, expired or revoked token |
| `SQS-A030` | replacing requires the restore or admin scope |
| `SQS-A031` | missing X-CSRF-Token header |
| `SQS-A040` | the second factor is required for administrators on this installation |
| `SQS-B001` | backup not found |
| `SQS-B002` | database ... does not exist |
| `SQS-B003` | cannot back up a library in state ... |
| `SQS-B010` | pg_dump failed: ... |
| `SQS-B011` | row count: ... |
| `SQS-B012` |  |
| `SQS-B020` | verification failed: ... |
| `SQS-B021` | the backup is not available (...) |
| `SQS-B022` | not enough space on the database volume for the test restore (... needed) |
| `SQS-B030` | retention values must be between 0 and 1000 |
| `SQS-B031` | retention must keep at least one backup |
| `SQS-B032` | keep at least one verified backup (1 to 100) |
| `SQS-B033` | activity-based mode checks every 5, 10, 15, 30 or 60 minutes |
| `SQS-B034` | it is the most recent verified backup of the library; it cannot be deleted manually |
| `SQS-B040` | unreadable manifest: ... |
| `SQS-B041` | pg_restore cannot read the file: ... |
| `SQS-B050` | choose a file from the import folder or upload one |
| `SQS-B051` | the file is outside the import folder |
| `SQS-B052` | could not save the upload: ... |
| `SQS-B054` | there is already a file named ... in the import folder; import it from the list or rename it |
| `SQS-B060` | the window stops: ... was not backed up and verified: ... |
| `SQS-C001` | ...: ... |
| `SQS-C010` | could not read the master key (did you run sqsolve init?): ... |
| `SQS-C011` | the master key is not valid base64: ... |
| `SQS-C012` | could not decrypt the secret |
| `SQS-C013` | sqsolve_admin password: ... |
| `SQS-C014` | superuser password: ... |
| `SQS-C020` | panel certificate: ... |
| `SQS-C021` | the certificate and the key do not match: ... |
| `SQS-C030` | invalid advertised address: type only the IP or the name, without a port |
| `SQS-C031` | the initialization window must be between 5 and 1440 minutes |
| `SQS-C032` | retention periods must be at least one day |
| `SQS-C040` | type the target Resolve version |
| `SQS-C050` | could not create ...: ... |
| `SQS-C051` | backups volume: ... |
| `SQS-C052` | ... is missing: run sqsolve init (the sqsolve-init service of the compose file): ... |
| `SQS-C053` | the service runs as ...:... but SQSOLVE_UID and SQSOLVE_GID say ...:.... |
| `SQS-C054` | PostgreSQL will not be able to use the database volume (...). |
| `SQS-C060` | The request body is too large |
| `SQS-C061` | invalid JSON body: ... |
| `SQS-C062` | unknown API route |
| `SQS-C063` | mode must be new or replace |
| `SQS-C070` | the master key must be the base64 text from the kit (44 characters) |
| `SQS-C071` | there are no control plane copies in the backups volume (_system) |
| `SQS-C072` | the key does not match any control plane copy: ... |
| `SQS-E001` | the library does not have the tables and columns the experimental export knows |
| `SQS-E002` | experimental export is disabled; an administrator can enable it in Settings |
| `SQS-E003` | does not exist in the library |
| `SQS-E004` | the experimental export does not reproduce this feature |
| `SQS-E005` | could not open the library: ... |
| `SQS-E006` | array with unexpected format: ... |
| `SQS-E007` | export space ... does not exist |
| `SQS-J001` | unknown job type |
| `SQS-J002` | job not found |
| `SQS-J003` | this job cannot be cancelled right now |
| `SQS-J004` | only failed, cancelled or interrupted jobs can be retried |
| `SQS-J010',` | error_detail = 'The process stopped while the job was running' WHERE id = ? |
| `SQS-J020` | invalid schedule expression |
| `SQS-J021` | unknown schedule kind: ... |
| `SQS-L001` | invalid identifier |
| `SQS-L002` | library not found |
| `SQS-L003` | the display name cannot contain control characters |
| `SQS-L004` | a library with identifier ... already exists |
| `SQS-L005` | a database ... already exists |
| `SQS-L006` | a role ... already exists on the server |
| `SQS-L007` | unknown creation strategy |
| `SQS-L010` | the library is not being prepared (...) |
| `SQS-L011` | the library is not waiting for initialization |
| `SQS-L012` | invalid connection limit (-1 for unlimited, up to 1000) |
| `SQS-L013` | Resolve version too long |
| `SQS-L014` | notes too long |
| `SQS-L015` | cannot change maintenance in state ... |
| `SQS-L016` | the session no longer exists or does not belong to this library |
| `SQS-L020` | invalid network: ... |
| `SQS-L030` | credential not found |
| `SQS-L031` | the credential is revoked |
| `SQS-L032` | people can only be added to active libraries |
| `SQS-L033` | role ... already exists |
| `SQS-L034` | the primary credential cannot be revoked; rotate its password |
| `SQS-L040` | type the library identifier to confirm |
| `SQS-L041` | cannot delete a library in state ... |
| `SQS-L042` | the library is not in the trash |
| `SQS-L043` | another library with identifier ... already exists |
| `SQS-L044` | only libraries in the trash can be purged |
| `SQS-L045` | there is no verified final backup; not purging |
| `SQS-L046` | role ... still owns: ...; review them before purging |
| `SQS-N001` | unknown channel kind |
| `SQS-N002` | the destination responded ... |
| `SQS-N010` | invalid sender: ... |
| `SQS-N011` | invalid recipient: ... |
| `SQS-N012` | STARTTLS: ... |
| `SQS-N020` | channel not found |
| `SQS-N021` | the channel needs a name |
| `SQS-N022` | invalid severity |
| `SQS-N023` | invalid URL (use http:// or https://) |
| `SQS-N024` | email needs a server, sender and recipients |
| `SQS-N025` | invalid port |
| `SQS-N026` | invalid security setting |
| `SQS-N030` | the test failed: ... |
| `SQS-P001` |  |
| `SQS-P002` | preparing ...: ... |
| `SQS-P010` | literal with disallowed characters |
| `SQS-P020` | creating role ...: ... |
| `SQS-P021` | membership of ...: ... |
| `SQS-P030` | pg_reload_conf returned false |
| `SQS-P040` | administrative role in a library rule |
| `SQS-P041` | could not write pg_hba.conf: ... |
| `SQS-P042` | generated pg_hba.conf is not valid: ... |
| `SQS-P043` | could not reload PostgreSQL: ... |
| `SQS-R001` | the backup cannot be restored (it failed or was deleted) |
| `SQS-R002` | type the library identifier to confirm |
| `SQS-R003` | cannot replace a library in state ... |
| `SQS-R010` | pg_restore failed: ... |
| `SQS-R011` | the restored structure does not match the manifest (... tables, expected ...) |
| `SQS-R012` | row counts do not match: ... |
| `SQS-R020` | ... sessions are still open on the library; ask users to close Resolve or choose to terminate sessions |
| `SQS-R030` | there is no previous state to recover |
| `SQS-R031` | the previous-state database no longer exists (...) |
| `SQS-R040` | only libraries with no database on the server can be recreated |
| `SQS-R041` | the library has no verified backups |
| `SQS-S001` | the volume has no sqsolve marker |
| `SQS-S002` | unreadable marker in ...: ... |
| `SQS-S003` | the backups volume belongs to another installation (...); this one is ... |
| `SQS-S004` | the backups volume has files but does not belong to sqsolve. Mount an empty folder or set SQSOLVE_BACKUPS_ADOPT=1 if you know it is the right one |
| `SQS-X001` | program not allowed: ... |
| `SQS-X002` | ... not found |
| `SQS-X003` | credential with line breaks |
| `SQS-X010` | could not open ...: ... |
| `SQS-X011` | migration ... changed after being applied |
| `SQS-X012` | the database has migration ..., newer than this sqsolve version; use the version it was created with or restore the previous copy |
| `SQS-X013` | could not copy the database before migrating: ... |
| `SQS-X014` | SQLite integrity: ... |
| `SQS-X900` |  |
| `SQS-X998)` |  |
| `SQS-X999` | error interno: ... |
| `SQS-X999)` |  |

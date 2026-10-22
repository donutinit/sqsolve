# Error codes

Generated with `make errors` from the source code; do not edit by hand. Each code
is listed with the text shown in the panel, the API, the console or the log.

| Code | Message |
|---|---|
| `SQS-A001` | user not found |
| `SQS-A002` | incorrect username or password |
| `SQS-A003` | too many attempts; wait ... |
| `SQS-A004` | account locked for 15 minutes after 10 incorrect passwords in a row; try again later, or unlock it now in the container console: sqsolve admin unlock <username> |
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
| `SQS-A050` | the server is checking too many passwords at once; try again in a few seconds |
| `SQS-A051` | the second factor is required for administrators on this installation: enable it in the panel before continuing |
| `SQS-B001` | backup not found |
| `SQS-B002` | database ... does not exist |
| `SQS-B003` | cannot back up a library in state ... |
| `SQS-B010` | pg_dump failed: ... |
| `SQS-B011` | row count: ... |
| `SQS-B012` | not enough space on the backups volume: ... estimated, ... free |
| `SQS-B020` | verification failed: |
| `SQS-B021` | the backup is not available (...) |
| `SQS-B022` | not enough space on the database volume for ...: ... needed, ... free |
| `SQS-B030` | retention values must be between 0 and 1000 |
| `SQS-B031` | retention must keep at least one backup |
| `SQS-B032` | keep at least one verified backup (1 to 100) |
| `SQS-B033` | unpin the backup before deleting it |
| `SQS-B034` | it is the most recent verified backup of the library; it cannot be deleted manually |
| `SQS-B040` | unreadable manifest |
| `SQS-B041` | pg_restore cannot read the file: ... |
| `SQS-B050` | choose a file from the import folder or upload one |
| `SQS-B051` | the file is outside the import folder |
| `SQS-B052` | could not save the upload: ... |
| `SQS-B054` | there is already a file named ... in the import folder; import it from the list or rename it |
| `SQS-B060` | the window stops: ... was not backed up and verified: ... |
| `SQS-B070` | the verification could not run: |
| `SQS-B071` | ... is still being written (it went from ... to ...); import it when the copy has finished |
| `SQS-B072` | the archive is incomplete or damaged: ... |
| `SQS-B073` | the archive could not be read: ... |
| `SQS-B074` | ... changed during the import; nothing was imported and the file is still in the import folder: import it again once it no longer changes |
| `SQS-B090` | activity-based mode checks every 5, 10, 15, 30 or 60 minutes |
| `SQS-B091` | unknown schedule mode |
| `SQS-C001` | ...: ... |
| `SQS-C010` | permissions too open on a secrets file |
| `SQS-C011` | the master key is not valid base64: ... |
| `SQS-C012` | could not decrypt the secret |
| `SQS-C013` | sqsolve_admin password: ... |
| `SQS-C014` | superuser password: ... |
| `SQS-C020` | panel certificate: ... |
| `SQS-C021` | the certificate and the key do not match: ... |
| `SQS-C030` | invalid advertised address: type only the IP or the name, without a port |
| `SQS-C031` | the initialization window must be between 5 and 1440 minutes |
| `SQS-C032` | retention periods must be between 1 and ... days |
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
| `SQS-C080` | could not write the PostgreSQL certificate: ... |
| `SQS-C081` | could not mark the ... volume: ... |
| `SQS-C082` | could not renew the panel certificate, which expires on ...: ... |
| `SQS-C083` | the panel certificate and its key did not belong together; a new self-signed certificate was generated and the old files kept aside |
| `SQS-C084` | could not prepare the recovered control plane: ... |
| `SQS-C085` | could not set the mode of ... to ...: ... |
| `SQS-C086` | the recovery stopped and continues at the next start: ... |
| `SQS-C087` | the recovery cannot continue: ... and ... both exist; keep the one you need outside the app volume, remove the other and start again |
| `SQS-C088` | the recovery journal ... cannot be read: .... |
| `SQS-C089` | the staged master key ... is missing while its database waits |
| `SQS-C090` | a recovery of the installation is being applied (...): start the sqsolve service so it finishes, then run this again |
| `SQS-C100` | could not read the master key (did you run sqsolve init?): ... |
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
| `SQS-J010` | the process stopped while the job was running |
| `SQS-J020` | invalid schedule expression |
| `SQS-J021` | unknown schedule kind: ... |
| `SQS-J030` | this type of job cannot be retried; start the operation again from its page, which asks for confirmation |
| `SQS-J031` | the stored parameters of job ... are not valid: ... |
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
| `SQS-L050` | the database ... exists again on the server; restart sqsolve so the library becomes active, then delete it |
| `SQS-L051` | the database in the trash (...) no longer exists on the server |
| `SQS-L052` | the database ... still has sessions sqsolve cannot close (for example a superuser session); close them and try again: ... |
| `SQS-L053` | the job journal could not be written, so nothing was locked: ... |
| `SQS-L054` | not every library role of ... could be set NOLOGIN, so the connections of its database were left as they were |
| `SQS-L055` | the job journal could not be written, so the database was not moved to the trash: ... |
| `SQS-L070` | the new password of ... could not be saved; the previous password is still in force: ... |
| `SQS-L071` | the new password of ... could not be saved and the previous one could not be put back (...); rotate the password again: ... |
| `SQS-L072` | could not add ...: ... |
| `SQS-L080` | the role ... was set NOLOGIN, but its revocation could not be recorded, so the next start would give it its login back; revoke it again: ... |
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
| `SQS-N040` | could not reach the destination: ... |
| `SQS-N041` | the destination URL is not valid |
| `SQS-N042` | ... points to a private, loopback or link-local address (...): sqsolve will send events inside its own network. Check that this is the destination you want. |
| `SQS-P001` | PostgreSQL is not available |
| `SQS-P002` | preparing ...: ... |
| `SQS-P010` | literal with disallowed characters |
| `SQS-P020` | creating role ...: ... |
| `SQS-P021` | membership of ...: ... |
| `SQS-P030` | pg_reload_conf returned false |
| `SQS-P040` | administrative role in a library rule |
| `SQS-P041` | could not write pg_hba.conf: ... |
| `SQS-P042` | generated pg_hba.conf is not valid: ... |
| `SQS-P043` | could not reload PostgreSQL: ... |
| `SQS-P050` | PostgreSQL gave up waiting for a lock that another session holds (lock_timeout); try again when that session finishes |
| `SQS-P051` | PostgreSQL cancelled a statement that ran for longer than sqsolve allows (statement_timeout) |
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
| `SQS-R050` | the library is no longer waiting for this restore (state ...); nothing was changed |
| `SQS-R051` | cannot revert a library in state ... |
| `SQS-R052` | a database ... already exists on the server; it was left untouched |
| `SQS-R053` | the swap stopped halfway and the original database could not be put back under its name; it is kept as ... and Revert puts it back: ... |
| `SQS-R054` | the databases were swapped but reopening the library failed: .... The restored data is in place; reconciliation at the next start, or turning maintenance on and off, applies the access again |
| `SQS-R055` | ... has no verified backup of its own and one could not be taken now (...); it was kept |
| `SQS-R056` | ... is not a previous state of ...; it was not dropped |
| `SQS-R057` | the job journal could not be written, so nothing was locked: ... |
| `SQS-R058` | the job journal could not be written, so the database was not created: ... |
| `SQS-R090` | ... is named by an interrupted job that reconciliation at startup has not settled yet; it was kept |
| `SQS-R091` | could not tell whether an interrupted job pending reconciliation names ..., so it was kept: ... |
| `SQS-S001` | the volume has no sqsolve marker |
| `SQS-S002` | unreadable marker in ...: ... |
| `SQS-S003` | the backups volume ... belongs to another installation (...); this one is .... |
| `SQS-S004` | the backups volume ... has files but does not belong to sqsolve. |
| `SQS-S010` | the application volume ... holds the ... volume of installation ... but its instance_id is ...; |
| `SQS-S011` | the database volume ... holds the ... volume of installation ...; this one is .... |
| `SQS-S012` | the database volume ... is empty but installation ... already has its catalog: PostgreSQL would create |
| `SQS-S013` | the application volume ... is empty but the database and backups volumes belong to installation .... |
| `SQS-S014` | the backups volume ... is empty and has no marker, but installation ... already exists: |
| `SQS-S015` | cannot read the marker of the database volume ...: .... |
| `SQS-S016` | cannot tell whether this is a new installation or a recovery: the application and database volumes |
| `SQS-S017` | cannot read the marker of the application volume ...: .... |
| `SQS-S018` | cannot read the backups volume ...: .... |
| `SQS-S019` | ... holds the ... volume of installation ..., not its ... volume: the host paths of the volumes are swapped. |
| `SQS-S020` | PostgreSQL is a different cluster (system identifier ...) from the one this installation knows (...): |
| `SQS-X001` | program not allowed: ... |
| `SQS-X002` | ... not found |
| `SQS-X003` | credential with line breaks |
| `SQS-X010` | could not open ...: ... |
| `SQS-X011` | migration ... changed after being applied |
| `SQS-X012` | the database has migration ..., newer than this sqsolve version; use the version it was created with or restore the previous copy |
| `SQS-X013` | could not copy the database before migrating: ... |
| `SQS-X014` | SQLite integrity: ... |
| `SQS-X030` | could not copy the database to ...: ... |
| `SQS-X031` | could not create ...: ... |
| `SQS-X040` | the audit log could not be written, so the action was not done |
| `SQS-X041` | the action was done, but its result could not be written to the audit log: ... |
| `SQS-X900` |  |
| `SQS-X998` |  |
| `SQS-X999` | internal error: ... |

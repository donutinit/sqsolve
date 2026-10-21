// SPDX-License-Identifier: AGPL-3.0-only

package core

import "context"

// Connection limit of a library (docs 07 §8). It counts the sessions of the
// workstations only, so it is set on each library role (the owner and every
// member) and never on the database: a database limit also counts the
// connections sqsolve opens to back up (snapshot plus pg_dump), verify or
// count rows, and a busy library could no longer be backed up.

// applyConnLimit sets limit (-1 for unlimited) on every active role of a
// library and lifts any limit an older version left on its database.
func (s *Service) applyConnLimit(ctx context.Context, l *Library, limit int) error {
	roles, err := s.activeRoles(ctx, l.ID)
	if err != nil {
		return err
	}
	for _, r := range roles {
		if err := s.PG.SetRoleConnLimit(ctx, r, limit); err != nil {
			return err
		}
	}
	return s.PG.SetDatabaseConnLimit(ctx, l.DBName, -1)
}

// migrateConnLimits runs at startup: it moves to the roles the limits older
// versions set on the databases of live libraries, and puts the library's
// limit back on roles created without it (for example by a recreation in an
// older version).
func (s *Service) migrateConnLimits(ctx context.Context, note func(string, ...any)) {
	libs, err := s.Libraries(ctx, ListOptions{States: []State{StateActive, StateMaintenance}})
	if err != nil {
		return
	}
	for _, l := range libs {
		db, err := s.PG.Database(ctx, l.DBName)
		if err != nil || db == nil || db.ConnLimit == -1 && l.ConnLimit == -1 {
			continue
		}
		if err := s.applyConnLimit(ctx, l, l.ConnLimit); err != nil {
			note("%s: could not set the connection limit on its roles: %v", l.Slug, err)
			continue
		}
		if db.ConnLimit != -1 {
			note("%s: the connection limit moved from the database to its roles", l.Slug)
		}
	}
}

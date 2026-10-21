// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/health"
)

// PostgreSQL cluster identity (ADR 0023). The control plane stores the
// system identifier of the cluster it first reached. A different identifier
// means the database volume was replaced or PostgreSQL initialized a new,
// empty cluster: reconciling would mark every library missing and rewrite
// pg_hba.conf for a server that is not the installation's, so sqsolve holds
// until the change is accepted with SQSOLVE_PG_ACCEPT_SYSTEM_ID.

const pgSystemIDSetting = "pg_system_identifier"

// EventClusterChanged is emitted (critical) when the cluster changed.
const EventClusterChanged = "postgres.cluster_changed"

type identityAction int

const (
	identityOK      identityAction = iota // same cluster
	identityStore                         // first connection: remember it
	identityAccept                        // changed and accepted by the operator
	identityHold                          // changed: wait for an acceptance
	identityUnknown                       // the identifier could not be read
)

// decideIdentity compares the stored identifier with the current one.
// accept only matches the exact current cluster, so leaving the variable
// set never accepts another one.
func decideIdentity(stored, current, accept string) identityAction {
	switch {
	case current == "":
		return identityUnknown
	case stored == "":
		return identityStore
	case stored == current:
		return identityOK
	case accept == current:
		return identityAccept
	}
	return identityHold
}

// systemIdentifier reads the identifier of the cluster sqsolve is connected
// to. pg_control_system() is a catalog function (ADR 0007 is not involved).
func (a *App) systemIdentifier(ctx context.Context) (string, error) {
	p, err := a.PG.Pool()
	if err != nil {
		return "", err
	}
	var id string
	err = p.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&id)
	return id, err
}

// guardCluster checks the cluster after connecting. It returns false when
// the cluster changed and was not accepted.
func (a *App) guardCluster(ctx context.Context) bool {
	current, err := a.systemIdentifier(ctx)
	if err != nil {
		a.Log.Warn("could not read the PostgreSQL system identifier; the cluster is not checked on this start", "err", err)
	}
	return a.applyIdentity(ctx, current)
}

// applyIdentity compares current with the stored identifier and stores,
// accepts or holds. It returns false when the start must hold.
func (a *App) applyIdentity(ctx context.Context, current string) bool {
	var stored string
	if _, err := a.DB.GetSetting(ctx, pgSystemIDSetting, &stored); err != nil {
		// Not a first start: an unknown stored value is never replaced by
		// the current cluster, which could be a foreign one.
		a.Log.Warn("could not read the stored PostgreSQL system identifier; the cluster is not checked on this start", "err", err)
		return true
	}
	switch decideIdentity(stored, current, a.Cfg.PGAcceptSystemID) {
	case identityStore:
		if err := a.DB.SetSetting(ctx, pgSystemIDSetting, current, "system"); err != nil {
			a.Log.Warn("could not store the PostgreSQL system identifier", "err", err)
		}
	case identityAccept:
		if err := a.DB.SetSetting(ctx, pgSystemIDSetting, current, "system"); err != nil {
			a.Log.Error("could not store the accepted PostgreSQL system identifier", "err", err)
		}
		a.Audit.Record(ctx, audit.System, "postgres.cluster_accepted", "system", "", audit.OK, map[string]any{"from": stored, "to": current})
		a.Events.Emit(ctx, events.Event{Type: "postgres.cluster_accepted", Severity: events.Warning,
			Message: fmt.Sprintf("PostgreSQL cluster %s accepted in place of %s (SQSOLVE_PG_ACCEPT_SYSTEM_ID); you can remove the variable", current, stored)})
	case identityHold:
		msg := clusterChangedMessage(current, stored)
		a.identityProblem.Store(&msg)
		a.Events.Emit(ctx, events.Event{Type: EventClusterChanged, Severity: events.Critical, Message: msg,
			Data: map[string]any{"stored": stored, "current": current}})
		return false
	}
	return true
}

// clusterChangedMessage is the one text of SQS-S020, shared by the guard and
// doctor.
func clusterChangedMessage(current, stored string) string {
	return fmt.Sprintf("SQS-S020 PostgreSQL is a different cluster (system identifier %s) from the one this installation knows (%s): "+
		"the database volume was replaced or PostgreSQL created a new, empty cluster. sqsolve does not reconcile the libraries, "+
		"does not rewrite pg_hba.conf and runs no jobs. Mount the right database volume; if the change is intended, "+
		"set SQSOLVE_PG_ACCEPT_SYSTEM_ID=%s on the sqsolve service and restart it", current, stored, current)
}

// PGIdentityProblem describes a PostgreSQL cluster change waiting for an
// acceptance, or is empty.
func (a *App) PGIdentityProblem() string {
	if p := a.identityProblem.Load(); p != nil {
		return *p
	}
	return ""
}

func (a *App) identityNotices() []health.Attention {
	if p := a.PGIdentityProblem(); p != "" {
		return []health.Attention{{Severity: events.Critical, Message: p, Link: "/health"}}
	}
	return nil
}

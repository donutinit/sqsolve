// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

// Role of a panel account.
type Role string

// Roles.
const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// Label in Spanish.
func (r Role) Label() string {
	switch r {
	case RoleAdmin:
		return "Administrador"
	case RoleOperator:
		return "Operador"
	case RoleViewer:
		return "Lector"
	}
	return string(r)
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }

// Action is something a principal may do.
type Action string

// Actions (docs 08 §5).
const (
	ActView               Action = "view"
	ActLibraryCreate      Action = "library.create"
	ActLibraryEdit        Action = "library.edit"
	ActLibraryDelete      Action = "library.delete"
	ActLibraryMaintenance Action = "library.maintenance"
	ActLibraryReveal      Action = "library.reveal"
	ActLibraryCredentials Action = "library.credentials"
	ActSessionTerminate   Action = "library.session.terminate"
	ActBackupCreate       Action = "backup.create"
	ActBackupVerify       Action = "backup.verify"
	ActBackupDownload     Action = "backup.download"
	ActBackupImport       Action = "backup.import"
	ActBackupPin          Action = "backup.pin"
	ActRestoreNew         Action = "restore.new"
	ActRestoreReplace     Action = "restore.replace"
	ActRestoreRevert      Action = "restore.revert"
	ActPolicyEdit         Action = "policy.edit"
	ActUsersManage        Action = "users.manage"
	ActSettingsEdit       Action = "settings.edit"
	ActAuditView          Action = "audit.view"
	ActNotifyManage       Action = "notify.manage"
	ActJobCancel          Action = "job.cancel"
	ActDiagnostics        Action = "diagnostics"
	ActTokensOwn          Action = "tokens.own"
)

// AllActions lists every action (for exhaustive tests).
var AllActions = []Action{ActView, ActLibraryCreate, ActLibraryEdit, ActLibraryDelete, ActLibraryMaintenance, ActLibraryReveal,
	ActLibraryCredentials, ActSessionTerminate, ActBackupCreate, ActBackupVerify, ActBackupDownload, ActBackupImport, ActBackupPin,
	ActRestoreNew, ActRestoreReplace, ActRestoreRevert, ActPolicyEdit, ActUsersManage, ActSettingsEdit, ActAuditView, ActNotifyManage,
	ActJobCancel, ActDiagnostics, ActTokensOwn}

var viewerActions = []Action{ActView, ActTokensOwn}

var operatorActions = append(append([]Action{}, viewerActions...),
	ActBackupCreate, ActBackupVerify, ActBackupDownload, ActRestoreNew, ActAuditView, ActJobCancel)

// RoleAllows is the single source of truth for role permissions.
func RoleAllows(r Role, a Action) bool {
	switch r {
	case RoleAdmin:
		return true
	case RoleOperator:
		return contains(operatorActions, a)
	case RoleViewer:
		return contains(viewerActions, a)
	}
	return false
}

// Scopes of API tokens.
var Scopes = map[string][]Action{
	"read":    {ActView},
	"backup":  {ActView, ActBackupCreate, ActBackupVerify},
	"restore": {ActView, ActBackupDownload, ActRestoreNew, ActRestoreReplace, ActRestoreRevert},
	"admin":   AllActions,
}

// ScopeAllows reports whether any scope allows a.
func ScopeAllows(scopes []string, a Action) bool {
	for _, s := range scopes {
		if contains(Scopes[s], a) {
			return true
		}
	}
	return false
}

// DangerousActions require a recent reauthentication in browser sessions.
var DangerousActions = map[Action]bool{
	ActLibraryDelete: true, ActLibraryReveal: true, ActRestoreReplace: true, ActRestoreRevert: true,
	ActUsersManage: true, ActLibraryCredentials: true,
}

func contains(list []Action, a Action) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

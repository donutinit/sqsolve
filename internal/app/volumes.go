// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Volume identity (ADR 0023). Every volume carries a marker with the
// installation it belongs to. Docker creates a missing bind-mount path as an
// empty folder, so an unmounted disk looks like a new, empty volume: once the
// installation has an identity, init never takes an empty folder for one of
// its volumes unless an adopt variable says so.

// Messages shared by init and doctor: one text per code.
const (
	// directory, marker volume, marker installation, instance_id
	msgAppForeign = "SQS-S010 the application volume %s holds the %s volume of installation %s but its instance_id is %s; " +
		"restore the volume from a copy or ask for help before starting"
	// directory, marker volume, marker installation, this installation
	msgDBForeign = "SQS-S011 the database volume %s holds the %s volume of installation %s; this one is %s. " +
		"Mount the right volume, or set SQSOLVE_DB_ADOPT=true if you know it is the right one"
)

// volumePlan is what init decided about the three volumes before writing
// anything.
type volumePlan struct {
	ID          string
	AdoptedFrom string // "backups" or "database" when the identity came from there
	MarkApp     bool
	MarkDB      bool
	MarkBackups bool
	BackupsOK   bool     // the backups volume belongs to ID once marked
	Warnings    []string // backups problems: reported, never fatal
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// present is like exists, but something this process may not read (a
// permission error) counts as present: init never takes a volume it cannot
// inspect for an empty one.
func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// readMarker reads the marker of dir. A missing marker gives (nil, nil).
func readMarker(dir string) (*fsx.Marker, error) {
	m, err := fsx.ReadMarker(dir)
	if errors.Is(err, fsx.ErrNoMarker) {
		return nil, nil
	}
	return m, err
}

// planVolumes reads the three volumes and decides the identity. It returns
// an error for every case where PostgreSQL or sqsolve would start on a volume
// that does not belong to the installation; backups problems only warn,
// because PostgreSQL waits for init and a backups disk must not take every
// library offline.
func planVolumes(c *config.Config) (*volumePlan, error) {
	p := &volumePlan{}

	// App volume: instance_id and its marker.
	instanceID := ""
	if b, err := os.ReadFile(instanceIDPath(c)); err == nil {
		instanceID = strings.TrimSpace(string(b))
	}
	appMarker, err := readMarker(c.AppDir)
	if err != nil {
		return nil, fmt.Errorf("SQS-S017 cannot read the marker of the application volume %s: %v. "+
			"Mount the right volume or delete the marker if you know this volume is right", c.AppDir, err)
	}
	if appMarker != nil && instanceID != "" && appMarker.InstanceID != instanceID {
		return nil, fmt.Errorf(msgAppForeign, c.AppDir, appMarker.Volume, appMarker.InstanceID, instanceID)
	}
	if appMarker != nil && appMarker.Volume != fsx.VolumeApp {
		// Another volume mounted on the app path: never adoptable.
		return nil, fsx.SwappedError(c.AppDir, fsx.VolumeApp, appMarker)
	}
	p.ID = instanceID
	if p.ID == "" && appMarker != nil {
		p.ID = appMarker.InstanceID
	}
	appHasCatalog := exists(c.DBPath())
	appEmpty := p.ID == "" && !appHasCatalog
	p.MarkApp = appMarker == nil

	// Database volume.
	dbMarker, dbErr := readMarker(c.PGDir)
	if dbErr != nil {
		if !c.DBAdopt {
			return nil, fmt.Errorf("SQS-S015 cannot read the marker of the database volume %s: %v. "+
				"Mount the right volume, or set SQSOLVE_DB_ADOPT=true if you know it is the right one", c.PGDir, dbErr)
		}
		dbMarker = nil
	}
	dbCluster := present(filepath.Join(c.PGDataDir, "PG_VERSION"))
	dbEstablished := dbMarker != nil || dbCluster
	dbRight := dbMarker == nil || dbMarker.Volume == fsx.VolumeDB

	// Backups volume.
	bkMarker, bkErr := readMarker(c.BackupDir)
	bkRight := bkMarker == nil || bkMarker.Volume == fsx.VolumeBackups

	if appEmpty && dbEstablished && bkMarker != nil && bkRight && !c.AppAdopt {
		return nil, fmt.Errorf("SQS-S013 the application volume %s is empty but the database and backups volumes belong to installation %s. "+
			"Mount the right application volume; if it was lost and you want to continue with new secrets, set SQSOLVE_APP_ADOPT=true "+
			"(to recover from another server, follow docs/runbooks/recover-installation.md)", c.AppDir, bkMarker.InstanceID)
	}

	fresh := false
	if p.ID == "" {
		// The identity is only taken from a marker on its own path.
		switch {
		case bkMarker != nil && bkRight:
			p.ID, p.AdoptedFrom = bkMarker.InstanceID, "backups"
		case bkMarker != nil:
			return nil, fsx.SwappedError(c.BackupDir, fsx.VolumeBackups, bkMarker)
		case dbMarker != nil && dbRight:
			p.ID, p.AdoptedFrom = dbMarker.InstanceID, "database"
		case dbMarker != nil:
			return nil, fsx.SwappedError(c.PGDir, fsx.VolumeDB, dbMarker)
		case bkErr != nil:
			return nil, fmt.Errorf("SQS-S016 cannot tell whether this is a new installation or a recovery: the application and database volumes "+
				"have no identity and the marker of the backups volume cannot be read (%v). Fix or remove %s",
				bkErr, filepath.Join(c.BackupDir, fsx.MarkerName))
		default:
			p.ID, fresh = ulid.New(), true
		}
	}

	switch {
	case dbMarker != nil && dbMarker.InstanceID == p.ID && !dbRight:
		// Another volume of this installation on the db path: PostgreSQL
		// would create a cluster on it. Never adoptable.
		return nil, fsx.SwappedError(c.PGDir, fsx.VolumeDB, dbMarker)
	case dbMarker != nil && dbMarker.InstanceID == p.ID:
	case dbMarker != nil:
		if !c.DBAdopt {
			return nil, fmt.Errorf(msgDBForeign, c.PGDir, dbMarker.Volume, dbMarker.InstanceID, p.ID)
		}
		p.MarkDB = true
	case dbCluster:
		// Installations from before the markers: the cluster is there.
		p.MarkDB = true
	case appHasCatalog && !c.DBAdopt:
		return nil, fmt.Errorf("SQS-S012 the database volume %s is empty but installation %s already has its catalog: PostgreSQL would create "+
			"a new, empty cluster. Mount the right volume; if the cluster was lost and you will recreate the libraries from their backups, "+
			"set SQSOLVE_DB_ADOPT=true", c.PGDir, p.ID)
	default:
		p.MarkDB = true
	}

	switch {
	case bkMarker != nil && bkMarker.InstanceID == p.ID && !bkRight:
		p.Warnings = append(p.Warnings, fsx.SwappedError(c.BackupDir, fsx.VolumeBackups, bkMarker).Error()+
			". sqsolve will not write backups until it is fixed")
	case bkMarker != nil && bkMarker.InstanceID == p.ID:
		p.BackupsOK = true
	case bkMarker != nil:
		p.Warnings = append(p.Warnings, fmt.Sprintf("SQS-S003 the backups volume %s belongs to another installation (%s); this one is %s. "+
			"sqsolve will not write backups until the right volume is mounted", c.BackupDir, bkMarker.InstanceID, p.ID))
	case bkErr != nil:
		msg := bkErr.Error()
		if !strings.HasPrefix(msg, "SQS-") {
			// The same text as fsx.ReadMarker for an unparsable marker.
			msg = fmt.Errorf(fsx.MsgUnreadableMarker, c.BackupDir, bkErr).Error()
		}
		p.Warnings = append(p.Warnings, msg+". sqsolve will not write backups until it is fixed")
	default:
		empty, err := fsx.IsEmptyDir(c.BackupDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("SQS-S018 cannot read the backups volume %s: %v. "+
				"sqsolve will not write backups until it is fixed", c.BackupDir, err))
			break
		}
		missing := err != nil
		switch {
		case !empty && !missing && !c.BackupsAdopt:
			p.Warnings = append(p.Warnings, fmt.Sprintf("SQS-S004 the backups volume %s has files but does not belong to sqsolve. "+
				"Mount an empty folder, or set SQSOLVE_BACKUPS_ADOPT=true if you know it is the right one", c.BackupDir))
		case !fresh && !c.BackupsAdopt:
			p.Warnings = append(p.Warnings, fmt.Sprintf("SQS-S014 the backups volume %s is empty and has no marker, but installation %s already exists: "+
				"its disk is probably not mounted. Mount it; if it is a new, empty disk for the backups, set SQSOLVE_BACKUPS_ADOPT=true", c.BackupDir, p.ID))
		default:
			p.MarkBackups, p.BackupsOK = true, true
		}
	}
	return p, nil
}

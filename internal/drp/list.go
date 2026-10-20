// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Project is a project as listed for export.
type Project struct {
	ID   string
	Name string
	// Path is the folder path inside the library, "" at the root.
	Path     string
	Modified time.Time
	// Thumb is a JPEG thumbnail or nil.
	Thumb []byte
}

// Still is a gallery still as listed for export.
type Still struct {
	ID     string
	Label  string
	RecTC  string
	Width  int
	Height int
	// Created is the wall-clock time Resolve stored, without zone ("2006-01-02T15:04:05.000").
	Created string
	// Thumb is a JPEG thumbnail or nil.
	Thumb []byte
}

// Album is a gallery album: a PowerGrade album (ProjectID "") or the album of a project.
type Album struct {
	ViewID      string
	Name        string
	ProjectID   string
	ProjectName string
	Stills      []Still
}

// Projects lists the projects of the library with their folder paths.
func (s *Session) Projects(ctx context.Context) ([]Project, error) {
	type pf struct{ name, parent string }
	folders := map[string]pf{}
	rows, err := s.tx.Query(ctx, `SELECT "SM_ProjectFolder_id"::text, COALESCE("Name", ''), COALESCE("ParentFolder"::text, '') FROM "SM_ProjectFolder"`)
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 reading SM_ProjectFolder: %w", err)
	}
	for rows.Next() {
		var id, name, parent string
		if err := rows.Scan(&id, &name, &parent); err != nil {
			rows.Close()
			return nil, err
		}
		folders[id] = pf{name, parent}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	path := func(id string) string {
		var parts []string
		for i := 0; id != "" && i < 64; i++ {
			f, ok := folders[id]
			if !ok {
				break
			}
			parts = append([]string{f.name}, parts...)
			id = f.parent
		}
		return strings.Join(parts, "/")
	}
	rows, err = s.tx.Query(ctx, `SELECT p."SM_Project_id"::text, COALESCE(p."ProjectName", ''), COALESCE(p."Folder"::text, ''), COALESCE(p."LastModTimeInSecs", 0), t."Buffer"
		FROM "SM_Project" p
		LEFT JOIN LATERAL (SELECT b."Buffer" FROM "BtThumnail_SM_Project" a JOIN "BtThumnail" b ON b."BtThumnail_id" = a."DbAssociate"
			WHERE a."DbOwner" = p."SM_Project_id" AND a."DbPropertyName" = 'Thumbnails' ORDER BY a."DbIndex" LIMIT 1) t ON true
		ORDER BY p."ProjectName", p."SM_Project_id"`)
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 reading SM_Project: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var folder string
		var mod int64
		if err := rows.Scan(&p.ID, &p.Name, &folder, &mod, &p.Thumb); err != nil {
			return nil, err
		}
		p.Path = path(folder)
		if mod > 0 {
			p.Modified = time.Unix(mod, 0)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Albums lists every gallery album with its stills: PowerGrade albums first,
// then the albums of each project.
func (s *Session) Albums(ctx context.Context) ([]Album, error) { return s.albums(ctx, "") }

func (s *Session) albums(ctx context.Context, viewID string) ([]Album, error) {
	q := `SELECT g."Gallery::GyGallery_id"::text, COALESCE(g."SM_Project_id"::text, ''), COALESCE(p."ProjectName", ''),
		v."Gallery::GyView_id"::text, COALESCE(v."ViewName", ''),
		s."Gallery::GyStill_id"::text, COALESCE(s."Label", ''), COALESCE(s."RecTC", ''), COALESCE(s."Width", 0), COALESCE(s."Height", 0),
		COALESCE(to_char(s."CreateTime", 'YYYY-MM-DD"T"HH24:MI:SS.MS'), ''), t."Buffer"
		FROM "Gallery::GyGallery" g
		JOIN "Gallery::GyGallery_Gallery::GyView" gv ON gv."DbOwner" = g."Gallery::GyGallery_id" AND gv."DbPropertyName" = 'Views'
		JOIN "Gallery::GyView" v ON v."Gallery::GyView_id" = gv."DbAssociate"
		JOIN "Gallery::GyStillRef_Gallery::GyView" a ON a."DbOwner" = v."Gallery::GyView_id" AND a."DbPropertyName" = 'Stills'
		JOIN "Gallery::GyStillRef" r ON r."Gallery::GyStillRef_id" = a."DbAssociate"
		JOIN "Gallery::GyStill" s ON s."Gallery::GyStill_id" IN (r."Ref0", r."Ref1")
		LEFT JOIN "SM_Project" p ON p."SM_Project_id" = g."SM_Project_id"
		LEFT JOIN LATERAL (SELECT b."Buffer" FROM "BtThumnail" b WHERE b."Gallery::GyStill_id" = s."Gallery::GyStill_id" ORDER BY b."BtThumnail_id" LIMIT 1) t ON true
		WHERE ($1 = '' OR v."Gallery::GyView_id"::text = $1)
		ORDER BY (g."SM_Project_id" IS NOT NULL), p."ProjectName", g."Gallery::GyGallery_id", gv."DbIndex", a."DbIndex", s."Gallery::GyStill_id"`
	rows, err := s.tx.Query(ctx, q, viewID)
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 reading the gallery: %w", err)
	}
	defer rows.Close()
	var out []Album
	for rows.Next() {
		var galleryID string
		var a Album
		var st Still
		if err := rows.Scan(&galleryID, &a.ProjectID, &a.ProjectName, &a.ViewID, &a.Name, &st.ID, &st.Label, &st.RecTC, &st.Width, &st.Height, &st.Created, &st.Thumb); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ViewID != a.ViewID {
			out = append(out, a)
		}
		out[len(out)-1].Stills = append(out[len(out)-1].Stills, st)
	}
	return out, rows.Err()
}

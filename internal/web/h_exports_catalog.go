// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/url"
	"strings"

	"github.com/donutinit/sqsolve/internal/drp"
)

// The export catalog of a library can hold hundreds of projects, so the page
// shows one view at a time (docs 22 §7): the projects, or the PowerGrade and
// project albums with their stills. Both views take a search box, and the
// stills view can be narrowed to the albums of a single project.

// exportProject is a project row with the number of stills in its albums.
type exportProject struct {
	drp.Project
	Stills int
}

// exportCatalog is what the export catalog page renders after the view,
// search and project filters of the query string.
type exportCatalog struct {
	View    string // "projects" or "stills"
	Query   string // the search text, trimmed
	Project string // stills view narrowed to this project ID, or ""
	// ProjectName is the name of the narrowed project, for the heading.
	ProjectName string
	// Open is true when the project albums are rendered expanded: a single
	// project, or a search that leaves a handful of albums.
	Open bool

	Projects      []exportProject // the projects that match
	PowerGrades   []exportAlbum   // the PowerGrade albums that match
	ProjectAlbums []exportAlbum   // the project albums that match

	TotalProjects int // in the whole catalog, for the view switch
	TotalStills   int
}

// openAlbumsUpTo is the largest number of matching project albums that a
// search expands on its own.
const openAlbumsUpTo = 5

// buildExportCatalog applies the query string to the whole catalog. An
// unknown view falls back to the projects; a project narrows the stills view.
func buildExportCatalog(q url.Values, projects []drp.Project, power, byProject []exportAlbum) exportCatalog {
	cat := exportCatalog{View: q.Get("view"), Query: strings.TrimSpace(q.Get("q")), Project: strings.TrimSpace(q.Get("project"))}
	if cat.Project != "" {
		cat.View = "stills"
	}
	if cat.View != "stills" {
		cat.View = "projects"
	}
	needle := strings.ToLower(cat.Query)
	match := func(texts ...string) bool {
		if needle == "" {
			return true
		}
		for _, t := range texts {
			if strings.Contains(strings.ToLower(t), needle) {
				return true
			}
		}
		return false
	}

	stillsOf := map[string]int{}
	for _, a := range byProject {
		stillsOf[a.ProjectID] += len(a.Stills)
		cat.TotalStills += len(a.Stills)
	}
	for _, a := range power {
		cat.TotalStills += len(a.Stills)
	}
	cat.TotalProjects = len(projects)

	for _, p := range projects {
		if p.ID == cat.Project {
			cat.ProjectName = p.Name
		}
		if cat.View == "projects" && match(p.Name, p.Path) {
			cat.Projects = append(cat.Projects, exportProject{Project: p, Stills: stillsOf[p.ID]})
		}
	}
	if cat.View == "projects" {
		return cat
	}
	albumMatch := func(a exportAlbum) bool {
		texts := []string{a.Name, a.ProjectName}
		for _, s := range a.Stills {
			texts = append(texts, s.Label)
		}
		return match(texts...)
	}
	if cat.Project == "" {
		for _, a := range power {
			if albumMatch(a) {
				cat.PowerGrades = append(cat.PowerGrades, a)
			}
		}
	}
	for _, a := range byProject {
		if cat.Project != "" && a.ProjectID != cat.Project {
			continue
		}
		if albumMatch(a) {
			cat.ProjectAlbums = append(cat.ProjectAlbums, a)
		}
	}
	cat.Open = cat.Project != "" || (cat.Query != "" && len(cat.ProjectAlbums) <= openAlbumsUpTo)
	return cat
}

// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/url"
	"testing"

	"github.com/donutinit/sqsolve/internal/drp"
)

func TestBuildExportCatalog(t *testing.T) {
	st := func(id, label string) exportStill { return exportStill{Still: drp.Still{ID: id, Label: label}} }
	projects := []drp.Project{
		{ID: "p1", Name: "Wedding", Path: "Clients/Acme"},
		{ID: "p2", Name: "Commercial Café", Path: ""},
		{ID: "p3", Name: "Documentary", Path: "Archive"},
	}
	power := []exportAlbum{{Album: drp.Album{ViewID: "v0", Name: "Looks"}, Stills: []exportStill{st("s0", "0001")}}}
	byProject := []exportAlbum{
		{Album: drp.Album{ViewID: "v1", ProjectID: "p1", ProjectName: "Wedding"}, Stills: []exportStill{st("s1", "0001"), st("s2", "0002")}},
		{Album: drp.Album{ViewID: "v2", ProjectID: "p1", ProjectName: "Wedding", Name: "Day 2"}, Stills: []exportStill{st("s3", "0003")}},
		{Album: drp.Album{ViewID: "v3", ProjectID: "p3", ProjectName: "Documentary"}, Stills: []exportStill{st("s4", "0001")}},
	}
	build := func(raw string) exportCatalog {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		return buildExportCatalog(q, projects, power, byProject)
	}

	// Default and unknown views show the projects, with their still counts
	// and the totals of the whole catalog.
	for _, raw := range []string{"", "view=nope"} {
		cat := build(raw)
		if cat.View != "projects" || len(cat.Projects) != 3 || cat.TotalProjects != 3 || cat.TotalStills != 5 {
			t.Fatalf("%q: %+v", raw, cat)
		}
		if cat.Projects[0].Stills != 3 || cat.Projects[1].Stills != 0 || cat.Projects[2].Stills != 1 {
			t.Fatalf("still counts: %+v", cat.Projects)
		}
		if cat.PowerGrades != nil || cat.ProjectAlbums != nil {
			t.Fatal("projects view carries albums")
		}
	}

	// The search is a case-insensitive substring of the name or the folder.
	if cat := build("q=+WED+"); len(cat.Projects) != 1 || cat.Projects[0].ID != "p1" || cat.Query != "WED" {
		t.Fatalf("search by name: %+v", cat)
	}
	if cat := build("q=archive"); len(cat.Projects) != 1 || cat.Projects[0].ID != "p3" {
		t.Fatalf("search by folder: %+v", cat)
	}
	if cat := build("q=café"); len(cat.Projects) != 1 || cat.Projects[0].ID != "p2" {
		t.Fatalf("search with accents: %+v", cat)
	}
	if cat := build("q=zzz"); len(cat.Projects) != 0 || cat.TotalProjects != 3 {
		t.Fatalf("search without matches keeps the totals: %+v", cat)
	}

	// The stills view lists every album, collapsed, and carries no projects.
	cat := build("view=stills")
	if cat.View != "stills" || len(cat.PowerGrades) != 1 || len(cat.ProjectAlbums) != 3 || cat.Projects != nil || cat.Open {
		t.Fatalf("stills view: %+v", cat)
	}
	// A search matches the album name, the project name or a still label,
	// and expands the few albums it leaves.
	if cat := build("view=stills&q=day"); len(cat.ProjectAlbums) != 1 || cat.ProjectAlbums[0].ViewID != "v2" || len(cat.PowerGrades) != 0 || !cat.Open {
		t.Fatalf("search by album name: %+v", cat)
	}
	if cat := build("view=stills&q=0003"); len(cat.ProjectAlbums) != 1 || cat.ProjectAlbums[0].ViewID != "v2" {
		t.Fatalf("search by still label: %+v", cat)
	}
	if cat := build("view=stills&q=looks"); len(cat.PowerGrades) != 1 || len(cat.ProjectAlbums) != 0 {
		t.Fatalf("search by PowerGrade album: %+v", cat)
	}
	// A project narrows the stills view to its albums, expanded, whatever
	// the view parameter says, and names the project for the heading.
	cat = build("view=projects&project=p1")
	if cat.View != "stills" || cat.Project != "p1" || cat.ProjectName != "Wedding" || !cat.Open {
		t.Fatalf("narrowed to a project: %+v", cat)
	}
	if len(cat.ProjectAlbums) != 2 || len(cat.PowerGrades) != 0 || cat.Projects != nil {
		t.Fatalf("narrowed albums: %+v", cat)
	}
	if cat := build("project=p9"); len(cat.ProjectAlbums) != 0 || cat.ProjectName != "" {
		t.Fatalf("unknown project: %+v", cat)
	}
}

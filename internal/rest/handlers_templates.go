package rest

import (
	"embed"
	"net/http"
	"sort"
	"strings"

	"gh-server/internal/rest/respond"
)

//go:embed templates/licenses/*.txt
var licenseFS embed.FS

//go:embed templates/gitignore/*
var gitignoreFS embed.FS

// licenseInfo holds metadata for a license.
type licenseInfo struct {
	Key    string
	Name   string
	SPDXID string
	NodeID string
}

var licenses = []licenseInfo{
	{"mit", "MIT License", "MIT", "MDc6TGljZW5zZTEz"},
	{"apache-2.0", "Apache License 2.0", "Apache-2.0", "MDc6TGljZW5zZTI="},
	{"gpl-3.0", "GNU General Public License v3.0", "GPL-3.0", "MDc6TGljZW5zZTk="},
}

var licenseMap map[string]licenseInfo

func init() {
	licenseMap = make(map[string]licenseInfo, len(licenses))
	for _, l := range licenses {
		licenseMap[l.Key] = l
	}
}

// ListLicenses handles GET /licenses
func (d *Deps) ListLicenses(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, len(licenses))
	for i, l := range licenses {
		out[i] = map[string]any{"key": l.Key, "name": l.Name, "spdx_id": l.SPDXID, "node_id": l.NodeID}
	}
	respond.JSON(w, 200, out)
}

// GetLicense handles GET /licenses/{license}
func (d *Deps) GetLicense(w http.ResponseWriter, r *http.Request) {
	key := pathParam(r, "license")
	l, ok := licenseMap[key]
	if !ok {
		respond.NotFound(w)
		return
	}
	body, err := licenseFS.ReadFile("templates/licenses/" + key + ".txt")
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, map[string]any{
		"key": l.Key, "name": l.Name, "spdx_id": l.SPDXID, "body": string(body),
	})
}

// ListGitignoreTemplates handles GET /gitignore/templates
func (d *Deps) ListGitignoreTemplates(w http.ResponseWriter, r *http.Request) {
	entries, _ := gitignoreFS.ReadDir("templates/gitignore")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".gitignore"))
	}
	sort.Strings(names)
	respond.JSON(w, 200, names)
}

// GetGitignoreTemplate handles GET /gitignore/templates/{name}
func (d *Deps) GetGitignoreTemplate(w http.ResponseWriter, r *http.Request) {
	name := pathParam(r, "name")
	source, err := gitignoreFS.ReadFile("templates/gitignore/" + name + ".gitignore")
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, map[string]any{
		"name":   name,
		"source": string(source),
	})
}

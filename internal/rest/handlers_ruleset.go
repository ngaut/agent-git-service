package rest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
)

// ListRulesets handles GET /repos/{owner}/{repo}/rulesets
func (d *Deps) ListRulesets(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	rulesets, err := d.Svc.ListRulesets(r.Context(), repo.FullName)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	nodes := make([]map[string]any, len(rulesets))
	for i, rs := range rulesets {
		nodes[i] = transform.Ruleset(rs, repo.FullName)
	}
	respond.JSON(w, 200, nodes)
}

// CreateRuleset handles POST /repos/{owner}/{repo}/rulesets
func (d *Deps) CreateRuleset(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	var body struct {
		Name        string          `json:"name"`
		Target      string          `json:"target"`
		Enforcement string          `json:"enforcement"`
		Conditions  json.RawMessage `json:"conditions"`
		Rules       json.RawMessage `json:"rules"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	rs := db.Ruleset{
		RepositoryID:   repo.ID,
		Name:           body.Name,
		Target:         body.Target,
		Enforcement:    body.Enforcement,
		ConditionsJSON: string(body.Conditions),
		RulesJSON:      string(body.Rules),
	}
	err := d.Svc.CreateRuleset(r.Context(), repo.FullName, &rs)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.Ruleset(rs, repo.FullName))
}

// GetRuleset handles GET /repos/{owner}/{repo}/rulesets/{ruleset_id}
func (d *Deps) GetRuleset(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	id, ok := mustUintParam(w, r, "ruleset_id")
	if !ok {
		return
	}
	rs, err := d.Svc.GetRuleset(r.Context(), id)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Ruleset(rs, repo.FullName))
}

// GetOrgRuleset handles GET /orgs/{org}/rulesets/{ruleset_id}
func (d *Deps) GetOrgRuleset(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	id, ok := mustUintParam(w, r, "ruleset_id")
	if !ok {
		return
	}
	rs, err := d.Svc.GetRuleset(r.Context(), id)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Ruleset(rs, org.Login))
}

// CheckBranchRules handles GET /repos/{owner}/{repo}/rules/branches/{branch}
func (d *Deps) CheckBranchRules(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	branch := chi.URLParam(r, "*")
	rulesets, err := d.Svc.ListBranchRulesets(r.Context(), repo.FullName)
	if err != nil {
		rulesets = []db.Ruleset{}
	}

	var matchingRules []map[string]any
	for _, rs := range rulesets {
		if rs.Enforcement != "active" {
			continue
		}
		// Check if the branch matches the conditions
		var conditions struct {
			RefName struct {
				Include []string `json:"include"`
				Exclude []string `json:"exclude"`
			} `json:"ref_name"`
		}
		if err := json.Unmarshal([]byte(rs.ConditionsJSON), &conditions); err != nil {
			slog.Warn("ruleset conditions unmarshal", "rulesetID", rs.ID, "error", err)
		}

		matches := false
		for _, inc := range conditions.RefName.Include {
			if inc == "~DEFAULT_BRANCH" && branch == repo.DefaultBranch {
				matches = true
			} else if inc == branch || inc == gitstore.RefsHeadsPrefix+branch {
				matches = true
			}
		}
		if !matches {
			continue
		}

		var rawRules []json.RawMessage
		if rs.RulesJSON != "" {
			if err := json.Unmarshal([]byte(rs.RulesJSON), &rawRules); err != nil {
				slog.Warn("ruleset rules unmarshal", "rulesetID", rs.ID, "error", err)
				continue
			}
		}
		for _, raw := range rawRules {
			ruleObj, err := decodeRuleObject(raw)
			if err != nil {
				slog.Warn("ruleset rule decode", "rulesetID", rs.ID, "error", err)
				continue
			}
			ruleObj["ruleset_source_type"] = "Repository"
			ruleObj["ruleset_source"] = repo.FullName
			ruleObj["ruleset_id"] = rs.ID
			matchingRules = append(matchingRules, ruleObj)
		}
	}

	if matchingRules == nil {
		matchingRules = []map[string]any{}
	}
	respond.JSON(w, 200, matchingRules)
}

// decodeRuleObject extracts a rule object from a raw JSON entry, tolerating
// both shapes produced by current and legacy writers: a bare JSON object, or a
// JSON string that itself encodes an object.
func decodeRuleObject(raw json.RawMessage) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj == nil {
			return nil, fmt.Errorf("rule is null")
		}
		return obj, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("rule is neither object nor string: %w", err)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(s), &inner); err != nil {
		return nil, fmt.Errorf("rule string does not decode to object: %w", err)
	}
	if inner == nil {
		// Guard against the caller writing into a nil map on stringified `"null"`.
		return nil, fmt.Errorf("rule string is null")
	}
	return inner, nil
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The board is the "On-prem tokens" GitHub project. Trackers join it through
// the project's own auto-add rule; this file keeps three of its fields in step
// with the tracker: Issued and Expires (dates) and Status, which is Active for a
// token with more than a month left and "Expires soon" once it is inside soonWindow. The
// project's built-in rule flips Status to Expired when the tracker is closed, so
// the tool never writes that value.
//
// Writing Projects v2 fields needs organization_projects permission, which the
// default workflow token does not have, so the board client runs on a separate
// token (PROJECT_TOKEN, minted by octo-sts) and is simply disabled when that is
// not configured.

const (
	soonWindow   = 30 * 24 * time.Hour
	boardActive  = "Active"
	boardSoon    = "Expires soon"
	fieldIssued  = "Issued"
	fieldExpires = "Expires"
	fieldStatus  = "Status"
)

// boardStatus is the Status a live token should carry on the board; "" means
// the token has expired and the board's own close rule owns the value.
func boardStatus(t tracker, now time.Time) string {
	exp := time.Unix(t.Exp, 0)
	switch {
	case !exp.After(now):
		return ""
	case exp.Sub(now) <= soonWindow:
		return boardSoon
	default:
		return boardActive
	}
}

type boardClient struct {
	api, token, owner string
	number            int
	http              *http.Client

	projectID  string
	fields     map[string]string // field name -> id
	options    map[string]string // Status option name -> id
	resolveErr error             // a failed lookup is final for the run; do not repeat it per tracker
}

func (b *boardClient) enabled() bool { return b != nil && b.token != "" && b.number > 0 }

type gqlError struct {
	Message string `json:"message"`
}

func (b *boardClient) gql(query string, vars map[string]any, out any) error {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, b.api, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("board: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("board: GraphQL HTTP %d: %.300s", res.StatusCode, raw)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("board: bad GraphQL response: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("board: GraphQL error: %s", env.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// resolve looks the project and its field ids up by name once per run, so the
// workflow only has to know the project number and nothing breaks when fields
// are recreated. Renaming an option the tool needs is a loud failure.
func (b *boardClient) resolve() (err error) {
	if b.projectID != "" {
		return nil
	}
	if b.resolveErr != nil {
		return b.resolveErr
	}
	defer func() { b.resolveErr = err }()
	var data struct {
		Organization struct {
			ProjectV2 *struct {
				ID     string `json:"id"`
				Fields struct {
					Nodes []struct {
						ID      string `json:"id"`
						Name    string `json:"name"`
						Options []struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"options"`
					} `json:"nodes"`
				} `json:"fields"`
			} `json:"projectV2"`
		} `json:"organization"`
	}
	const q = `query($owner:String!,$number:Int!){ organization(login:$owner){ projectV2(number:$number){ id fields(first:50){ nodes{ ... on ProjectV2Field{ id name } ... on ProjectV2SingleSelectField{ id name options{ id name } } } } } } }`
	if err := b.gql(q, map[string]any{"owner": b.owner, "number": b.number}, &data); err != nil {
		return err
	}
	p := data.Organization.ProjectV2
	if p == nil {
		return fmt.Errorf("board: project #%d not found in %s (does the token have organization_projects access?)", b.number, b.owner)
	}
	// Validate into locals first: projectID doubles as the "resolved" flag, so
	// it must only be set once everything the tool needs is known to exist.
	fields, options := map[string]string{}, map[string]string{}
	for _, f := range p.Fields.Nodes {
		fields[f.Name] = f.ID
		if f.Name == fieldStatus {
			for _, o := range f.Options {
				options[o.Name] = o.ID
			}
		}
	}
	var missing []string
	for _, name := range []string{fieldIssued, fieldExpires, fieldStatus} {
		if fields[name] == "" {
			missing = append(missing, "field "+name)
		}
	}
	for _, name := range []string{boardActive, boardSoon} {
		if options[name] == "" {
			missing = append(missing, "Status option "+name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("board: project #%d is missing %v", b.number, missing)
	}
	b.projectID, b.fields, b.options = p.ID, fields, options
	return nil
}

// sync puts the tracker on the board (idempotent: an item that is already there
// is returned, not duplicated), writes both dates and, when status is non-empty,
// the Status option.
func (b *boardClient) sync(issueNodeID string, t tracker, status string) error {
	if err := b.resolve(); err != nil {
		return err
	}
	var added struct {
		AddProjectV2ItemByID struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	const addQ = `mutation($project:ID!,$content:ID!){ addProjectV2ItemById(input:{projectId:$project,contentId:$content}){ item{ id } } }`
	if err := b.gql(addQ, map[string]any{"project": b.projectID, "content": issueNodeID}, &added); err != nil {
		return err
	}
	item := added.AddProjectV2ItemByID.Item.ID
	if item == "" {
		return errors.New("board: addProjectV2ItemById returned no item id")
	}
	const dateQ = `mutation($project:ID!,$item:ID!,$field:ID!,$date:Date!){ updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{date:$date}}){ projectV2Item{ id } } }`
	for name, unix := range map[string]int64{fieldIssued: t.IssuedAt, fieldExpires: t.Exp} {
		if unix <= 0 {
			continue
		}
		vars := map[string]any{"project": b.projectID, "item": item, "field": b.fields[name], "date": time.Unix(unix, 0).UTC().Format("2006-01-02")}
		if err := b.gql(dateQ, vars, nil); err != nil {
			return err
		}
	}
	if status != "" {
		const optQ = `mutation($project:ID!,$item:ID!,$field:ID!,$option:String!){ updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}){ projectV2Item{ id } } }`
		vars := map[string]any{"project": b.projectID, "item": item, "field": b.fields[fieldStatus], "option": b.options[status]}
		if err := b.gql(optQ, vars, nil); err != nil {
			return err
		}
	}
	return nil
}

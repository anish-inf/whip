package inferencenet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// The relay's tRPC uses the superjson transport. For the request/response
// shapes whip touches (plain JSON, no Date/Map/Set round-trips), superjson is
// identical to plain JSON plus a {"json": …} envelope, which is what the
// helpers below add and strip. That keeps the client stdlib-only.

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// envelope is a tRPC/superjson response: a result on success, an error on
// failure. The error message may be plain or superjson-masked ({"json":…}).
type envelope struct {
	Result struct {
		Data json.RawMessage `json:"data"`
	} `json:"result"`
	Error *struct {
		Message json.RawMessage `json:"message"`
	} `json:"error"`
}

func (e envelope) err(path string) error {
	if e.Error == nil {
		return nil
	}
	msg := string(e.Error.Message)
	var s string
	if json.Unmarshal(e.Error.Message, &s) == nil {
		msg = s // plain string
	} else {
		var wrap struct {
			JSON string `json:"json"`
		}
		if json.Unmarshal(e.Error.Message, &wrap) == nil && wrap.JSON != "" {
			msg = wrap.JSON // superjson-masked
		}
	}
	return fmt.Errorf("%s: %s", path, msg)
}

// trpcQuery runs a tRPC query (GET, input in the query string).
func trpcQuery(ctx context.Context, token, teamID, path string, input, out any) error {
	q := url.Values{}
	if input == nil {
		// A z.void() input arrives as undefined; encode it as superjson does.
		q.Set("input", `{"json":null,"meta":{"values":["undefined"]}}`)
	} else {
		b, err := json.Marshal(map[string]any{"json": input})
		if err != nil {
			return err
		}
		q.Set("input", string(b))
	}
	u := relayURL + trpcEndpoint + "/" + path + "?" + q.Encode()
	var env envelope
	if err := doJSON(ctx, http.MethodGet, u, nil, trpcHeaders(token, teamID), &env); err != nil {
		return err
	}
	if err := env.err(path); err != nil {
		return err
	}
	return unmarshalData(env.Result.Data, out)
}

// trpcMutate runs a tRPC mutation (POST, body in the request).
func trpcMutate(ctx context.Context, token, teamID, path string, input, out any) error {
	var env envelope
	if err := doJSON(ctx, http.MethodPost, relayURL+trpcEndpoint+"/"+path,
		map[string]any{"json": input}, trpcHeaders(token, teamID), &env); err != nil {
		return err
	}
	if err := env.err(path); err != nil {
		return err
	}
	return unmarshalData(env.Result.Data, out)
}

// unmarshalData unwraps the superjson {"json": value} data envelope into out.
func unmarshalData(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return errors.New("empty result")
	}
	var wrap struct {
		JSON json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return err
	}
	if len(wrap.JSON) == 0 || out == nil {
		return nil
	}
	return json.Unmarshal(wrap.JSON, out)
}

func trpcHeaders(token, teamID string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + token}
	if teamID != "" {
		h[teamIDHeader] = teamID
	}
	return h
}

// getProjects lists the active team's projects.
func getProjects(ctx context.Context, token, teamID string) ([]Project, error) {
	var projects []Project
	err := trpcQuery(ctx, token, teamID, "project.getProjects", nil, &projects)
	return projects, err
}

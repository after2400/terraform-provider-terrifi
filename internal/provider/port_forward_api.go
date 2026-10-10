package provider

// The go-unifi SDK ships create/read/update/delete code for the classic v1
// `rest/portforward` endpoint, but all of it is unexported, so the provider
// cannot call it. These wrappers reuse the SDK's exported unifi.PortForward
// type and send the requests through doV2Request, which only adds auth and
// accepts any 2xx status.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubiquiti-community/go-unifi/unifi"
)

// portForwardEnvelope is the v1 REST response wrapper: {"meta":{"rc":"ok"},"data":[...]}.
// The controller reports failures as HTTP 200 with meta.rc == "error".
type portForwardEnvelope struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
	Data []unifi.PortForward `json:"data"`
}

func (c *Client) portForwardURL(site, id string) string {
	u := fmt.Sprintf("%s%s/api/s/%s/rest/portforward", c.BaseURL, c.APIPath, site)
	if id != "" {
		u += "/" + id
	}
	return u
}

func (c *Client) doPortForward(ctx context.Context, method, url string, body any) ([]unifi.PortForward, error) {
	var env portForwardEnvelope
	if err := c.doV2Request(ctx, method, url, body, &env); err != nil {
		return nil, err
	}
	if env.Meta.RC != "" && env.Meta.RC != "ok" {
		return nil, fmt.Errorf("controller rejected %s %s: %s", method, url, env.Meta.Msg)
	}
	return env.Data, nil
}

// ListPortForwards returns every port forward in the site.
func (c *Client) ListPortForwards(ctx context.Context, site string) ([]unifi.PortForward, error) {
	return c.doPortForward(ctx, http.MethodGet, c.portForwardURL(site, ""), struct{}{})
}

// GetPortForward returns one port forward, or *unifi.NotFoundError. It lists
// and matches by ID because a missing object does not return a reliable 404.
func (c *Client) GetPortForward(ctx context.Context, site, id string) (*unifi.PortForward, error) {
	all, err := c.ListPortForwards(ctx, site)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, &unifi.NotFoundError{}
}

func (c *Client) CreatePortForward(ctx context.Context, site string, d *unifi.PortForward) (*unifi.PortForward, error) {
	out, err := c.doPortForward(ctx, http.MethodPost, c.portForwardURL(site, ""), d)
	if err != nil {
		return nil, err
	}
	if len(out) != 1 {
		return nil, fmt.Errorf("creating port forward: expected one object back, got %d", len(out))
	}
	return &out[0], nil
}

func (c *Client) UpdatePortForward(ctx context.Context, site string, d *unifi.PortForward) (*unifi.PortForward, error) {
	out, err := c.doPortForward(ctx, http.MethodPut, c.portForwardURL(site, d.ID), d)
	if err != nil {
		return nil, err
	}
	if len(out) != 1 {
		return nil, fmt.Errorf("updating port forward: expected one object back, got %d", len(out))
	}
	return &out[0], nil
}

func (c *Client) DeletePortForward(ctx context.Context, site, id string) error {
	_, err := c.doPortForward(ctx, http.MethodDelete, c.portForwardURL(site, id), struct{}{})
	return err
}

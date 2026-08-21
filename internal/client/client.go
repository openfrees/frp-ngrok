// Package client 是本地控制台接口的调用方，供菜单栏等本机组件使用。
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
)

// Client 通过 127.0.0.1 访问本机控制台服务。
type Client struct {
	port  int
	token string
	http  *http.Client
}

// New 创建客户端。
func New(port int, token string) *Client {
	return &Client{
		port:  port,
		token: token,
		http:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Port 返回控制台端口。
func (c *Client) Port() int { return c.port }

// SetPort 在后台换了监听端口后更新客户端，避免菜单栏一直打旧端口。
func (c *Client) SetPort(port int) { c.port = port }

// ConsoleURL 返回带访问令牌的控制台地址。
func (c *Client) ConsoleURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", c.port, c.token)
}

// State 拉取当前状态快照。
func (c *Client) State() (apitypes.State, error) {
	var out apitypes.State
	err := c.do(http.MethodGet, "/api/state", nil, &out)
	return out, err
}

// ClientAction 控制隧道客户端的启停，action 取 start / stop / restart。
func (c *Client) ClientAction(action string) (apitypes.ActionResult, error) {
	var out apitypes.ActionResult
	err := c.do(http.MethodPost, "/api/client/"+action, struct{}{}, &out)
	return out, err
}

func (c *Client) do(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", c.port, path), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("接口返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PutJSON 发送 JSON 写请求。
func (c *Client) PutJSON(path string, body, out any) error {
	return c.do(http.MethodPut, path, body, out)
}

// GetJSON 读取 JSON。
func (c *Client) GetJSON(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

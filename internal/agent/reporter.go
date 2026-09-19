package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

type Reporter struct {
	HubURL string
	Token  string
	Client *http.Client
}

func NewReporter(hubURL, token string) *Reporter {
	return &Reporter{
		HubURL: hubURL,
		Token:  token,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *Reporter) Send(hb *models.Heartbeat) error {
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	url := r.HubURL + "/api/agent/heartbeat"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", r.Token)
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("hub status %d", resp.StatusCode)
	}
	return nil
}

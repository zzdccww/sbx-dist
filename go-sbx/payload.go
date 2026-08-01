package main

import (
	"encoding/json"
	"errors"
)

type Payload struct {
	Config       string        `json:"config"`
	WorkingDir   string        `json:"workingDir"`
	DisableColor bool          `json:"disableColor"`
	Tunnel       *TunnelConfig `json:"tunnel,omitempty"`
}

type TunnelConfig struct {
	Token       string `json:"token"`
	Hostname    string `json:"hostname"`
	BackendPort int    `json:"backendPort"`
}

func parsePayload(jsonStr string) (*Payload, error) {
	var p Payload
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return nil, err
	}
	if p.Config == "" {
		return nil, errors.New("missing required field: config")
	}
	if p.WorkingDir == "" {
		return nil, errors.New("missing required field: workingDir")
	}
	return &p, nil
}

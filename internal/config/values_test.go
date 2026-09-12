package config

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestCommandAcceptsListsAndPlainStrings(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    []string
		wantErr string
	}{
		{name: "list", yaml: "c: [go, test, -race, ./...]", want: []string{"go", "test", "-race", "./..."}},
		{name: "string split on whitespace", yaml: "c: go vet ./...", want: []string{"go", "vet", "./..."}},
		{name: "string with shell syntax", yaml: `c: "go test ./... | tee log"`, wantErr: "contains shell syntax"},
		{name: "string with a variable", yaml: `c: "echo $HOME"`, wantErr: "contains shell syntax"},
		{name: "empty string", yaml: `c: ""`, wantErr: "can't be empty"},
		{name: "empty list", yaml: "c: []", wantErr: "can't be empty"},
		{name: "mapping", yaml: "c: {run: go}", wantErr: "string or a list"},
		{name: "list of lists", yaml: "c: [[go, test]]", wantErr: "only strings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v struct {
				C Command `yaml:"c"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &v)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if strings.Join(v.C, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("command = %q, want %q", v.C, tt.want)
			}
		})
	}
}

func TestDurationParsesGoDurationStrings(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    time.Duration
		wantErr string
	}{
		{name: "minutes", yaml: "d: 30m", want: 30 * time.Minute},
		{name: "hours and minutes", yaml: "d: 1h30m", want: 90 * time.Minute},
		{name: "prose", yaml: "d: 30 minutes", wantErr: `invalid duration "30 minutes"`},
		{name: "list", yaml: "d: [30m]", wantErr: "must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v struct {
				D Duration `yaml:"d"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &v)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if time.Duration(v.D) != tt.want {
				t.Errorf("duration = %v, want %v", time.Duration(v.D), tt.want)
			}
		})
	}
}

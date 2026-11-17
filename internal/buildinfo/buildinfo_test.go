package buildinfo

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCollector(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })

	for _, v := range []string{"dev", "0123abc"} {
		Version = v
		reg := prometheus.NewRegistry()
		reg.MustRegister(Collector())
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		if len(mfs) != 1 || mfs[0].GetName() != "build_info" || len(mfs[0].GetMetric()) != 1 {
			t.Fatalf("version %q: gathered %v, want one build_info", v, mfs)
		}
		m := mfs[0].GetMetric()[0]
		labels := m.GetLabel()
		if m.GetGauge().GetValue() != 1 || len(labels) != 1 || labels[0].GetName() != "version" || labels[0].GetValue() != v {
			t.Errorf("version %q: build_info = %v", v, m)
		}
	}
}

func TestLogStart(t *testing.T) {
	var buf bytes.Buffer
	LogStart(slog.New(slog.NewJSONHandler(&buf, nil)), "api")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q: %v", buf.String(), err)
	}
	if line["msg"] != "starting" || line["binary"] != "api" || line["version"] != Version || line["go_version"] == "" {
		t.Errorf("start line = %v", line)
	}
}

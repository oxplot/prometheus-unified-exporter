package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/common/expfmt"
	"gopkg.in/yaml.v3"
)

// Target is a Prometheus exporter target.
type Target struct {
	URL    string            `yaml:"url"`
	Labels map[string]string `yaml:"labels"`

	// labelsSerialized is the serialized form of Labels, used for directly
	// injecting into upstream responses.
	labelsSerialized string
}

// Config is the configuration for the exporter.
type Config struct {
	Listen  string   `yaml:"listen"`
	Targets []Target `yaml:"targets"`
}

var cfg *Config

// loadConfig loads the configuration from the given path.
func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:9001"
	}
	// Serialize labels into k="v" pairs separated by ,.
	for i, t := range cfg.Targets {
		var l []string
		for k, v := range t.Labels {
			l = append(l, fmt.Sprintf(`%s="%s"`, k, v))
		}
		cfg.Targets[i].labelsSerialized = strings.Join(l, ",")
	}

	return &cfg, nil
}

func fetchMetrics(url string) ([]*dto.MetricFamily, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parser expfmt.TextParser
	metrics, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	metricFamilies := make([]*dto.MetricFamily, 0, len(metrics))
	for _, mf := range metrics {
		metricFamilies = append(metricFamilies, mf)
	}

	return metricFamilies, nil
}

func addLabels(metrics []*dto.MetricFamily, labels map[string]string) {
	for _, mf := range metrics {
		for _, m := range mf.Metric {
			for labelName, labelValue := range labels {
				m.Label = append(m.Label, &dto.LabelPair{
					Name:  &labelName,
					Value: &labelValue,
				})
			}
		}
	}
}

func serializeMetrics(w io.Writer, metrics []*dto.MetricFamily) error {
	encoder := expfmt.NewEncoder(w, expfmt.FmtText)
	for _, mf := range metrics {
		err := encoder.Encode(mf)
		if err != nil {
			return err
		}
	}
	return nil
}

// handleMetrics handles the /metrics endpoint by collating metrics from all
// targets and writing them to the response.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Fan out requests to all targets.
	ch := make(chan []*dto.MetricFamily, len(cfg.Targets))
	for _, t := range cfg.Targets {
		go func(t Target) {
			metrics, err := fetchMetrics(t.URL)
			defer func() {
				ch <- metrics
			}()
			if err != nil {
				log.Printf("failed to fetch metrics from %s: %v", t.URL, err)
				return
			}
			addLabels(metrics, t.Labels)
		}(t)
	}
	allMetrics := make([]*dto.MetricFamily, 0)
	for range cfg.Targets {
		allMetrics = append(allMetrics, <-ch...)
	}
	if err := serializeMetrics(w, allMetrics); err != nil {
		log.Printf("failed to serialize metrics: %v", err)
	}
}

func main() {
	var err error
	log.SetFlags(0)
	log.SetPrefix("prometheus-unified-exporter: ")
	configPath := os.Getenv("PUE_CONFIG")
	if configPath == "" {
		log.Fatal("PUE_CONFIG env var must be set to the path of the config file")
	}
	cfg, err = loadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	http.HandleFunc("/metrics", handleMetrics)
	log.Printf("listening on http://%s/metrics", cfg.Listen)
	log.Fatal(http.ListenAndServe(cfg.Listen, nil))
}

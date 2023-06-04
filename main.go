package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
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

func fetchMetrics(url string) (map[string]*dto.MetricFamily, error) {
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
	return parser.TextToMetricFamilies(strings.NewReader(string(body)))
}

func addLabels(metrics map[string]*dto.MetricFamily, labels map[string]string) {
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

func serializeMetrics(w io.Writer, metricFamilies map[string]*dto.MetricFamily) error {
	lst := make([]*dto.MetricFamily, 0, len(metricFamilies))
	for _, mf := range metricFamilies {
		lst = append(lst, mf)
	}
	sort.Slice(lst, func(i, j int) bool {
		return *lst[i].Name < *lst[j].Name
	})
	encoder := expfmt.NewEncoder(w, expfmt.FmtText)
	for _, mf := range lst {
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
	ch := make(chan map[string]*dto.MetricFamily, len(cfg.Targets))
	for _, t := range cfg.Targets {
		go func(t Target) {
			metricFamilies, err := fetchMetrics(t.URL)
			defer func() {
				ch <- metricFamilies
			}()
			if err != nil {
				log.Printf("failed to fetch metrics from %s: %v", t.URL, err)
			}
			addLabels(metricFamilies, t.Labels)
		}(t)
	}
	allMetricsFamilies := map[string]*dto.MetricFamily{}
	for range cfg.Targets {
		for n, mf := range <-ch {
			if amf, ok := allMetricsFamilies[n]; ok {
				amf.Metric = append(amf.Metric, mf.Metric...)
			} else {
				allMetricsFamilies[*mf.Name] = mf
			}
		}
	}
	if err := serializeMetrics(w, allMetricsFamilies); err != nil {
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

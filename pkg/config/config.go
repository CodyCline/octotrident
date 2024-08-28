package config

import (
	"errors"
	"os"
	"path"
	"runtime"

	flag "github.com/lynxsecurity/pflag"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type CLIConfig struct {
	Repository   string
	Organization string
	Path         string
	ConfigFile   ConfigFile
	Threads      int
	ShortShaSize int
	MaxForks     int
	Retries      int
	Timeout      int
	Proxy        string
	BatchSize    int
	Silent       bool
	Verbose      bool
	KeepOutput   bool
	Version      bool
}

var template = &map[string]interface{}{
	"github": map[string]interface{}{
		"apikeys": []string{},
	},
}

func NewCLIConfig() (*CLIConfig, error) {
	path := flag.String("path", ".", "path to clone the repository or open existing one")
	repository := flag.String("repository", "", "repository URL to run octotrident on")
	configFile := flag.String("config", "", "alternative path to your config.yml file")
	keepOutput := flag.Bool("keep-output", false, "output valid hidden commits to a file")
	threads := flag.Int("threads", 10, "sets the desired concurrency ")
	retries := flag.Int("retries", 3, "number of retries for failed API requests")
	timeout := flag.Int("timeout", 60, "timeout for http connections")
	silent := flag.Bool("silent", false, "suppress all output")
	batchSize := flag.Int("batch-size", 300, "size of each github batch API request")
	verbose := flag.Bool("verbose", false, "show debug/verbose output")
	shortShaSize := flag.Int("sha-size", 4, "length of short sha hashes to bruteforce (default 4)")
	proxy := flag.String("proxy", "", "url of a proxy to use")
	version := flag.Bool("version", false, "output octotrident version")
	flag.Parse()

	sourcesYml, err := locateConfigYml(*configFile)
	if err != nil {
		return nil, err
	}

	file, err := os.ReadFile(sourcesYml)
	if err != nil {
		return nil, err
	}

	var cfg ConfigFile

	if err := yaml.Unmarshal(file, &cfg); err != nil {
		return nil, err
	}

	return &CLIConfig{
		ConfigFile:   cfg,
		Threads:      *threads,
		Path:         *path,
		Repository:   *repository,
		ShortShaSize: *shortShaSize,
		Retries:      *retries,
		Timeout:      *timeout,
		Silent:       *silent,
		Verbose:      *verbose,
		Proxy:        *proxy,
		BatchSize:    *batchSize,
		KeepOutput:   *keepOutput,
		Version:      *version,
	}, nil
}

func locateConfigYml(location string) (string, error) {
	if _, err := os.Stat(location); !os.IsNotExist(err) {
		return location, nil
	}
	var sourcesDir string

	switch runtime.GOOS {
	case "darwin":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		sourcesDir = path.Join(homeDir, "Library", "Preferences", "octotrident")
	case "linux":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		sourcesDir = path.Join(homeDir, ".config", "octotrident")
	case "windows":
		appDataDir := os.Getenv("AppData")
		if appDataDir == "" {
			return "", errors.New("AppData environment variable not set")
		}
		sourcesDir = path.Join(appDataDir, "Roaming", "octotrident", "Config")
	default:
		return "", errors.New("unsupported OS")
	}
	sourcesFile := path.Join(sourcesDir, "config.yml")

	if _, err := os.Stat(sourcesDir); os.IsNotExist(err) {
		if err := os.MkdirAll(sourcesDir, 0755); err != nil {
			return "", err
		}
		data, err := yaml.Marshal(&template)
		if err != nil {
			return "", err
		}

		if err := os.WriteFile(sourcesFile, data, 0644); err != nil {
			return "", err
		}
		log.Infof("created new config.yml file at %s", sourcesFile)
	}
	return sourcesFile, nil
}

type Source struct {
	APIKeys []string `yaml:"apikeys"`
}

type ConfigFile struct {
	GitHub Source `yaml:"github"`
}

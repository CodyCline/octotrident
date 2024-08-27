package config

type Config struct {
	Providers []string
	Threads   int
	Retries   int
	Timeout   int
	Proxy     string
	BatchSize int
}

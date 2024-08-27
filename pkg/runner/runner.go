package runner

import (
	"net/http"
	"net/url"
	"time"

	githubRL "github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v63/github"
	"golang.org/x/exp/rand"
)

type Runner struct {
	Concurrency     int
	Timeout         int
	MaxRetries      int
	Proxy           url.URL
	Clients         []*github.Client
	OnHiddenCommit  func(commit string)
	OnInvalidCommit func(commit string)
}

func New(apiKeys []string) (*Runner, error) {
	var clients []*github.Client
	for _, apiKey := range apiKeys {
		DefaultTransport := &http.Transport{
			// Proxy:                 http.ProxyURL(url),
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		rateLimiter, err := githubRL.NewRateLimitWaiterClient(DefaultTransport, nil)
		if err != nil {
			return nil, err
		}
		clients = append(clients, github.NewClient(rateLimiter).WithAuthToken(apiKey))
	}
	return &Runner{
		Clients: clients,
	}, nil
}

func (r *Runner) Prepare() {
	//Batch all the queries start the client, emit callbacks etc.
}

func (r *Runner) Brute() {
	//Batch all the queries start the client, emit callbacks etc.
}

func (r *Runner) Finish() {
	//Batch all the queries start the client, emit callbacks etc.
}

func (r *Runner) RandomClient() *github.Client {
	return r.Clients[rand.Intn(len(r.Clients))]
}

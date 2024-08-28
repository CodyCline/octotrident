package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	githubBrute "github.com/CodyCline/octotrident/pkg/github"
	results "github.com/CodyCline/octotrident/pkg/results"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githubRL "github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v63/github"
	giturl "github.com/kubescape/go-git-url"
	"github.com/peterhellberg/link"
	"golang.org/x/exp/rand"
)

type Runner struct {
	Threads         int
	Timeout         int
	Retries         int
	MaxForks        int
	Path            string
	Proxy           url.URL
	BatchSize       int
	ShortShaSize    int
	Clients         []*github.Client
	OnHiddenCommit  func(commit plumbing.Hash)
	OnInvalidCommit func(commit plumbing.Hash)
}

// basically a constructor type gadget so this can work as a standalone library
type RunnerOpts struct {
	ApiKeys      []string
	Url          string
	Config       string
	Threads      int
	Timeout      int
	Retries      int
	Path         string
	MaxForks     int
	Proxy        url.URL
	BatchSize    int
	ShortShaSize int
}

func NewRunner(options RunnerOpts) (*Runner, error) {
	var clients []*github.Client
	for _, apiKey := range options.ApiKeys {
		DefaultTransport := &http.Transport{
			// Proxy:                 http.ProxyURL(url),
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
		}

		rateLimiter, err := githubRL.NewRateLimitWaiterClient(DefaultTransport, nil)
		if err != nil {
			return nil, err
		}
		clients = append(clients, github.NewClient(rateLimiter).WithAuthToken(apiKey))
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("provided %d valid github api keys", len(clients))
	}
	fmt.Println(options.BatchSize)
	return &Runner{
		Clients:      clients,
		Threads:      options.Threads,
		Timeout:      options.Timeout,
		Retries:      options.Retries,
		MaxForks:     options.MaxForks,
		BatchSize:    options.BatchSize,
		ShortShaSize: options.ShortShaSize,
	}, nil
}

// First we need to do the things that must work
func (r *Runner) Init(repositoryURL string, location string) (*results.Results, error) {

	gitURL, err := giturl.NewGitURL(repositoryURL) // initialize and parse the URL
	if err != nil {
		return nil, err
	}

	owner, repositoryName := gitURL.GetOwnerName(), gitURL.GetRepoName()

	var repository *git.Repository

	if location == "." {
		location = path.Join(location, repositoryName)
	}

	//Not pretty, clone or open the repo
	if repo, err := git.PlainClone(location, false, &git.CloneOptions{
		URL:      gitURL.GetURL().String(),
		Progress: os.Stdout,
	}); err != nil {
		if err == git.ErrRepositoryAlreadyExists {
			localRepo, err := git.PlainOpen(location)
			if err != nil {
				return nil, err
			}
			repository = localRepo
		} else {
			return nil, err
		}
	} else {
		repository = repo
	}

	workingTree, err := repository.Worktree()
	if err != nil {
		fmt.Println(err)
	}

	remote, err := repository.Remote("origin")
	if err != nil {
		return nil, err
	}

	// Create a temporary directory
	results := &results.Results{
		Mu:                &sync.RWMutex{},
		Owner:             owner,
		Name:              repositoryName,
		Repository:        repository,
		Remote:            remote,
		WorkingTree:       workingTree,
		ForkURLs:          []string{},
		KnownShortCommits: make(map[string]plumbing.Hash),
		KnownCommits:      make(map[string]plumbing.Hash),
		HiddenCommits:     []plumbing.Hash{},
	}

	refs, err := repository.References()
	if err != nil {
		return nil, err
	}

	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := repository.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			commitIter.ForEach(func(commit *object.Commit) error {
				results.UpsertCommit(commit.Hash)
				return nil
			})
			return nil
		}
		return nil
	})

	return results, nil
	//Clone the repository
}

func (r *Runner) ResolveForks(results *results.Results) error {
	if err := r.fetchForksRecursively(results, 1); err != nil {
		fmt.Println(err)
	}

	var wg sync.WaitGroup
	for _, fork := range results.ForkURLs {
		wg.Add(1)
		go func(forkURL string) error {
			defer wg.Done()
			commits, err := resolveFork(forkURL, results)
			if err != nil {
				return err
			}
			err = fetchCommits(results.Remote, commits)
			if err != nil {
				return err
			}
			return nil
		}(fork)
	}
	wg.Wait()
	return nil
}

func (r *Runner) Start(results *results.Results) {
	results.GenerateShortSHAStrings(r.ShortShaSize)
	queries := githubBrute.CreateQueries(results.BruteCommits, r.BatchSize, results.Owner, results.Name)
	var wg sync.WaitGroup

	requestChannels := make([]chan string, len(r.Clients))
	for i := range requestChannels {
		requestChannels[i] = make(chan string)
	}

	for i, client := range r.Clients {
		wg.Add(1)
		go handleRequests(client, requestChannels[i], &wg, results, r.Threads, r.Retries)
	}

	for i, batch := range queries {
		clientIndex := i % len(r.Clients)
		requestChannels[clientIndex] <- batch // Send request to the appropriate client's channel.
	}

	for _, ch := range requestChannels {
		close(ch)
	}

	wg.Wait()
	//Batch all the queries start the client, emit callbacks etc.
}

func (r *Runner) Finish(results *results.Results) {
	batchCommits := createBatches(results.HiddenCommits, 1000)
	for batch := range batchCommits {
		fetchCommits(results.Remote, batch)
	}
	//Batch all the queries start the client, emit callbacks etc.
}

func (r *Runner) randomClient() *github.Client {
	return r.Clients[rand.Intn(len(r.Clients))]
}

func (r *Runner) fetchForksRecursively(results *results.Results, page int) error {
	client := r.randomClient()
	ctx := context.Background()
	//fetch the repository forks with page =
	opts := &github.RepositoryListForksOptions{
		ListOptions: github.ListOptions{PerPage: 100, Page: page},
	}
	forks, resp, err := client.Repositories.ListForks(ctx, results.Owner, results.Name, opts)
	if err != nil {
		return err
	}
	for _, fork := range forks {
		if fork.CloneURL != nil {
			results.ForkURLs = append(results.ForkURLs, *fork.CloneURL)
		}
	}

	header := resp.Header.Get("Link")

	links := link.Parse(header)
	for _, link := range links {
		if link.Rel == "next" {
			url, err := url.Parse(link.URI)
			if err != nil {
				return err
			}
			page := url.Query().Get("page")

			i, err := strconv.Atoi(page)
			if err != nil {
				return err
			}
			r.fetchForksRecursively(results, i)
		}
	}
	return nil
}

func resolveFork(uri string, results *results.Results) ([]plumbing.Hash, error) {
	var uniqueCommits []plumbing.Hash
	location, err := os.MkdirTemp("", "*-octotrident-repo")
	defer os.RemoveAll(location)
	if err != nil {
		return nil, err
	}
	localRepo, err := git.PlainClone(location, false, &git.CloneOptions{
		URL: uri,
	})
	if err != nil {
		return nil, err
	}

	refs, err := localRepo.References()
	if err != nil {
		return nil, err
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := localRepo.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			err = commitIter.ForEach(func(commit *object.Commit) error {
				seen := results.UpsertCommit(commit.Hash)
				if !seen {
					uniqueCommits = append(uniqueCommits, uniqueCommits...)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return uniqueCommits, nil
}

func fetchCommits(remote *git.Remote, commits []plumbing.Hash) error {
	var hashes []config.RefSpec
	for _, commit := range commits {
		remoteRef := plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remote.Config().Name, commit.String()), commit)
		refSpec := config.RefSpec(fmt.Sprintf("%v:%v", remoteRef.Hash().String(), remoteRef.Name().String()))
		hashes = append(hashes, refSpec)
	}

	err := remote.Fetch(&git.FetchOptions{
		RefSpecs: hashes,
		Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	return nil
}

func handleRequests(client *github.Client, requests <-chan string, wg *sync.WaitGroup, results *results.Results, maxThreads int, retries int) {
	defer wg.Done()
	semaphore := make(chan string, maxThreads)

	for request := range requests {
		wg.Add(1)
		semaphore <- request // Acquire a token.
		go func(query string) error {
			defer wg.Done()

			for i := retries; i >= 0; i-- {
				resp, err := githubBrute.Brute(client, query)
				if err != nil {
					fmt.Println(err)
				}
				if resp != nil {
					results.Mu.Lock()
					defer results.Mu.Unlock()
					for k, commit := range resp.Data.Repository {
						if commit.OID != nil {
							results.HiddenCommits = append(results.HiddenCommits, plumbing.NewHash(*commit.OID))
						} else {
							results.InvalidCommits = append(results.InvalidCommits, k)
						}
					}
					break
				}
			}
			<-semaphore // Release a token.
			return nil
		}(request)
	}
}

func createBatches(items []plumbing.Hash, batchSize int) <-chan []plumbing.Hash {
	out := make(chan []plumbing.Hash)
	go func() {
		defer close(out)
		itemsCopy := append([]plumbing.Hash(nil), items...)
		for len(itemsCopy) > 0 {
			end := batchSize
			if len(itemsCopy) < batchSize {
				end = len(itemsCopy)
			}
			batch := itemsCopy[:end]
			itemsCopy = itemsCopy[end:]
			out <- batch
		}
	}()
	return out
}
